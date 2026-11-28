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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
)

// apiServer stands in for the API server: its ServiceAccount issuer's key and the Pods it holds.
type apiServer struct {
	key *ecdsa.PrivateKey

	mu   sync.Mutex
	pods map[string]workspaceaccess.Pod // by name
	read int
}

func (a *apiServer) Keys(context.Context) (workspaceaccess.Issuer, error) {
	public := jose.JSONWebKey{Key: &a.key.PublicKey, KeyID: "sa-key", Algorithm: string(jose.ES256), Use: "sig"}
	return workspaceaccess.Issuer{URL: issuerURL, Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{public}}}, nil
}

func (a *apiServer) Pod(_ context.Context, namespace, name string) (workspaceaccess.Pod, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.read++
	pod, ok := a.pods[name]
	return pod, ok && namespace == watched, nil
}

func (a *apiServer) setPod(name string, pod workspaceaccess.Pod) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pods[name] = pod
}

func (a *apiServer) endPod(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pods, name)
}

// namespaces stands in for the watched namespaces: their claims, Bots and WorkspaceClasses.
type namespaces struct {
	mu   sync.Mutex
	bots map[workspaceaccess.Bot]workspaceaccess.Declared
}

func (n *namespaces) Served(_ context.Context, namespace string) (bool, error) {
	return namespace == watched, nil
}

func (n *namespaces) Bot(_ context.Context, b workspaceaccess.Bot) (workspaceaccess.Declared, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	d, ok := n.bots[b]
	return d, ok, nil
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
}

type options struct {
	maxDials     int
	bindingFresh time.Duration
}

func newHarness(t *testing.T, o options) *harness {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	h := &harness{
		t:          t,
		api:        &apiServer{key: key, pods: map[string]workspaceaccess.Pod{}},
		namespaces: &namespaces{bots: map[workspaceaccess.Bot]workspaceaccess.Declared{}},
		provider: &provider{
			Provider: workspacefake.New(workspace.Capabilities{Labels: true}), failWith: map[string]error{},
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
		Keys: h.api, Pods: h.api, Bots: h.namespaces,
		Places: map[string]workspaceaccess.Place{podProvider: {
			Provider: h.provider, ModelURL: "https://gateway.invalid/v1", ToolProxyURL: "https://tools.invalid",
		}},
		Images: map[string]string{harnessName: image}, Issuer: h.issuer,
		MaxDials: o.maxDials, BindingFresh: o.bindingFresh, Recheck: 20 * time.Millisecond,
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

// bot places mira, with the attachment main and the tool github.
func (h *harness) bot() workspaceaccess.Declared {
	declared := workspaceaccess.Declared{
		UID: "bot-" + rand.Text(), Harness: harnessName, Tools: []string{"github"},
		Attachments: map[string]workspaceaccess.Attachment{"main": {Class: "standard"}},
	}
	h.namespaces.mu.Lock()
	h.namespaces.bots[workspaceaccess.Bot{Namespace: watched, Name: mira}] = declared
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

// instance starts an instance of declared's Bot.
func (h *harness) instance(declared workspaceaccess.Declared) *process {
	h.t.Helper()
	name := "mira-" + strings.ToLower(rand.Text()[:6])
	pod := workspaceaccess.Pod{UID: "pod-" + rand.Text(), ServiceAccount: mira, BotUID: declared.UID, Container: "containerd://" + rand.Text()}
	h.api.setPod(name, pod)
	p := &process{h: h, podName: name, pod: pod, key: h.key()}
	p.credential = h.credential(name, pod.UID)
	return p
}

func (h *harness) key() *ecdsa.PrivateKey {
	h.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(h.t, err)
	return key
}

// credential signs a projected ServiceAccount token bound to the Pod, as the API server does.
func (h *harness) credential(podName, podUID string) string {
	h.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: h.api.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "sa-key"))
	require.NoError(h.t, err)
	now := time.Now()
	claims := jwt.Claims{
		Issuer: issuerURL, Subject: "system:serviceaccount:" + watched + ":" + mira,
		Audience: jwt.Audience{workspaceaccess.DefaultAudience},
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}
	bound := map[string]any{"kubernetes.io": map[string]any{
		"namespace": watched, "pod": map[string]string{"name": podName, "uid": podUID},
		"serviceaccount": map[string]string{"name": mira, "uid": "sa-uid"},
	}}
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
