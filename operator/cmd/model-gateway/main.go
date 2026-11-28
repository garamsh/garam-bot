// Command model-gateway is interface 5's model gateway at a cluster (garam-bot ADR 0044): it issues
// each bot's model token to the connector, and forwards the bots' model calls to the provider with the
// organization's own key. It runs apart from the connector, so the provider's key stays out of the
// program that holds the cluster's identity.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	botkube "github.com/garamsh/garam-bot/operator/internal/bottoken/kube"
	"github.com/garamsh/garam-bot/operator/internal/certificate"
	"github.com/garamsh/garam-bot/operator/internal/modelgateway"
	gatewaykube "github.com/garamsh/garam-bot/operator/internal/modelgateway/kube"
)

type options struct {
	address         string
	certFile        string
	keyFile         string
	basePath        string
	upstream        string
	providerKeyFile string
	issuer          string
	namespace       string
	signingSecret   string
	limits          modelgateway.Limits
}

func (o *options) register(flags *flag.FlagSet) {
	flags.StringVar(&o.address, "listen-address", ":8443", "The address the gateway serves HTTPS on.")
	flags.StringVar(&o.certFile, "tls-cert-file", "",
		"PEM file of the serving certificate chain, read again when it changes.")
	flags.StringVar(&o.keyFile, "tls-key-file", "",
		"PEM file of the serving certificate's key, read again when it changes.")
	flags.StringVar(&o.basePath, "base-path", "",
		"The path of the gateway's base URL, below which both surfaces are. Empty is the root.")
	flags.StringVar(&o.upstream, "upstream-url", "https://api.anthropic.com",
		"The provider's API the model surface is forwarded to.")
	flags.StringVar(&o.providerKeyFile, "provider-key-file", "",
		"File holding the organization's own provider key, read once at start. It is never logged.")
	flags.StringVar(&o.issuer, "issuer", "",
		"The one ServiceAccount, as <namespace>/<name>, whose projected token of the audience garam-model-gateway "+
			"obtains model tokens: the connector's.")
	flags.StringVar(&o.namespace, "namespace", "",
		"The namespace the gateway runs in, which holds its signing key's Secret.")
	flags.StringVar(&o.signingSecret, "signing-key-secret", "garam-model-gateway-signing-key",
		"The Secret the gateway writes its signing key to once, and every replica reads it from.")
	flags.IntVar(&o.limits.Bot.Burst, "bot-burst", 60, "How many model calls a bot may make at once.")
	flags.DurationVar(&o.limits.Bot.Every, "bot-every", time.Second, "How long a bot's model call takes to come back.")
	flags.IntVar(&o.limits.Issuer.Burst, "issuer-burst", 60, "How many model tokens the issuer may obtain at once.")
	flags.DurationVar(&o.limits.Issuer.Every, "issuer-every", time.Second,
		"How long an issuer's token call takes to come back.")
}

func main() {
	var o options
	o.register(flag.CommandLine)
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("model gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options, log *slog.Logger) error {
	cfg, err := configOf(o, log)
	if err != nil {
		return err
	}
	certificates, err := certificate.NewReloader(o.certFile, o.keyFile)
	if err != nil {
		return fmt.Errorf("load the serving certificate: %w", err)
	}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return fmt.Errorf("load the cluster's configuration: %w", err)
	}
	kube, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	cfg.Keys = apiServerKeys{botkube.Keys{Kube: kube}}
	cfg.SigningKeys = gatewaykube.SigningKeys{Kube: kube, Namespace: o.namespace, Name: o.signingSecret}
	gateway, err := modelgateway.New(ctx, cfg)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", o.address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return serve(ctx, listener, gateway, certificates.GetCertificate, log)
}

// configOf checks the options and reads the provider's key, refusing to start without one (KEY-2).
func configOf(o options, log *slog.Logger) (modelgateway.Config, error) {
	switch {
	case o.certFile == "" || o.keyFile == "":
		return modelgateway.Config{}, errors.New(
			"--tls-cert-file and --tls-key-file are required: the gateway serves HTTPS alone (T-1)")
	case o.providerKeyFile == "":
		return modelgateway.Config{}, errors.New(
			"--provider-key-file is required: a gateway with no key serves no model call (KEY-2)")
	case o.issuer == "" || o.namespace == "":
		return modelgateway.Config{}, errors.New("--issuer and --namespace are required")
	}
	upstream, err := url.Parse(o.upstream)
	if err != nil || upstream.Scheme != "https" || upstream.Host == "" || upstream.User != nil {
		return modelgateway.Config{}, errors.New("--upstream-url is no https URL without credentials")
	}
	read, err := os.ReadFile(o.providerKeyFile)
	if err != nil {
		return modelgateway.Config{}, fmt.Errorf("read the provider key file: %w", err)
	}
	key := strings.TrimSpace(string(read))
	if key == "" {
		return modelgateway.Config{}, errors.New(
			"the provider key file is empty: a gateway with no key serves no model call (KEY-2)")
	}
	return modelgateway.Config{
		BasePath: o.basePath, Upstream: upstream, Key: key, Issuer: o.issuer, Limits: o.limits,
		Record: recordUsage(log),
	}, nil
}

// recordUsage logs each forwarded call's usage, which holds no content, token or key (USE-1, SEC-1).
func recordUsage(log *slog.Logger) func(modelgateway.Usage) {
	return func(u modelgateway.Usage) {
		log.Info("model call",
			"namespace", u.Bot.Namespace, "bot", u.Bot.Name, "uid", u.Bot.UID, "model", u.Model, "status", u.Status,
			"inputTokens", u.InputTokens, "outputTokens", u.OutputTokens,
			"cacheCreationInputTokens", u.CacheCreationInputTokens, "cacheReadInputTokens", u.CacheReadInputTokens)
	}
}

// serve serves gateway over TLS on listener until ctx ends, then lets the calls it holds finish for
// up to a minute.
func serve(ctx context.Context, listener net.Listener, gateway http.Handler,
	get func(*tls.ClientHelloInfo) (*tls.Certificate, error), log *slog.Logger) error {
	server := &http.Server{
		Handler:           gateway,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: get},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// A refused call's error is the gateway's to answer; the server's own are kept out of the log,
		// as a client's handshake error names no secret but is not the gateway's to report.
		ErrorLog: slog.NewLogLogger(slog.DiscardHandler, slog.LevelError),
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	log.Info("model gateway serves", "address", listener.Addr().String())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return err
	}
	return ctx.Err()
}

// apiServerKeys are the API server's keys for the gateway, read as interface 6 reads them.
type apiServerKeys struct {
	keys botkube.Keys
}

func (k apiServerKeys) Keys(ctx context.Context) (modelgateway.Issuer, error) {
	read, err := k.keys.Keys(ctx)
	return modelgateway.Issuer{URL: read.URL, Keys: read.Keys}, err
}
