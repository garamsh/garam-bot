package bottoken_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/operator/internal/bottoken"
)

const (
	record    = "grn:acme/iam/cluster/c-1"
	issuer    = "https://kubernetes.default.svc"
	actor     = "garam_ct_actor"
	watched   = "game-team"
	otherTeam = "other-team"
	mira      = "mira"
	// The members and codes of interface 6's refusals.
	errorKey     = "error"
	useNonce     = "use_dpop_nonce"
	invalidProof = "invalid_dpop_proof"
	unavailable  = "temporarily_unavailable"
)

// apiServer stands in for the API server's ServiceAccount issuer: it signs credentials with its key.
type apiServer struct {
	key *rsa.PrivateKey
}

func newAPIServer(t *testing.T) *apiServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return &apiServer{key: key}
}

func (a *apiServer) Keys(context.Context) (bottoken.Issuer, error) {
	public := jose.JSONWebKey{Key: &a.key.PublicKey, KeyID: "sa-key", Algorithm: string(jose.RS256), Use: "sig"}
	return bottoken.Issuer{URL: issuer, Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{public}}}, nil
}

// credential is how a credential is signed and what it claims.
type credential struct {
	key      *rsa.PrivateKey
	issuer   string
	subject  string
	audience string
	// issuedAt defaults to now, and expiry to 10 minutes after issuedAt.
	issuedAt time.Time
	expiry   time.Time
	// bound is the kubernetes.io claim, and unbound leaves it out.
	bound   *kubernetesIO
	unbound bool
}

// kubernetesIO is a projected ServiceAccount token's kubernetes.io claim.
type kubernetesIO struct {
	Namespace      string `json:"namespace"`
	Pod            *named `json:"pod,omitempty"`
	ServiceAccount named  `json:"serviceaccount"`
}

type named struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// workloadPod is the name of the one Pod of Bot name's StatefulSet, and podUID its UID.
func workloadPod(name string) string { return name + "-0" }
func podUID(name string) string      { return "pod-uid-" + name }

// credentialFor is the projected ServiceAccount token of the ServiceAccount namespace/name, bound to
// the Pod of the Bot of that name.
func (a *apiServer) credentialFor(t *testing.T, namespace, name string) string {
	t.Helper()
	return a.sign(t, credential{subject: "system:serviceaccount:" + namespace + ":" + name})
}

func (a *apiServer) sign(t *testing.T, c credential) string {
	t.Helper()
	if c.key == nil {
		c.key = a.key
	}
	if c.issuer == "" {
		c.issuer = issuer
	}
	if c.audience == "" {
		c.audience = bottoken.DefaultAudience
	}
	if c.issuedAt.IsZero() {
		c.issuedAt = time.Now()
	}
	if c.expiry.IsZero() {
		c.expiry = c.issuedAt.Add(10 * time.Minute)
	}
	if c.bound == nil && !c.unbound {
		parts := strings.Split(c.subject, ":")
		namespace, name := parts[len(parts)-2], parts[len(parts)-1]
		c.bound = &kubernetesIO{Namespace: namespace, Pod: &named{Name: workloadPod(name), UID: podUID(name)},
			ServiceAccount: named{Name: name, UID: "sa-uid-" + name}}
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: c.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "sa-key"))
	require.NoError(t, err)
	private := map[string]any{}
	if c.bound != nil {
		private["kubernetes.io"] = c.bound
	}
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: c.issuer, Subject: c.subject, Audience: jwt.Audience{c.audience},
		IssuedAt: jwt.NewNumericDate(c.issuedAt), NotBefore: jwt.NewNumericDate(c.issuedAt),
		Expiry: jwt.NewNumericDate(c.expiry), ID: rand.Text(),
	}).Claims(private).Serialize()
	require.NoError(t, err)
	return raw
}

// bots stands in for the declarations the connector holds and the Bots it applied from them.
type bots struct {
	mu      sync.Mutex
	applied map[bottoken.Bot]bottoken.Applied
}

func (b *bots) Applied(bot bottoken.Bot) (bottoken.Applied, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	applied, ok := b.applied[bot]
	return applied, ok
}

func (b *bots) set(bot bottoken.Bot, applied bottoken.Applied, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.applied[bot] = applied
	} else {
		delete(b.applied, bot)
	}
}

// pods stands in for the API server's Pods.
type pods struct {
	mu   sync.Mutex
	held map[[2]string]bottoken.Pod
}

func (p *pods) Pod(_ context.Context, namespace, name string) (bottoken.Pod, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pod, ok := p.held[[2]string{namespace, name}]
	return pod, ok, nil
}

func (p *pods) set(namespace, name string, pod bottoken.Pod, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ok {
		p.held[[2]string{namespace, name}] = pod
	} else {
		delete(p.held, [2]string{namespace, name})
	}
}

// botUID is the UID of the object the connector made for Bot name.
func botUID(name string) string { return "bot-uid-" + name }

// place makes namespace/name a Bot the connector applied from declaration id, run by its Pod.
func (s *server) place(namespace, name, id string) {
	s.bots.set(bottoken.Bot{Namespace: namespace, Name: name},
		bottoken.Applied{DeclarationID: id, UID: botUID(name)}, true)
	s.pods.set(namespace, workloadPod(name),
		bottoken.Pod{UID: podUID(name), ServiceAccount: name, BotUID: botUID(name)}, true)
}

// trust stands in for the trust ConfigMap: every bundle published, in order.
type trust struct {
	mu      sync.Mutex
	bundles [][]byte
	// failing is how many publishes from now fail, and failed how many have.
	failing int
	failed  int
}

func (s *trust) Publish(_ context.Context, bundle []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing > 0 {
		s.failing--
		s.failed++
		return errors.New("the ConfigMap is not writable")
	}
	s.bundles = append(s.bundles, bytes.Clone(bundle))
	return nil
}

func (s *trust) fail(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = n
}

func (s *trust) failures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

func (s *trust) published() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.bundles...)
}

// exchange is one request the stand-in mint read.
type exchange struct {
	form  map[string]string
	proof string
}

// mint stands in for garam's token endpoint: it asks for its nonce, binds each token to the proof's
// key, and answers refuse in place of a token where refuse is set.
type mint struct {
	server *httptest.Server
	nonce  string

	mu        sync.Mutex
	exchanges []exchange
	// bound is each token answered, with the RFC 7638 thumbprint of the key it is bound to.
	bound  map[string]string
	refuse func(w http.ResponseWriter) bool
	delay  time.Duration
}

func newMint(t *testing.T) *mint {
	t.Helper()
	m := &mint{nonce: "nonce-1", bound: map[string]string{}}
	m.server = httptest.NewTLSServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mint) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeAnswer(w, http.StatusBadRequest, map[string]string{errorKey: "invalid_request"})
		return
	}
	m.mu.Lock()
	read := exchange{form: map[string]string{}, proof: r.Header.Get("DPoP")}
	for key := range r.PostForm {
		read.form[key] = r.PostForm.Get(key)
	}
	m.exchanges = append(m.exchanges, read)
	refuse, delay := m.refuse, m.delay
	m.mu.Unlock()
	time.Sleep(delay)
	w.Header().Set("DPoP-Nonce", m.nonce)
	if refuse != nil && refuse(w) {
		return
	}
	key, nonce, ok := readProof(read.proof)
	if !ok {
		writeAnswer(w, http.StatusBadRequest, map[string]string{errorKey: invalidProof})
		return
	}
	if nonce != m.nonce {
		writeAnswer(w, http.StatusBadRequest, map[string]string{errorKey: useNonce})
		return
	}
	token := "garam_bt_" + rand.Text()
	m.mu.Lock()
	m.bound[token] = thumbprint(key)
	m.mu.Unlock()
	writeAnswer(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "DPoP", "expires_in": 3600,
		"issued_token_type": "urn:ietf:params:oauth:token-type:access_token"})
}

func (m *mint) read() []exchange {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]exchange(nil), m.exchanges...)
}

func (m *mint) boundTo(token string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bound[token]
}

func (m *mint) set(refuse func(w http.ResponseWriter) bool, delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refuse, m.delay = refuse, delay
}

func writeAnswer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// readProof verifies a proof by the key its header carries, and answers that key and its nonce.
func readProof(proof string) (*jose.JSONWebKey, string, bool) {
	signed, err := jose.ParseSignedCompact(proof, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(signed.Signatures) != 1 || signed.Signatures[0].Header.JSONWebKey == nil {
		return nil, "", false
	}
	key := signed.Signatures[0].Header.JSONWebKey
	payload, err := signed.Verify(key)
	if err != nil {
		return nil, "", false
	}
	var claims struct {
		Nonce string `json:"nonce"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return nil, "", false
	}
	return key, claims.Nonce, true
}

func thumbprint(key *jose.JSONWebKey) string {
	sum, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(sum)
}

// process stands in for a bot's process: it holds its key and signs proofs with it.
type process struct {
	key *ecdsa.PrivateKey
}

func newProcess(t *testing.T) process {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return process{key: key}
}

func (p process) thumbprint() string {
	return thumbprint(&jose.JSONWebKey{Key: &p.key.PublicKey})
}

// proof is a DPoP proof for htu, carrying nonce where it is not empty.
func (p process) proof(t *testing.T, htu, nonce string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: p.key}, (&jose.SignerOptions{
		EmbedJWK: true,
	}).WithType("dpop+jwt"))
	require.NoError(t, err)
	claims := map[string]any{"htm": "POST", "htu": htu, "iat": time.Now().Unix(), "jti": rand.Text()}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

// server is a Service serving on a port of its own, and what it was built over.
type server struct {
	url   string
	trust *trust
	mint  *mint
	api   *apiServer
	bots  *bots
	pods  *pods
	logs  *lockedWriter
	// invalidated is every cluster token the server dropped.
	invalidated *recorded
	client      *http.Client
}

// options change the Config a test's server is built from.
type options func(*bottoken.Config)

func start(t *testing.T, change ...options) *server {
	t.Helper()
	s := &server{trust: &trust{}, mint: newMint(t), api: newAPIServer(t), logs: &lockedWriter{},
		bots: &bots{applied: map[bottoken.Bot]bottoken.Applied{}}, pods: &pods{held: map[[2]string]bottoken.Pod{}},
		invalidated: &recorded{}}
	s.place(watched, mira, "decl-mira")
	cfg := bottoken.Config{
		Record:     record,
		Namespaces: []string{watched, otherTeam},
		Keys:       s.api,
		Bots:       s.bots,
		Pods:       s.pods,
		Trust:      s.trust,
		HTTP:       s.mint.server.Client(),
		TokenURL:   func() string { return s.mint.server.URL + "/oauth/token" },
		Actor:      func(context.Context) (string, error) { return actor, nil },
		Invalidate: s.invalidated.add,
		DNSNames:   []string{"garam-connector.garam.svc"},
		Log:        slog.New(slog.NewJSONHandler(s.logs, nil)),
	}
	for _, c := range change {
		c(&cfg)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bottoken.NewService(cfg).Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	require.Eventually(t, func() bool { return len(s.trust.published()) > 0 }, 5*time.Second, 10*time.Millisecond)
	s.url = "https://" + listener.Addr().String() + bottoken.Path
	s.client = s.trusting(t, s.trust.published()[0])
	return s
}

// trusting is a client trusting bundle alone, as a bot's process trusts what the place publishes (T-1).
func (s *server) trusting(t *testing.T, bundle []byte) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(bundle))
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, ServerName: "garam-connector.garam.svc", MinVersion: tls.VersionTLS13,
	}}}
}

// answer is what one call answered.
type answer struct {
	status     int
	nonce      string
	retryAfter string
	body       map[string]any
}

func (a answer) code() any { return a.body[errorKey] }

// call makes the one call with credential and, where proof is not empty, proof.
func (s *server) call(t *testing.T, credential, proof string) answer {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, s.url, nil)
	require.NoError(t, err)
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	if proof != "" {
		request.Header.Set("DPoP", proof)
	}
	response, err := s.client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	body := map[string]any{}
	require.NoError(t, json.Unmarshal(read, &body), string(read))
	return answer{status: response.StatusCode, nonce: response.Header.Get("DPoP-Nonce"),
		retryAfter: response.Header.Get("Retry-After"), body: body}
}

// obtain is the calls a process makes for a token: one for the nonce and htu, then one with its proof.
func (s *server) obtain(t *testing.T, credential string, p process) answer {
	t.Helper()
	asked := s.call(t, credential, "")
	if asked.code() != useNonce {
		return asked
	}
	htu, _ := asked.body["htu"].(string)
	nonce := asked.nonce
	if nonce == "" {
		relayed := s.call(t, credential, p.proof(t, htu, ""))
		if relayed.code() != useNonce {
			return relayed
		}
		nonce = relayed.nonce
	}
	return s.call(t, credential, p.proof(t, htu, nonce))
}

// lockedWriter is the server's log, which a test reads while the server writes it.
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// recorded is what a test reads while the server appends to it.
type recorded struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorded) add(v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, v)
}

func (r *recorded) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}
