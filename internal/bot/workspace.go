package bot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// ModelTokenPath is the workspace's model token file (interface 2's MODEL-1).
const ModelTokenPath = access.TokenDir + "model/token"

// ToolTokenPath is the workspace's token file of the tool named tool (interface 2's TOOL-1).
func ToolTokenPath(tool string) string { return access.TokenDir + "tools/" + tool + "/token" }

// renewEvery bounds how long the token files the process wrote stand before it writes them again, so
// none is more than an hour old (interface 2's CRED-1, ADR 0043 decision 7).
const renewEvery = 40 * time.Minute

// WorkspaceConfig is what a Workspace reaches its connector with.
type WorkspaceConfig struct {
	// URL is the connector's base URL, and TrustFile the certificates it is trusted by, read again as
	// they change (T-1).
	URL       string
	TrustFile string
	// CredentialFile holds the place's credential of interface 7's audience, read again for each
	// request (CRED-1).
	CredentialFile string
	// Attachment is the Bot's attachment whose workspace is driven.
	Attachment string
	// Key is the process's key, which every proof is signed with (KEY-1).
	Key *ecdsa.PrivateKey
	Now func() time.Time
	// Sleep waits d, or until ctx ends, and answers ctx's error then. Time's own where nil.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Workspace drives the Bot's workspace at its place's connector through interface 7: it acquires it
// until it runs, writes the adapter's and the session's tokens into it, writes them again before they
// age past CRED-1 and after every acquire, and dials the adapter. No token, credential or proof reaches
// a log line, an error or the status.
type Workspace struct {
	cfg   WorkspaceConfig
	base  string
	trust placeTrust

	// reacquire wakes Run to acquire again, a call having found the workspace not running.
	reacquire chan struct{}

	// authMu guards authWait, the wait before the next request while the last was answered 401
	// (AUTH-2), and zero once one was not.
	authMu   sync.Mutex
	authWait time.Duration

	mu        sync.Mutex
	client    *http.Client
	tlsConfig *tls.Config
	adapter   string
	answer    access.Workspace
	// ready is set once the session's tokens are written after the latest acquire.
	ready  bool
	status WorkspaceStatus
}

// WorkspaceStatus is what the status file says of the workspace: its phase and reason as the connector
// last answered, whether its session waits for the place's gateway, when its tokens were last written,
// and the code of the latest refusal, never its content.
type WorkspaceStatus struct {
	Attachment        string          `json:"attachment"`
	Phase             workspace.Phase `json:"phase,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	WaitingForGateway bool            `json:"waitingForGateway,omitempty"`
	TokensWrittenAt   time.Time       `json:"tokensWrittenAt,omitzero"`
	Refusal           string          `json:"refusal,omitempty"`
}

// NewWorkspace returns a Workspace reaching the connector under cfg.
func NewWorkspace(cfg WorkspaceConfig) (*Workspace, error) {
	if cfg.TrustFile == "" || cfg.CredentialFile == "" || cfg.Key == nil || cfg.Attachment == "" {
		return nil, errors.New("a workspace needs the connector's trust file, a credential file, the key and an attachment")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return &Workspace{
		cfg: cfg, base: strings.TrimRight(cfg.URL, "/") + access.WorkspacesPath + url.PathEscape(cfg.Attachment),
		trust: placeTrust{file: cfg.TrustFile}, reacquire: make(chan struct{}, 1),
		status: WorkspaceStatus{Attachment: cfg.Attachment},
	}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Status answers the workspace's status.
func (w *Workspace) Status() WorkspaceStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// Session answers the model gateway and the tools the workspace reaches, as acquire last answered them,
// and whether the workspace runs with the session's tokens written, so a session can be opened on it.
func (w *Workspace) Session() (access.Workspace, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.answer, w.ready
}

// AdapterToken answers the adapter's token last written into the workspace (interface 2's AUTH-1), and
// empty before the first.
func (w *Workspace) AdapterToken() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.adapter
}

// Run acquires the workspace and keeps its tokens written until ctx ends. A failure leaves it running:
// report receives it, and it is retried after a back-off from 1 second, doubling to 60, or after the
// Retry-After the connector answers (REN-1). A 401 waits as AUTH-2 says, before the next request.
func (w *Workspace) Run(ctx context.Context, report func(error)) {
	retry := backoff{}
	acquired := false
	for ctx.Err() == nil {
		renewIn, err := w.pass(ctx, &acquired)
		var refused *refusal7
		isRefusal := errors.As(err, &refused)
		switch {
		case err == nil:
			retry.reset()
			if w.wait(ctx, renewIn) {
				acquired = false
			}
			continue
		case ctx.Err() != nil:
			return
		case isRefusal && (refused.Code == access.CodeNotRunning || refused.Code == access.CodeNotFound):
			acquired = false
		case isRefusal && refused.Code == access.CodeWaitingForGateway:
			// The adapter's token is written, and the session waits for the place's issuer.
		default:
			report(err)
		}
		if isRefusal && refused.Status == http.StatusUnauthorized {
			continue // AUTH-2 waits before the next request.
		}
		if w.cfg.Sleep(ctx, retry.next(err)) != nil {
			return
		}
	}
}

// pass acquires the workspace where it is not acquired, and writes a fresh adapter token after every
// acquire, since one that started or recovered the workspace left no token in it (WS-1). It then
// writes the session's tokens, and answers how long they stand.
func (w *Workspace) pass(ctx context.Context, acquired *bool) (time.Duration, error) {
	if !*acquired {
		// A call that found the workspace not running before this acquire asks for nothing more.
		select {
		case <-w.reacquire:
		default:
		}
		w.setReady(false)
		if err := w.acquire(ctx); err != nil {
			return 0, err
		}
		adapter, files, _, err := tokenFiles(nil)
		if err != nil {
			return 0, err
		}
		if err := w.write(ctx, adapter, files); err != nil {
			return 0, err
		}
		*acquired = true
	}
	stand, err := w.writeTokens(ctx)
	if err == nil {
		w.setReady(true)
	}
	return stand, err
}

func (w *Workspace) setReady(ready bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready = ready
}

// wait waits until the tokens are due again, and answers true where a call found the workspace not
// running first.
func (w *Workspace) wait(ctx context.Context, d time.Duration) bool {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	woken := make(chan bool, 1)
	go func() {
		select {
		case <-w.reacquire:
			woken <- true
			cancel()
		case <-waitCtx.Done():
			woken <- false
		}
	}()
	_ = w.cfg.Sleep(waitCtx, d)
	cancel()
	return <-woken
}

// backoff is a failed pass's wait: from 1 second, doubling to 60, or the Retry-After the connector
// answered, at least that long and at most 60 seconds (REN-1).
type backoff struct{ wait time.Duration }

func (b *backoff) next(err error) time.Duration {
	if b.wait == 0 {
		b.wait = firstWait
	}
	next := b.wait
	b.wait = min(b.wait*2, lastWait)
	var refused *refusal7
	if errors.As(err, &refused) && refused.retryAfter > 0 {
		next = min(max(next, refused.retryAfter), lastWait)
	}
	return next
}

func (b *backoff) reset() { b.wait = 0 }

// acquire acquires the workspace until it runs (WS-1), reading it again no sooner than the
// Retry-After a pending answer carries, or after a back-off from 1 second doubling to 60 where it
// carries none (WS-3). A stopped or failed workspace is acquired again, which starts or recovers it.
func (w *Workspace) acquire(ctx context.Context) error {
	method := http.MethodPut
	pending := backoff{}
	for {
		answer, header, err := w.call(ctx, method, "", nil)
		if err != nil {
			return err
		}
		var ws access.Workspace
		if err := json.Unmarshal(answer, &ws); err != nil {
			return errors.New("the connector answered no workspace")
		}
		phase := knownPhase(ws.Phase)
		w.mu.Lock()
		w.status.Phase, w.status.Reason = phase, ws.Reason
		w.answer = ws
		w.mu.Unlock()
		var wait time.Duration
		switch phase {
		case workspace.PhaseRunning:
			return nil
		case workspace.PhasePending:
			method = http.MethodGet
			wait = pendingWait(header, &pending)
		default:
			method = http.MethodPut
			wait = pending.next(nil)
		}
		if err := w.cfg.Sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// pendingWait is how long a pending workspace is waited before it is read again: its Retry-After, or
// the next of a back-off where it carries none (WS-3).
func pendingWait(header http.Header, pending *backoff) time.Duration {
	if seconds, err := strconv.Atoi(header.Get(access.HeaderRetryAfter)); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return pending.next(nil)
}

// knownPhase reads a phase the client does not know as unknown (VER-2).
func knownPhase(phase workspace.Phase) workspace.Phase {
	switch phase {
	case workspace.PhasePending, workspace.PhaseRunning, workspace.PhaseStopped, workspace.PhaseFailed:
		return phase
	default:
		return workspace.PhaseUnknown
	}
}

// writeTokens obtains the session's tokens and writes them with a fresh adapter token in one write
// (TOK-2). It answers how long they stand before they are written again: 40 minutes, or two thirds of
// the shortest token's life where that is sooner.
func (w *Workspace) writeTokens(ctx context.Context) (time.Duration, error) {
	answer, _, err := w.call(ctx, http.MethodPost, access.TokensSuffix, nil)
	var refused *refusal7
	waiting := errors.As(err, &refused) && refused.Code == access.CodeWaitingForGateway
	w.mu.Lock()
	w.status.WaitingForGateway = waiting
	w.mu.Unlock()
	if err != nil {
		return 0, err
	}
	var tokens access.SessionTokens
	if err := json.Unmarshal(answer, &tokens); err != nil {
		return 0, errors.New("the connector answered no session tokens")
	}
	adapter, files, stand, err := tokenFiles(&tokens)
	if err != nil {
		return 0, err
	}
	if err := w.write(ctx, adapter, files); err != nil {
		return 0, err
	}
	return stand, nil
}

// tokenFiles are one write of the workspace's tokens: a fresh adapter token, at least 128 random bits
// (AUTH-1), first, then the session's tokens where given, and how long they stand before they are
// written again (TOK-2). A tool whose name gives no token file under TokenDir is refused.
func tokenFiles(tokens *access.SessionTokens) (string, []access.File, time.Duration, error) {
	adapter := rand.Text() + rand.Text()
	files := []access.File{{Path: agent.TokenPath, Content: []byte(adapter)}}
	if tokens == nil {
		return adapter, files, 0, nil
	}
	if tokens.Model.Token == "" || tokens.Model.ExpiresIn <= 0 {
		return "", nil, 0, errors.New("the connector answered no model token")
	}
	files = append(files, access.File{Path: ModelTokenPath, Content: []byte(tokens.Model.Token)})
	shortest := tokens.Model.ExpiresIn
	tools := make([]string, 0, len(tokens.Tools))
	for name := range tokens.Tools {
		tools = append(tools, name)
	}
	slices.Sort(tools)
	for _, name := range tools {
		token := tokens.Tools[name]
		path := ToolTokenPath(name)
		if strings.Contains(name, "/") || access.CheckTokenPath(path) != nil || token.Token == "" || token.ExpiresIn <= 0 {
			return "", nil, 0, fmt.Errorf("the connector answered a token for a tool %q no token file is written for", name)
		}
		files = append(files, access.File{Path: path, Content: []byte(token.Token)})
		shortest = min(shortest, token.ExpiresIn)
	}
	return adapter, files, renewal(shortest), nil
}

// renewal is how long tokens whose shortest lives seconds stand before they are written again: two
// thirds of that life, and 40 minutes at most (TOK-2).
func renewal(seconds int64) time.Duration {
	return min(renewEvery, time.Duration(seconds)*time.Second*2/3)
}

// write writes files, the adapter token first, in one write (FILE-1), and holds the adapter token once
// it is written. Files beyond the adapter's are the session's tokens.
func (w *Workspace) write(ctx context.Context, adapter string, files []access.File) error {
	body, err := json.Marshal(access.Files{Files: files})
	if err != nil {
		return err
	}
	if _, _, err := w.call(ctx, http.MethodPut, access.FilesSuffix, body); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.adapter = adapter
	if len(files) > 1 {
		w.status.TokensWrittenAt = w.cfg.Now()
	}
	return nil
}

// Dial opens one connection to the adapter's port through the connector (DIAL-1). A workspace that is
// not running wakes Run to acquire it again. Dialling again after a broken connection is the caller's
// (DIAL-2).
func (w *Workspace) Dial(ctx context.Context) (net.Conn, error) {
	_, tlsConfig, err := w.clients()
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(w.base + access.DialSuffix)
	if err != nil {
		return nil, err
	}
	request, err := w.request(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", access.DialProtocol)
	address := target.Host
	if target.Port() == "" {
		address = net.JoinHostPort(target.Hostname(), "443")
	}
	dialer := &tls.Dialer{Config: tlsConfig}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial the connector: %w", err)
	}
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("dial the connector: %w", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("dial the connector: %w", err)
	}
	if response.StatusCode == http.StatusSwitchingProtocols {
		w.answered()
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	defer func() { _ = conn.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	return nil, w.refused("dial", response, body)
}

// bufferedConn reads first what the 101's reader buffered past the answer.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// call makes one request of the workspace's suffix, and answers the body and headers of a 2xx answer,
// or the refusal.
func (w *Workspace) call(ctx context.Context, method, suffix string, body []byte) ([]byte, http.Header, error) {
	client, _, err := w.clients()
	if err != nil {
		return nil, nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := w.request(ctx, method, w.base+suffix, reader)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("ask the connector: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	if err != nil {
		return nil, nil, fmt.Errorf("read the connector's answer: %w", err)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		w.answered()
		return answer, response.Header, nil
	}
	return nil, nil, w.refused(callName(method, suffix), response, answer)
}

func callName(method, suffix string) string {
	switch {
	case suffix == access.FilesSuffix:
		return "files"
	case suffix == access.TokensSuffix:
		return "session tokens"
	case method == http.MethodPut:
		return "acquire"
	default:
		return "read"
	}
}

// request is a request of target carrying the place's credential, read again now, and a proof of it
// signed with the process's key (CRED-1, PROOF-1). It first waits as AUTH-2 says where the last
// request was answered 401.
func (w *Workspace) request(ctx context.Context, method, target string, body io.Reader) (*http.Request, error) {
	w.authMu.Lock()
	wait := w.authWait
	w.authMu.Unlock()
	if wait > 0 {
		if err := w.cfg.Sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	read, err := os.ReadFile(w.cfg.CredentialFile)
	if err != nil {
		// The path error names the file and the failure, never its content.
		return nil, fmt.Errorf("read the place credential: %w", err)
	}
	credential := string(bytes.TrimSpace(read))
	proof, err := w.proof(method, target, credential)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("ask the connector: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set(headerProof, proof)
	return request, nil
}

// proof is a DPoP proof of the request, bound to the credential (PROOF-1).
func (w *Workspace) proof(method, target, credential string) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: w.cfg.Key},
		(&jose.SignerOptions{EmbedJWK: true}).WithType("dpop+jwt"))
	if err != nil {
		return "", fmt.Errorf("sign the proof: %w", err)
	}
	ath := sha256.Sum256([]byte(credential))
	proof, err := jwt.Signed(signer).Claims(map[string]any{
		"htm": method, "htu": target, "iat": w.cfg.Now().Unix(), "jti": rand.Text(),
		"ath": base64.RawURLEncoding.EncodeToString(ath[:]),
	}).Serialize()
	if err != nil {
		return "", fmt.Errorf("sign the proof: %w", err)
	}
	return proof, nil
}

// answered records an answer that is not 401, which ends AUTH-2's back-off.
func (w *Workspace) answered() {
	w.authMu.Lock()
	defer w.authMu.Unlock()
	w.authWait = 0
}

// refused reads a refusal (T-3), steps AUTH-2's back-off on a 401, and wakes Run to acquire again on
// not_running.
func (w *Workspace) refused(call string, response *http.Response, body []byte) error {
	refused := &refusal7{call: call, Status: response.StatusCode}
	var parsed access.ErrorBody
	if json.Unmarshal(body, &parsed) == nil {
		refused.Code = parsed.Error
	}
	if seconds, err := strconv.Atoi(response.Header.Get(access.HeaderRetryAfter)); err == nil && seconds > 0 {
		refused.retryAfter = time.Duration(seconds) * time.Second
	}
	w.authMu.Lock()
	if response.StatusCode == http.StatusUnauthorized {
		w.authWait = min(max(w.authWait*2, firstWait), lastWait)
	} else {
		w.authWait = 0
	}
	w.authMu.Unlock()
	w.mu.Lock()
	w.status.Refusal = string(refused.Code)
	w.mu.Unlock()
	if refused.Code == access.CodeNotRunning {
		select {
		case w.reacquire <- struct{}{}:
		default:
		}
	}
	return refused
}

// refusal7 is interface 7's refusal of one call. Its message names the call, the status and the code,
// and nothing of the request.
type refusal7 struct {
	call       string
	Status     int
	Code       access.Code
	retryAfter time.Duration
}

func (r *refusal7) Error() string {
	return fmt.Sprintf("the connector refused %s: %d %s", r.call, r.Status, r.Code)
}

// clients reads the connector's trust again, and answers the client and the dial's TLS configuration
// trusting it alone. Both offer HTTP/1.1 alone, which the dial's upgrade needs (DIAL-1).
func (w *Workspace) clients() (*http.Client, *tls.Config, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	changed, err := w.trust.read()
	if err != nil {
		return nil, nil, err
	}
	if changed || w.client == nil {
		w.tlsConfig = &tls.Config{RootCAs: w.trust.roots, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
		w.client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: w.tlsConfig.Clone()},
			// A redirect would carry the credential wherever it pointed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return w.client, w.tlsConfig, nil
}
