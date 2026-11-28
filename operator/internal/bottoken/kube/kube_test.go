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
	appsv1 "k8s.io/api/apps/v1"
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

// The ServiceAccount tokens, the issuer's keys, the Pods and the trust ConfigMap are the API server's, so the
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

func TestService_AnswersAProjectedServiceAccountTokenOfTheBotsPod(t *testing.T) {
	namespace := newNamespace(t)
	botUID := createWorkload(t, namespace, "mira", true)
	createWorkload(t, namespace, "local", true)
	createPod(t, namespace, "stray", "mira", nil)
	s := startService(t, namespace, applied{
		{Namespace: namespace, Name: "mira"}: {DeclarationID: "decl-mira", UID: botUID},
	})

	refused := s.call(t, podToken(t, namespace, "local", "local-0", bottoken.DefaultAudience), "")
	assert.Equal(t, http.StatusForbidden, refused.status, "a Bot the connector did not apply is refused")
	assert.Equal(t, "unknown_bot", refused.body["error"])
	other := s.call(t, podToken(t, namespace, "mira", "mira-0", "https://kubernetes.default.svc"), "")
	assert.Equal(t, http.StatusUnauthorized, other.status, "a token for another audience is refused")
	unbound := s.call(t, serviceAccountToken(t, namespace, "mira", bottoken.DefaultAudience, nil), "")
	assert.Equal(t, http.StatusUnauthorized, unbound.status, "a token bound to no Pod is refused")
	stray := s.call(t, podToken(t, namespace, "mira", "stray", bottoken.DefaultAudience), "")
	assert.Equal(t, http.StatusUnauthorized, stray.status, "a Pod of the Bot's ServiceAccount the Bot does not run is refused")

	// Control: the token of the Pod the Bot's StatefulSet runs, for the server's audience, verifies
	// against the API server's keys and obtains garam's token.
	credential := podToken(t, namespace, "mira", "mira-0", bottoken.DefaultAudience)
	asked := s.call(t, credential, "")
	require.Equal(t, "use_dpop_nonce", asked.body["error"], asked.body)
	answered := s.call(t, credential, s.proof(t, asked.body["htu"]))
	require.Equal(t, http.StatusOK, answered.status, answered.body)
	assert.Equal(t, "grn:acme/iam/bot/decl-mira", s.subject.Load(), "the exchange names the Bot's declaration")
}

func TestPods_AnswersTheBotWhoseStatefulSetControlsThePod(t *testing.T) {
	namespace := newNamespace(t)
	botUID := createWorkload(t, namespace, "mira", true)
	createWorkload(t, namespace, "nova", false)
	// A Pod naming mira's StatefulSet as its controller by another UID: a StatefulSet since replaced.
	createPod(t, namespace, "mira-old", "mira", &metav1.OwnerReference{
		APIVersion: "apps/v1", Kind: "StatefulSet", Name: "mira", UID: "uid-of-a-set-gone", Controller: ptr.To(true),
	})
	pods := kube.Pods{Kube: clientset}

	pod, found, err := pods.Pod(t.Context(), namespace, "mira-0")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, botUID, pod.BotUID)
	assert.Equal(t, "mira", pod.ServiceAccount)
	assert.NotEmpty(t, pod.UID)

	pod, found, err = pods.Pod(t.Context(), namespace, "nova-0")
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, pod.BotUID, "a StatefulSet the Bot owns but does not control is not the Bot's workload")

	pod, found, err = pods.Pod(t.Context(), namespace, "mira-old")
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, pod.BotUID, "a Pod of a StatefulSet since replaced is not the Bot's workload")

	_, found, err = pods.Pod(t.Context(), namespace, "absent-0")
	require.NoError(t, err)
	assert.False(t, found)
}

// service is a bot token Service over envtest, with a stand-in mint, serving on a port of its own.
type service struct {
	url     string
	client  *http.Client
	key     *ecdsa.PrivateKey
	subject atomic.Value
}

// applied stands in for the declarations the connector holds.
type applied map[bottoken.Bot]bottoken.Applied

func (a applied) Applied(bot bottoken.Bot) (bottoken.Applied, bool) {
	held, ok := a[bot]
	return held, ok
}

func startService(t *testing.T, namespace string, bots applied) *service {
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tokens := bottoken.NewService(bottoken.Config{
		Record: record, Namespaces: []string{namespace},
		Keys: kube.Keys{Kube: clientset}, Bots: bots, Pods: kube.Pods{Kube: clientset}, Trust: trust,
		HTTP: mint.Client(), TokenURL: func() string { return mint.URL + "/oauth/token" },
		Actor:      func(context.Context) (string, error) { return "garam_ct_actor", nil },
		Invalidate: func(string) {},
		DNSNames:   []string{"garam-connector.garam.svc"},
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

// podToken is the token the kubelet projects into the Pod podName of ServiceAccount namespace/name, for
// audience: bound to that Pod.
func podToken(t *testing.T, namespace, name, podName, audience string) string {
	t.Helper()
	pod, err := clientset.CoreV1().Pods(namespace).Get(t.Context(), podName, metav1.GetOptions{})
	require.NoError(t, err)
	return serviceAccountToken(t, namespace, name, audience, &authenticationv1.BoundObjectReference{
		Kind: "Pod", APIVersion: "v1", Name: pod.Name, UID: pod.UID,
	})
}

// serviceAccountToken is a token the API server issues the ServiceAccount namespace/name for audience,
// bound to bound where it is set.
func serviceAccountToken(t *testing.T, namespace, name, audience string, bound *authenticationv1.BoundObjectReference) string {
	t.Helper()
	createServiceAccount(t, namespace, name)
	issued, err := clientset.CoreV1().ServiceAccounts(namespace).CreateToken(t.Context(), name,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
			Audiences: []string{audience}, ExpirationSeconds: ptr.To[int64](600), BoundObjectRef: bound,
		}}, metav1.CreateOptions{})
	require.NoError(t, err)
	return issued.Status.Token
}

func createServiceAccount(t *testing.T, namespace, name string) {
	t.Helper()
	_, err := clientset.CoreV1().ServiceAccounts(namespace).Get(t.Context(), name, metav1.GetOptions{})
	if err == nil {
		return
	}
	_, err = clientset.CoreV1().ServiceAccounts(namespace).Create(t.Context(),
		&corev1.ServiceAccount{Name: name}, metav1.CreateOptions{})
	require.NoError(t, err)
}

// createWorkload makes the Bot name, its StatefulSet, controlled by the Bot where controlled is set and
// only owned by it otherwise, and the StatefulSet's Pod <name>-0 under the ServiceAccount name, as the
// manager makes them. It answers the Bot's UID.
func createWorkload(t *testing.T, namespace, name string, controlled bool) string {
	t.Helper()
	bot := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"persona": map[string]any{"role": "Game Lead"}, "agent": map[string]any{"harness": "claude-code"}},
	}}
	bot.SetGroupVersionKind(botv1.GroupVersion.WithKind("Bot"))
	bot.SetNamespace(namespace)
	bot.SetName(name)
	require.NoError(t, k8sClient.Create(t.Context(), bot))
	labels := map[string]string{"app.kubernetes.io/instance": name}
	set, err := clientset.AppsV1().StatefulSets(namespace).Create(t.Context(), &appsv1.StatefulSet{
		Name: name, OwnerReferences: []metav1.OwnerReference{{
			APIVersion: botv1.GroupVersion.String(), Kind: "Bot", Name: name, UID: bot.GetUID(),
			Controller: ptr.To(controlled),
		}},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To[int32](1), Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: podSpec(name)},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	createPod(t, namespace, name+"-0", name, &metav1.OwnerReference{
		APIVersion: "apps/v1", Kind: "StatefulSet", Name: set.Name, UID: set.UID, Controller: ptr.To(true),
	})
	return string(bot.GetUID())
}

// createPod makes the Pod name under serviceAccount, controlled by owner where it is set. envtest runs
// no controller and no kubelet, so the Pod is made here and never runs.
func createPod(t *testing.T, namespace, name, serviceAccount string, owner *metav1.OwnerReference) {
	t.Helper()
	createServiceAccount(t, namespace, serviceAccount)
	pod := &corev1.Pod{Name: name, Spec: podSpec(serviceAccount)}
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	_, err := clientset.CoreV1().Pods(namespace).Create(t.Context(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
}

func podSpec(serviceAccount string) corev1.PodSpec {
	return corev1.PodSpec{ServiceAccountName: serviceAccount, Containers: []corev1.Container{{Name: "runtime", Image: "garam-bot"}}}
}

var spaces atomic.Int32

func newNamespace(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("bots-%d", spaces.Add(1))
	require.NoError(t, k8sClient.Create(t.Context(), &corev1.Namespace{Name: name}))
	return name
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
