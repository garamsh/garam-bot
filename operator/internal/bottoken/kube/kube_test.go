package kube_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/garamsh/garam-bot/operator/internal/bottoken"
	"github.com/garamsh/garam-bot/operator/internal/bottoken/kube"
)

// The ServiceAccount tokens, the issuer's keys and the trust ConfigMap are the API server's, so the
// adapters are tested against envtest's API server serving interface 1's definitions.
var (
	cfg       *rest.Config
	clientset *kubernetes.Clientset
	k8sClient client.Client
)

const record = "grn:acme/iam/cluster/c-1"

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	env := &envtest.Environment{}
	if dir := firstEnvTestBinaryDir(); dir != "" {
		env.BinaryAssetsDirectory = dir
	}
	definitions, err := readDefinitions()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	env.CRDs = definitions
	if cfg, err = env.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start envtest:", err)
		return 1
	}
	defer func() { _ = env.Stop() }()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if k8sClient, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if clientset, err = kubernetes.NewForConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

func TestService_AnswersAProjectedServiceAccountTokenOfAGaramOwnedBot(t *testing.T) {
	namespace := newNamespace(t)
	createBot(t, namespace, "mira", map[string]string{botv1.LabelManagedBy: "garam"}, map[string]string{
		botv1.AnnotationCluster: record, botv1.AnnotationDeclaration: "decl-mira", botv1.AnnotationDeclarationGeneration: "1",
	})
	createBot(t, namespace, "local", nil, nil)
	s := startService(t, namespace)

	refused := s.call(t, serviceAccountToken(t, namespace, "local", bottoken.DefaultAudience), "")
	assert.Equal(t, http.StatusForbidden, refused.status, "a cluster-owned Bot's token is refused")
	assert.Equal(t, "unknown_bot", refused.body["error"])

	other := s.call(t, serviceAccountToken(t, namespace, "mira", "https://kubernetes.default.svc"), "")
	assert.Equal(t, http.StatusUnauthorized, other.status, "a token for another audience is refused")

	// Control: the garam-owned Bot's token, for the server's audience, verifies against the API
	// server's keys and obtains garam's token.
	credential := serviceAccountToken(t, namespace, "mira", bottoken.DefaultAudience)
	asked := s.call(t, credential, "")
	require.Equal(t, "use_dpop_nonce", asked.body["error"], asked.body)
	answered := s.call(t, credential, s.proof(t, asked.body["htu"]))
	require.Equal(t, http.StatusOK, answered.status, answered.body)
	assert.Equal(t, "grn:acme/iam/bot/decl-mira", s.subject.Load(), "the exchange names the Bot's declaration")
}

func TestTrustStore_PublishesIntoTheConfigMapTheChartCreated(t *testing.T) {
	namespace := newNamespace(t)
	store := kube.TrustStore{Kube: clientset, Namespace: namespace, Name: "garam-connector-trust", Key: "ca.crt"}

	assert.Error(t, store.Publish(t.Context(), []byte("bundle")), "no ConfigMap is created")

	require.NoError(t, k8sClient.Create(t.Context(), &corev1.ConfigMap{
		Namespace: namespace, Name: "garam-connector-trust",
	}))
	require.NoError(t, store.Publish(t.Context(), []byte("bundle")))
	held, err := clientset.CoreV1().ConfigMaps(namespace).Get(t.Context(), "garam-connector-trust", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ca.crt": "bundle"}, held.Data)
}

// service is a bot token Service over envtest, with a stand-in mint, serving on a port of its own.
type service struct {
	url     string
	client  *http.Client
	key     *ecdsa.PrivateKey
	subject atomic.Value
}

func startService(t *testing.T, namespace string) *service {
	t.Helper()
	s := &service{}
	var err error
	s.key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	mint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.subject.Store(r.PostForm.Get("subject_token"))
		w.Header().Set("DPoP-Nonce", "nonce-1")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "garam_bt_1", "token_type": "DPoP", "expires_in": 3600})
	}))
	t.Cleanup(mint.Close)
	trust := &bundle{}
	reader, err := client.New(cfg, client.Options{Scheme: botScheme(t)})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tokens := bottoken.NewService(bottoken.Config{
		Record: record, Namespaces: []string{namespace},
		Keys: kube.Keys{Kube: clientset}, Bots: kube.Bots{Reader: reader}, Trust: trust,
		HTTP: mint.Client(), TokenURL: mint.URL + "/oauth/token",
		Actor:    func(context.Context) (string, error) { return "garam_ct_actor", nil },
		DNSNames: []string{"garam-connector.garam.svc"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tokens.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	require.Eventually(t, func() bool { return trust.load() != nil }, 5*time.Second, 10*time.Millisecond)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(trust.load()))
	s.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, ServerName: "garam-connector.garam.svc", MinVersion: tls.VersionTLS13,
	}}}
	s.url = "https://" + listener.Addr().String() + bottoken.Path
	return s
}

type answer struct {
	status int
	body   map[string]any
}

func (s *service) call(t *testing.T, credential, proof string) answer {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, s.url, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+credential)
	if proof != "" {
		request.Header.Set("DPoP", proof)
	}
	response, err := s.client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	body := map[string]any{}
	require.NoError(t, json.Unmarshal(read, &body))
	return answer{status: response.StatusCode, body: body}
}

func (s *service) proof(t *testing.T, htu any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.key},
		(&jose.SignerOptions{EmbedJWK: true}).WithType("dpop+jwt"))
	require.NoError(t, err)
	raw, err := jwt.Signed(signer).Claims(map[string]any{
		"htm": "POST", "htu": htu, "iat": time.Now().Unix(), "jti": rand.Text(), "nonce": "nonce-1",
	}).Serialize()
	require.NoError(t, err)
	return raw
}

// bundle stands in for the trust ConfigMap where the service, not the store, is under test.
type bundle struct {
	held atomic.Value
}

func (b *bundle) Publish(_ context.Context, published []byte) error {
	b.held.Store(published)
	return nil
}

func (b *bundle) load() []byte {
	held, _ := b.held.Load().([]byte)
	return held
}

// serviceAccountToken is a token the API server issues the ServiceAccount namespace/name for
// audience, as the kubelet projects one into a Bot's Pod.
func serviceAccountToken(t *testing.T, namespace, name, audience string) string {
	t.Helper()
	_, err := clientset.CoreV1().ServiceAccounts(namespace).Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		_, err = clientset.CoreV1().ServiceAccounts(namespace).Create(t.Context(),
			&corev1.ServiceAccount{Name: name}, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	issued, err := clientset.CoreV1().ServiceAccounts(namespace).CreateToken(t.Context(), name,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
			Audiences: []string{audience}, ExpirationSeconds: ptr.To[int64](600),
		}}, metav1.CreateOptions{})
	require.NoError(t, err)
	return issued.Status.Token
}

func createBot(t *testing.T, namespace, name string, labels, annotations map[string]string) {
	t.Helper()
	bot := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"persona": map[string]any{"role": "Game Lead"}, "agent": map[string]any{"harness": "claude-code"}},
	}}
	bot.SetGroupVersionKind(botv1.GroupVersion.WithKind("Bot"))
	bot.SetNamespace(namespace)
	bot.SetName(name)
	bot.SetLabels(labels)
	bot.SetAnnotations(annotations)
	require.NoError(t, k8sClient.Create(t.Context(), bot))
}

var spaces atomic.Int32

func newNamespace(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("bots-%d", spaces.Add(1))
	require.NoError(t, k8sClient.Create(t.Context(), &corev1.Namespace{Name: name}))
	return name
}

func botScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, botv1.AddToScheme(scheme))
	return scheme
}

// readDefinitions are interface 1's CustomResourceDefinitions, as the chart installs them.
func readDefinitions() ([]*apiextv1.CustomResourceDefinition, error) {
	var out []*apiextv1.CustomResourceDefinition
	err := fs.WalkDir(botv1.CRDs, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		read, err := fs.ReadFile(botv1.CRDs, path)
		if err != nil {
			return err
		}
		definition := &apiextv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(read, definition); err != nil {
			return err
		}
		out = append(out, definition)
		return nil
	})
	return out, err
}

// firstEnvTestBinaryDir finds the binaries `make setup-envtest` downloads, so the tests run from an IDE
// without KUBEBUILDER_ASSETS set.
func firstEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
