package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/garamsh/garam-bot/pkg/naruworkspace"
	"github.com/garamsh/garam-bot/pkg/naruworkspace/secret"
)

// naruOptions configure interface 3's naru Machine provider, which the connector runs where a naru
// fleet is reached (garam-bot#1209). With url empty it is off, and every other option must be too.
type naruOptions struct {
	url           string
	rootFile      string
	tokenFile     string
	callerSecret  string
	proxyAddress  string
	proxyRootFile string
	rootDiskBytes uint64
	deployment    string
}

func (o *naruOptions) register(fs *flag.FlagSet) {
	fs.StringVar(&o.url, "naru-url", "",
		"orch's Machine API base URL, https, without /machines/v1alpha1. Empty, the naru Machine provider is off.")
	fs.StringVar(&o.rootFile, "naru-root-file", "",
		"PEM file holding the root orch's certificate chains to. Nothing else is trusted for it.")
	fs.StringVar(&o.tokenFile, "naru-registration-token-file", "",
		"File holding naru's one-time caller registration token, read only while no caller certificate is held. "+
			"It is never logged.")
	fs.StringVar(&o.callerSecret, "naru-caller-secret", "",
		"The Secret in the release namespace that holds the naru caller's key and certificate. The chart creates it empty.")
	fs.StringVar(&o.proxyAddress, "naru-proxy-address", "",
		"naru-proxy's host:port, which a workspace's port is dialled through.")
	fs.StringVar(&o.proxyRootFile, "naru-proxy-root-file", "",
		"PEM file holding the root naru-proxy's certificate chains to. Empty, the system's roots.")
	fs.Uint64Var(&o.rootDiskBytes, "naru-root-disk-bytes", 0, "The root disk of every workspace's Machine, in bytes.")
	fs.StringVar(&o.deployment, "connector-deployment", "",
		"The connector's Deployment in the release namespace, which a Warning Event the provider raises names.")
}

// set reports whether any option is given.
func (o naruOptions) set() bool {
	return o != naruOptions{}
}

// naruProvider builds the naru Machine provider from o, or answers nil where it is off. A partial
// configuration is refused, so the connector does not start on one.
func naruProvider(
	o naruOptions, kube kubernetes.Interface, namespace string, log *slog.Logger,
) (*naruworkspace.Provider, error) {
	if o.url == "" {
		if o.set() {
			return nil, errors.New("a --naru-* option is set without --naru-url")
		}
		return nil, nil
	}
	if o.rootFile == "" || o.tokenFile == "" || o.callerSecret == "" || o.proxyAddress == "" || o.rootDiskBytes == 0 ||
		o.deployment == "" {
		return nil, errors.New("with --naru-url, --naru-root-file, --naru-registration-token-file, --naru-caller-secret, " +
			"--naru-proxy-address, --naru-root-disk-bytes and --connector-deployment are all required")
	}
	roots, err := readRoots(o.rootFile)
	if err != nil {
		return nil, err
	}
	var proxyRoots *x509.CertPool
	if o.proxyRootFile != "" {
		if proxyRoots, err = readRoots(o.proxyRootFile); err != nil {
			return nil, err
		}
	}
	events := deploymentEvents{kube: kube, namespace: namespace, deployment: o.deployment, log: log}
	provider, err := naruworkspace.New(naruworkspace.Config{
		URL:           o.url,
		Roots:         roots,
		TokenFile:     o.tokenFile,
		Store:         secret.New(kube, namespace, o.callerSecret),
		ProxyAddress:  o.proxyAddress,
		ProxyRoots:    proxyRoots,
		RootDiskBytes: o.rootDiskBytes,
		Warn:          events.warn,
		Log:           log.With("component", "naru-workspace"),
	})
	if err != nil {
		return nil, err
	}
	return provider, nil
}

// readRoots reads a PEM file of root certificates.
func readRoots(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the root file: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return roots, nil
}

// naruWarningReason is the reason of the Warning Event the naru Machine provider raises.
const naruWarningReason = "NaruCallerCertificate"

// deploymentEvents raises the provider's warnings as Warning Events on the connector's Deployment,
// where the cluster's operator sees them.
type deploymentEvents struct {
	kube       kubernetes.Interface
	namespace  string
	deployment string
	log        *slog.Logger
}

func (d deploymentEvents) warn(message string) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	now := metav1.NewTime(time.Now())
	_, err := d.kube.CoreV1().Events(d.namespace).Create(ctx, &corev1.Event{
		GenerateName: d.deployment + ".",
		Namespace:    d.namespace,
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: d.namespace, Name: d.deployment,
		},
		Reason:         naruWarningReason,
		Message:        message,
		Type:           corev1.EventTypeWarning,
		Source:         corev1.EventSource{Component: "garam-connector"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}, metav1.CreateOptions{})
	if err != nil {
		d.log.Error("raise the naru Machine provider's warning as an Event", "error", err)
	}
}
