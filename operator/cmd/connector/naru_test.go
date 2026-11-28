package main

import (
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The release namespace and the connector's Deployment the tests name.
const (
	namespace  = "garam-system"
	deployment = "garam-connector"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// completeNaru is a whole naru configuration, its root a PEM file.
func completeNaru(t *testing.T) naruOptions {
	t.Helper()
	root := filepath.Join(t.TempDir(), "naru-root.pem")
	cert := selfSigned(t)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	require.NoError(t, os.WriteFile(root, encoded, 0o600))
	return naruOptions{
		url:           "https://orch.example:7600",
		rootFile:      root,
		tokenFile:     filepath.Join(t.TempDir(), "token"),
		callerSecret:  "garam-connector-naru-caller",
		proxyAddress:  "proxy.example:443",
		rootDiskBytes: 1 << 30,
		deployment:    deployment,
	}
}

func TestNaruProvider_IsOffWithoutAnyOption(t *testing.T) {
	provider, err := naruProvider(naruOptions{}, fake.NewClientset(), namespace, quiet())
	require.NoError(t, err)
	assert.Nil(t, provider)
}

func TestNaruProvider_RefusesAnIncompleteConfiguration(t *testing.T) {
	complete := completeNaru(t)
	provider, err := naruProvider(complete, fake.NewClientset(), namespace, quiet())
	require.NoError(t, err, "control: the whole configuration builds the provider")
	require.NotNil(t, provider)

	for name, breakIt := range map[string]func(*naruOptions){
		"no URL":             func(o *naruOptions) { o.url = "" },
		"no root file":       func(o *naruOptions) { o.rootFile = "" },
		"a root of no PEM":   func(o *naruOptions) { o.rootFile = o.tokenFile },
		"no token file":      func(o *naruOptions) { o.tokenFile = "" },
		"no caller Secret":   func(o *naruOptions) { o.callerSecret = "" },
		"no proxy address":   func(o *naruOptions) { o.proxyAddress = "" },
		"no root disk":       func(o *naruOptions) { o.rootDiskBytes = 0 },
		"no Deployment":      func(o *naruOptions) { o.deployment = "" },
		"an http URL":        func(o *naruOptions) { o.url = "http://orch.example:7600" },
		"no proxy root file": func(o *naruOptions) { o.proxyRootFile = filepath.Join(t.TempDir(), "absent.pem") },
	} {
		t.Run(name, func(t *testing.T) {
			o := complete
			breakIt(&o)
			provider, err := naruProvider(o, fake.NewClientset(), namespace, quiet())
			require.Error(t, err)
			assert.Nil(t, provider)
		})
	}
}

func TestDeploymentEvents_RaisesAWarningOnTheConnectorsDeployment(t *testing.T) {
	kube := fake.NewClientset()
	events := deploymentEvents{kube: kube, namespace: namespace, deployment: deployment, log: quiet()}
	events.warn("the Machine API caller certificate renews no more")

	list, err := kube.CoreV1().Events(namespace).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	event := list.Items[0]
	assert.Equal(t, corev1.EventTypeWarning, event.Type)
	assert.Equal(t, naruWarningReason, event.Reason)
	assert.Equal(t, "the Machine API caller certificate renews no more", event.Message)
	want := corev1.ObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: namespace, Name: deployment}
	assert.Equal(t, want, event.InvolvedObject)
}
