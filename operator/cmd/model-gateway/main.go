// Command model-gateway is interface 5's model gateway at a cluster (garam-bot ADR 0044): it issues
// each bot's model token to the connector, and forwards the bots' model calls to the provider with the
// organization's own key. It runs apart from the connector, so the provider's key stays out of the
// program that holds the cluster's identity.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	botkube "github.com/garamsh/garam-bot/operator/internal/bottoken/kube"
	"github.com/garamsh/garam-bot/operator/internal/certificate"
	"github.com/garamsh/garam-bot/operator/internal/modelgateway"
	gatewaykube "github.com/garamsh/garam-bot/operator/internal/modelgateway/kube"
)

// keyReread is how often the provider key file is read again, as the kubelet replaces a mounted
// Secret's files within about a minute of the Secret's change.
const keyReread = 10 * time.Second

type options struct {
	address         string
	metricsAddress  string
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
	flags.IntVar(&o.limits.BotCalls, "bot-calls", 8, "How many model calls one bot may hold at once.")
	flags.IntVar(&o.limits.Calls, "calls", 32,
		"How many model calls the gateway holds at once, each with up to 32 MiB of its body in memory.")
	flags.StringVar(&o.metricsAddress, "metrics-address", ":9090",
		"The address the gateway serves its metrics on, over plain HTTP, apart from its TLS surfaces.")
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
	registry := prometheus.NewRegistry()
	metrics, err := modelgateway.NewMetrics(registry)
	if err != nil {
		return err
	}
	cfg, key, err := configOf(o, recordUsage(log, metrics))
	if err != nil {
		return err
	}
	cfg.Log = log
	go key.Watch(ctx, keyReread, log)
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
	metricsListener, err := net.Listen("tcp", o.metricsAddress)
	if err != nil {
		return fmt.Errorf("listen for metrics: %w", err)
	}
	go serveMetrics(ctx, metricsListener, registry, log)
	listener, err := net.Listen("tcp", o.address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return modelgateway.Serve(ctx, listener, gateway, certificates.GetCertificate, log)
}

// serveMetrics serves registry's metrics on listener until ctx ends.
func serveMetrics(ctx context.Context, listener net.Listener, registry *prometheus.Registry, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("the metrics server stopped", "error", err)
	}
}

// configOf checks the options and reads the provider's key, refusing to start without one (KEY-2).
// The key file it answers is read again while its Watch runs.
func configOf(o options, record func(modelgateway.Usage)) (modelgateway.Config, *modelgateway.KeyFile, error) {
	switch {
	case o.certFile == "" || o.keyFile == "":
		return modelgateway.Config{}, nil, errors.New(
			"--tls-cert-file and --tls-key-file are required: the gateway serves HTTPS alone (T-1)")
	case o.providerKeyFile == "":
		return modelgateway.Config{}, nil, errors.New(
			"--provider-key-file is required: a gateway with no key serves no model call (KEY-2)")
	case o.issuer == "" || o.namespace == "":
		return modelgateway.Config{}, nil, errors.New("--issuer and --namespace are required")
	}
	upstream, err := url.Parse(o.upstream)
	if err != nil || upstream.Scheme != "https" || upstream.Host == "" || upstream.User != nil {
		return modelgateway.Config{}, nil, errors.New("--upstream-url is no https URL without credentials")
	}
	key, err := modelgateway.NewKeyFile(o.providerKeyFile)
	if err != nil {
		return modelgateway.Config{}, nil, fmt.Errorf("a gateway with no key serves no model call (KEY-2): %w", err)
	}
	return modelgateway.Config{
		BasePath: o.basePath, Upstream: upstream, Key: key, Issuer: o.issuer, Limits: o.limits, Record: record,
	}, key, nil
}

// recordUsage logs each forwarded call's usage, which holds no content, token or key, and counts it in
// metrics (USE-1, SEC-1). The model is as boundedModel records it.
func recordUsage(log *slog.Logger, metrics *modelgateway.Metrics) func(modelgateway.Usage) {
	return func(u modelgateway.Usage) {
		log.Info("model call",
			"namespace", u.Bot.Namespace, "bot", u.Bot.Name, "uid", u.Bot.UID, "model", u.Model, "status", u.Status,
			"inputTokens", u.InputTokens, "outputTokens", u.OutputTokens,
			"cacheCreationInputTokens", u.CacheCreationInputTokens, "cacheReadInputTokens", u.CacheReadInputTokens)
		metrics.Record(u)
	}
}

// apiServerKeys are the API server's keys for the gateway, read as interface 6 reads them.
type apiServerKeys struct {
	keys botkube.Keys
}

func (k apiServerKeys) Keys(ctx context.Context) (modelgateway.Issuer, error) {
	read, err := k.keys.Keys(ctx)
	return modelgateway.Issuer{URL: read.URL, Keys: read.Keys}, err
}
