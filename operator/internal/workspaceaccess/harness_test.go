package workspaceaccess_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/api/workspace/v1alpha1/workspacefake"
	"github.com/garamsh/garam-bot/operator/internal/workspaceaccess"
)

const (
	issuerURL   = "https://kubernetes.default.svc"
	watched     = "game-team"
	mira        = "mira"
	harnessName = "claude-code"
	image       = "registry.invalid/agent@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	token       = access.TokenDir + "agent/token"
	// podProvider is the one provider the place runs.
	podProvider = "pod"
	// mainPath is the Bot's one attachment's workspace.
	mainPath = access.WorkspacesPath + "main"
	// nameClaim names an object in a credential's kubernetes.io claim.
	nameClaim = "name"
)

// apiServer stands in for the API server: its ServiceAccount issuer's key and the Pods it holds.
type apiServer struct {
	key *ecdsa.PrivateKey

	mu   sync.Mutex
	pods map[string]heldPod // by name
	read int
}

// heldPod is a Pod and its namespace.
type heldPod struct {
	namespace string
	pod       workspaceaccess.Pod
}

func (a *apiServer) Keys(context.Context) (workspaceaccess.Issuer, error) {
	public := jose.JSONWebKey{Key: &a.key.PublicKey, KeyID: "sa-key", Algorithm: string(jose.ES256), Use: "sig"}
	return workspaceaccess.Issuer{URL: issuerURL, Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{public}}}, nil
}

func (a *apiServer) Pod(_ context.Context, namespace, name string) (workspaceaccess.Pod, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.read++
	held, ok := a.pods[name]
	if !ok || held.namespace != namespace {
		return workspaceaccess.Pod{}, false, nil
	}
	return held.pod, true, nil
}

// setPod replaces the Pod of the name, in the namespace it is in.
func (a *apiServer) setPod(name string, pod workspaceaccess.Pod) {
	a.mu.Lock()
	defer a.mu.Unlock()
	held := a.pods[name]
	held.pod = pod
	a.pods[name] = held
}

func (a *apiServer) setPodIn(namespace, name string, pod workspaceaccess.Pod) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pods[name] = heldPod{namespace: namespace, pod: pod}
}

func (a *apiServer) endPod(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pods, name)
}

// namespaces stands in for the watched namespaces: their claims, Bots and WorkspaceClasses.
type namespaces struct {
	mu        sync.Mutex
	bots      map[workspaceaccess.Bot]workspaceaccess.Declared
	unclaimed map[string]bool
}

// Served answers every namespace claimed but those the test unclaims, so the watched namespaces alone
// stand between a caller and an unwatched one.
func (n *namespaces) Served(_ context.Context, namespace string) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return !n.unclaimed[namespace], nil
}

func (n *namespaces) set(change func(*namespaces)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	change(n)
}

func (n *namespaces) Bot(_ context.Context, b workspaceaccess.Bot) (workspaceaccess.Declared, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	d, ok := n.bots[b]
	return d, ok, nil
}

// UID reads a Bot as the API server holds it now, which these stand-ins keep in the same map.
func (n *namespaces) UID(_ context.Context, b workspaceaccess.Bot) (string, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	d, ok := n.bots[b]
	return d.UID, ok, nil
}

func (n *namespaces) Class(_ context.Context, _, name string) (workspaceaccess.Class, bool, error) {
	resources := workspace.Resources{CPU: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 30}
	return workspaceaccess.Class{Provider: podProvider, Resources: resources},
		name == "standard", nil
}

// provider is workspacefake with a failure a test plants: on a call, or on a write of one path.
type provider struct {
	*workspacefake.Provider

	mu       sync.Mutex
	failWith map[string]error // by call
	failAt   string
	panicOn  string
	// calls counts the calls that reach the provider, by method.
	calls map[string]int
	// stuck, while set, holds every Get or Dial until it is closed, whatever its context says.
	stuckGet, stuckDial chan struct{}
}

// set changes what the provider fails on.
func (p *provider) set(change func(*provider)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(p)
}

func (p *provider) fail(call string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[call]++
	if p.panicOn == call {
		panic("the provider's " + call + " panicked")
	}
	return p.failWith[call]
}

func (p *provider) Create(ctx context.Context, name workspace.Name, spec workspace.Spec) (workspace.Workspace, bool, error) {
	if err := p.fail("Create"); err != nil {
		return workspace.Workspace{}, false, err
	}
	return p.Provider.Create(ctx, name, spec)
}

// called answers how many calls reached the provider, of every method.
func (p *provider) called() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		n += c
	}
	return n
}

// gets answers how many Gets reached the provider.
func (p *provider) gets() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls["Get"]
}

func (p *provider) Capabilities(ctx context.Context) (workspace.Capabilities, error) {
	if err := p.fail("Capabilities"); err != nil {
		return workspace.Capabilities{}, err
	}
	return p.Provider.Capabilities(ctx)
}

func (p *provider) Get(ctx context.Context, name workspace.Name) (workspace.Workspace, error) {
	if err := p.fail("Get"); err != nil {
		return workspace.Workspace{}, err
	}
	p.mu.Lock()
	stuck := p.stuckGet
	p.mu.Unlock()
	if stuck != nil {
		<-stuck
	}
	return p.Provider.Get(ctx, name)
}

func (p *provider) Dial(ctx context.Context, name workspace.Name, port uint16) (net.Conn, error) {
	if err := p.fail("Dial"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	stuck := p.stuckDial
	p.mu.Unlock()
	if stuck != nil {
		<-stuck
	}
	return p.Provider.Dial(ctx, name, port)
}

func (p *provider) WriteFile(ctx context.Context, name workspace.Name, path string, content []byte) error {
	p.mu.Lock()
	failAt := p.failAt
	p.mu.Unlock()
	if path == failAt {
		return io.ErrUnexpectedEOF
	}
	return p.Provider.WriteFile(ctx, name, path, content)
}

// issuer answers session tokens it records, so a test can look for them where they must not be.
type issuer struct {
	mu     sync.Mutex
	issued []string
}

func (i *issuer) Available(context.Context) bool { return true }

func (i *issuer) Tokens(_ context.Context, _ workspaceaccess.Bot, tools []string) (access.SessionTokens, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	answer := access.SessionTokens{Model: access.Token{Token: "model_" + rand.Text(), ExpiresIn: 3600}, Tools: map[string]access.Token{}}
	i.issued = append(i.issued, answer.Model.Token)
	for _, tool := range tools {
		answer.Tools[tool] = access.Token{Token: "tool_" + rand.Text(), ExpiresIn: 3600}
		i.issued = append(i.issued, answer.Tools[tool].Token)
	}
	return answer, nil
}

// syncBuffer is a log a server writes while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// harness is a workspace access service over stand-ins, served over TLS.
type harness struct {
	t          *testing.T
	api        *apiServer
	namespaces *namespaces
	provider   *provider
	issuer     *issuer
	log        *syncBuffer
	server     *httptest.Server
	service    workspaceaccess.Service
	// offset is how far the service's clock is moved past the wall clock, in nanoseconds.
	offset atomic.Int64
}

type options struct {
	maxDials     int
	bindingFresh time.Duration
	dialTimeout  time.Duration
}

// advance moves the service's clock by d. Proofs are signed by the wall clock, so a test that
// advances it past the proof window makes no call after.
func (h *harness) advance(d time.Duration) { h.offset.Add(int64(d)) }

func newHarness(t *testing.T, o options) *harness {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	h := &harness{
		t:          t,
		api:        &apiServer{key: key, pods: map[string]heldPod{}},
		namespaces: &namespaces{bots: map[workspaceaccess.Bot]workspaceaccess.Declared{}, unclaimed: map[string]bool{}},
		provider: &provider{
			Provider: workspacefake.New(workspace.Capabilities{Labels: true}), failWith: map[string]error{},
			calls: map[string]int{},
		},
		issuer: &issuer{},
		log:    &syncBuffer{},
	}
	var handler = http.NotFoundHandler()
	h.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	h.server.StartTLS()
	t.Cleanup(h.server.Close)
	h.service, err = workspaceaccess.NewService(workspaceaccess.Config{
		Release:    workspaceaccess.Release{Namespace: "garam-system", Name: "garam"},
		Namespaces: []string{watched}, Origins: []string{h.server.URL},
		Keys: h.api, Pods: h.api, Bots: h.namespaces, LiveBots: h.namespaces,
		Places: map[string]workspaceaccess.Place{podProvider: {
			Provider: h.provider, ModelURL: "https://gateway.invalid/v1", ToolProxyURL: "https://tools.invalid",
		}},
		Images: map[string]string{harnessName: image}, Issuer: h.issuer,
		MaxDials: o.maxDials, BindingFresh: o.bindingFresh, Recheck: 20 * time.Millisecond, DialTimeout: o.dialTimeout,
		Now: func() time.Time { return time.Now().Add(time.Duration(h.offset.Load())) },
		Log: slog.New(slog.NewJSONHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	require.NoError(t, err)
	handler = h.service.Handler()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.service.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return h
}

// bot places mira in the watched namespace, with the attachment main and the tool github.
func (h *harness) bot() workspaceaccess.Declared { return h.botIn(watched) }

func (h *harness) botIn(namespace string) workspaceaccess.Declared {
	declared := workspaceaccess.Declared{
		UID: "bot-" + rand.Text(), Harness: harnessName, Tools: []string{"github"},
		Attachments: map[string]workspaceaccess.Attachment{"main": {Class: "standard"}},
	}
	h.namespaces.mu.Lock()
	h.namespaces.bots[workspaceaccess.Bot{Namespace: namespace, Name: mira}] = declared
	h.namespaces.mu.Unlock()
	return declared
}

// process is an instance of the Bot: its Pod, its credential, and the key its proofs are signed with.
type process struct {
	h          *harness
	podName    string
	pod        workspaceaccess.Pod
	credential string
	key        *ecdsa.PrivateKey
}

// instance starts an instance of declared's Bot, in the watched namespace.
func (h *harness) instance(declared workspaceaccess.Declared) *process {
	h.t.Helper()
	return h.instanceIn(watched, declared)
}

// instanceIn starts an instance of declared's Bot in namespace, its credential's claims changed by
// change.
func (h *harness) instanceIn(namespace string, declared workspaceaccess.Declared, change ...claimChange) *process {
	h.t.Helper()
	name := "mira-" + strings.ToLower(rand.Text()[:6])
	pod := workspaceaccess.Pod{
		UID: "pod-" + rand.Text(), ServiceAccount: mira, BotUID: declared.UID, Container: "containerd://" + rand.Text(),
	}
	h.api.setPodIn(namespace, name, pod)
	p := &process{h: h, podName: name, pod: pod, key: h.key()}
	p.credential = h.credential(namespace, name, pod.UID, change...)
	return p
}

// claimChange changes a credential's registered claims or its kubernetes.io claim before it is signed.
type claimChange func(claims *jwt.Claims, bound map[string]any)

func (h *harness) key() *ecdsa.PrivateKey {
	h.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(h.t, err)
	return key
}

// credential signs a projected ServiceAccount token bound to the Pod, as the API server does.
func (h *harness) credential(namespace, podName, podUID string, change ...claimChange) string {
	h.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: h.api.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "sa-key"))
	require.NoError(h.t, err)
	now := time.Now()
	claims := jwt.Claims{
		Issuer: issuerURL, Subject: "system:serviceaccount:" + namespace + ":" + mira,
		Audience: jwt.Audience{workspaceaccess.DefaultAudience},
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}
	kubernetesIO := map[string]any{
		"namespace": namespace, "pod": map[string]string{nameClaim: podName, "uid": podUID},
		"serviceaccount": map[string]string{nameClaim: mira, "uid": "sa-uid"},
	}
	for _, c := range change {
		c(&claims, kubernetesIO)
	}
	bound := map[string]any{"kubernetes.io": kubernetesIO}
	raw, err := jwt.Signed(signer).Claims(claims).Claims(bound).Serialize()
	require.NoError(h.t, err)
	return raw
}

// proof signs a DPoP proof of the request with the process's key.
func (p *process) proof(method, path string) string {
	p.h.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: p.key}, (&jose.SignerOptions{
		EmbedJWK: true, ExtraHeaders: map[jose.HeaderKey]any{jose.HeaderType: "dpop+jwt"},
	}))
	require.NoError(p.h.t, err)
	digest := sha256.Sum256([]byte(p.credential))
	claims := map[string]any{
		"htm": method, "htu": p.h.server.URL + path, "iat": time.Now().Unix(), "jti": rand.Text(),
		"ath": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(p.h.t, err)
	return raw
}

// answered is a call's answer.
type answered struct {
	status int
	code   access.Code
	path   string
	header http.Header
	raw    string
}

func (p *process) call(method, path string, body any) answered {
	p.h.t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(p.h.t, err)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, p.h.server.URL+path, reader)
	require.NoError(p.h.t, err)
	request.Header.Set("Authorization", "Bearer "+p.credential)
	request.Header.Set(access.HeaderProof, p.proof(method, path))
	response, err := p.h.server.Client().Do(request)
	require.NoError(p.h.t, err)
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(response.Body)
	require.NoError(p.h.t, err)
	a := answered{status: response.StatusCode, header: response.Header, raw: string(read)}
	var refusal access.ErrorBody
	if json.Unmarshal(read, &refusal) == nil {
		a.code, a.path = refusal.Error, refusal.Path
	}
	return a
}

func (p *process) acquire() answered { return p.call(http.MethodPut, mainPath, nil) }

func (p *process) write(files ...access.File) answered {
	return p.call(http.MethodPut, mainPath+access.FilesSuffix, access.Files{Files: files})
}

// running acquires main and requires it running.
func (p *process) running() {
	p.h.t.Helper()
	a := p.acquire()
	require.Equal(p.h.t, http.StatusOK, a.status, a.raw)
}

// file reads what the Bot's workspace main holds at path, past interface 7.
func (h *harness) file(path string) (string, bool) {
	h.t.Helper()
	all, err := h.provider.List(context.Background())
	require.NoError(h.t, err)
	require.Len(h.t, all, 1)
	content, err := h.provider.ReadFile(context.Background(), all[0].Name, path)
	if err != nil {
		return "", false
	}
	return string(content), true
}
