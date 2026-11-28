package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	"github.com/garamsh/garam-bot/operator/internal/bottoken"
)

// The connector's run, assembled as the binary runs it, against envtest's API server serving interface
// 1's definitions (ADR 0099 decision 3).

const (
	offRelease     = "garam-system"
	offReleaseName = "garam"
	offWatched     = "bots"
	offWorkspaces  = "bot-workspaces"
	offService     = "garam-connector"
)

// startAPIServer starts envtest with the chart's CRDs, points the connector's client configuration at
// it, and makes the namespaces and objects the chart and the cluster would.
func startAPIServer(t *testing.T) kubernetes.Interface {
	t.Helper()
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "deploy", "charts", "garam-connector", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		entries, _ := os.ReadDir(filepath.Join("..", "..", "bin", "k8s"))
		for _, entry := range entries {
			if entry.IsDir() {
				environment.BinaryAssetsDirectory = filepath.Join("..", "..", "bin", "k8s", entry.Name())
			}
		}
	}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = environment.Stop() })
	user, err := environment.AddUser(envtest.User{Name: "connector-test", Groups: []string{"system:masters"}}, nil)
	require.NoError(t, err)
	kubeconfig, err := user.KubeConfig()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, kubeconfig, 0o600))
	t.Setenv("KUBECONFIG", path)

	kube, err := kubernetes.NewForConfig(config)
	require.NoError(t, err)
	ctx := context.Background()
	for _, name := range []string{offRelease, offWatched, offWorkspaces} {
		_, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{Name: name},
			metav1.CreateOptions{})
		require.NoError(t, err)
	}
	_, err = kube.CoreV1().ConfigMaps(offRelease).Create(ctx, &corev1.ConfigMap{
		Name: "garam-connector-trust",
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kube.CoordinationV1().Leases(offWatched).Create(ctx, &coordinationv1.Lease{
		Name: "garam-connector-claim",
		Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(offRelease + "/" + offReleaseName)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	// envtest runs no ServiceAccount controller, so the workspace namespace's default is made here.
	_, err = kube.CoreV1().ServiceAccounts(offWorkspaces).Create(ctx, &corev1.ServiceAccount{
		Name: "default",
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return kube
}

// freeAddress is a loopback address no listener holds now.
func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

// published waits for the serving certificate the connector publishes to its trust ConfigMap, and
// answers a client trusting it alone, as a bot's process does.
func published(t *testing.T, kube kubernetes.Interface) *http.Client {
	t.Helper()
	var bundle string
	require.Eventually(t, func() bool {
		trust, err := kube.CoreV1().ConfigMaps(offRelease).Get(context.Background(), "garam-connector-trust",
			metav1.GetOptions{})
		bundle = trust.Data[trustKey]
		return err == nil && bundle != ""
	}, 30*time.Second, 50*time.Millisecond)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(bundle)))
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, ServerName: offService + "." + offRelease + ".svc", MinVersion: tls.VersionTLS13,
	}}}
}

// answered sends a request and answers its status and its body's error code.
func answered(t *testing.T, client *http.Client, method, url, credential string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	return response.StatusCode, body.Error
}

func TestRun_ServesTheWorkspacesAndNoBotTokenWithGaramOff(t *testing.T) {
	kube := startAPIServer(t)
	address := freeAddress(t)
	o := options{
		releaseNamespace: offRelease, releaseName: offReleaseName, watchNamespaces: offWatched,
		rotationPeriod: time.Hour, identitySecret: "garam-connector-identity",
		botTokenAddress: address, botTokenService: offService, botTokenAudience: bottoken.DefaultAudience,
		trustConfigMap: "garam-connector-trust",
		workspaces: workspaceOptions{
			namespace: offWorkspaces, audience: "garam-workspace",
			helperImage:       "registry.invalid/helper:1@sha256:" + sixtyFourZeros,
			podEnforcesEgress: true, podModelURL: "https://gateway.invalid", podToolProxyURL: "https://tools.invalid",
			maxDials: 8,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, o, slog.New(slog.DiscardHandler)) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("run = %v", err)
		}
	})

	client := published(t, kube)
	base := "https://" + address

	status, code := answered(t, client, http.MethodPost, base+bottoken.Path, "any-credential")
	assert.Equal(t, http.StatusForbidden, status, "interface 6 with garam off")
	assert.Equal(t, "unknown_bot", code)
	// Interface 7 answers on the same listener: a call without a credential is refused by it, not
	// answered 404 as a path nothing serves.
	status, _ = answered(t, client, http.MethodGet, base+access.WorkspacesPath+"main", "")
	assert.Equal(t, http.StatusUnauthorized, status, "interface 7 is served")
	account, err := kube.CoreV1().ServiceAccounts(offWorkspaces).Get(context.Background(), "default", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, ptr.To(false), account.AutomountServiceAccountToken, "the workspace namespace's default is unmounted")
	_, err = kube.CoreV1().Secrets(offRelease).Get(context.Background(), o.identitySecret, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "nothing is registered: the identity Secret is never read or made, %v", err)
}

const sixtyFourZeros = "0000000000000000000000000000000000000000000000000000000000000000"
