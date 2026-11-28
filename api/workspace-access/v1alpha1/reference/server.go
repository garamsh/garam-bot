package reference

import (
	"bufio"
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	v1alpha1 "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

const (
	// Audience is the audience the place's credentials name for the server.
	Audience = "workspace-access-reference"
	// issuer is the iss of the place's credentials.
	issuer = "workspace-access-reference"
	// ModelBaseURL is the model gateway every workspace of the reference reaches.
	ModelBaseURL = "https://gateway.reference.invalid/v1"
	// toolProxy is the tool proxy every workspace of the reference reaches, a tool's URL below it.
	toolProxy = "https://tools.reference.invalid/"
	// tokenLife is how long a session token lives (TOK-1).
	tokenLife = time.Hour
)

// server answers interface 7 from its place.
type server struct {
	place   *place
	origin  *url.URL
	keys    jose.JSONWebKeySet
	started time.Time
}

func newServer(p *place, baseURL string, credentialKey *ecdsa.PublicKey, started time.Time) (*server, error) {
	origin, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	return &server{
		place: p, origin: origin, started: started,
		keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: credentialKey, KeyID: "reference", Algorithm: string(jose.ES256)}}},
	}, nil
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	ws := v1alpha1.WorkspacesPath + "{attachment}"
	mux.HandleFunc("PUT "+ws, s.authenticated(s.acquire))
	mux.HandleFunc("GET "+ws, s.authenticated(s.read))
	mux.HandleFunc("PUT "+ws+v1alpha1.FilesSuffix, s.authenticated(s.files))
	mux.HandleFunc("GET "+ws+v1alpha1.DialSuffix, s.authenticated(s.dial))
	mux.HandleFunc("POST "+ws+v1alpha1.TokensSuffix, s.authenticated(s.tokens))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "no such call")
	})
	return recovering(mux)
}

// recovering answers a handler's panic with 503 temporarily_unavailable where nothing is answered yet
// (T-3). net/http would close the connection instead.
func recovering(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &answering{ResponseWriter: w}
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler || rw.answered || rw.hijacked { //nolint:errorlint // ErrAbortHandler is compared, as net/http does
				panic(recovered)
			}
			refuse(rw, http.StatusServiceUnavailable, v1alpha1.CodeTemporarilyUnavailable, "the server failed")
		}()
		next.ServeHTTP(rw, r)
	})
}

// answering records whether a response has begun, or its connection was taken over.
type answering struct {
	http.ResponseWriter
	answered, hijacked bool
}

func (a *answering) WriteHeader(status int) {
	a.answered = true
	a.ResponseWriter.WriteHeader(status)
}

func (a *answering) Write(b []byte) (int, error) {
	a.answered = true
	return a.ResponseWriter.Write(b)
}

func (a *answering) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := a.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("the connection cannot be taken over")
	}
	a.hijacked = true
	return hijacker.Hijack()
}

// caller is who a request was authenticated as.
type caller struct {
	instance string
	bot      *bot
	// attachment is the request's attachment, declared by the bot.
	attachment string
}

// authenticated runs next for a request whose credential, proof and key hold, and whose bot declares
// the attachment it names (CRED-2, LIMIT-2, BOT-1, PROOF-1, PIN-1).
func (s *server) authenticated(next func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credential, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !bearer {
			credential = ""
		}
		id, err := s.verify(credential)
		if err != nil {
			challenge(w, v1alpha1.CodeInvalidCredential, "the place's credential does not hold")
			return
		}
		p := s.place
		p.mu.Lock()
		if !limiter(p.instLimits, id, instanceEvery, instanceBurst).Allow() {
			p.mu.Unlock()
			retry(w, instanceEvery)
			refuse(w, http.StatusTooManyRequests, v1alpha1.CodeTemporarilyUnavailable, "the instance's request limit")
			return
		}
		inst, ok := p.instances[id]
		if !ok || !inst.running {
			p.mu.Unlock()
			challenge(w, v1alpha1.CodeInvalidCredential, "the credential's instance does not run")
			return
		}
		b := p.bots[inst.bot]
		if b != nil && b.uid != inst.botUID {
			// The instance is of the bot's earlier incarnation, which is gone.
			b = nil
		}
		instanceKey := inst.key()
		p.mu.Unlock()
		if b == nil {
			refuse(w, http.StatusForbidden, v1alpha1.CodeUnknownBot, "the instance runs no bot the place runs")
			return
		}
		thumbprint, err := s.proofKey(r, credential)
		if err != nil {
			challenge(w, v1alpha1.CodeInvalidDPoPProof, err.Error())
			return
		}
		p.mu.Lock()
		pinned, held := p.pins[instanceKey]
		if !held {
			p.pins[instanceKey] = thumbprint
		}
		p.mu.Unlock()
		if held && pinned != thumbprint {
			challenge(w, v1alpha1.CodeInvalidKey, "another key is pinned to the instance")
			return
		}
		attachment := r.PathValue("attachment")
		if _, declared := b.attachments[attachment]; !declared {
			refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "the bot declares no such attachment")
			return
		}
		next(w, r, caller{instance: instanceKey, bot: b, attachment: attachment})
	}
}

// verify answers the instance a credential names, where it is one the server signed for its audience
// since it started, lives an hour at most, and has not expired.
func (s *server) verify(credential string) (string, error) {
	token, err := jwt.ParseSigned(credential, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return "", err
	}
	var claims jwt.Claims
	if err := token.Claims(s.keys, &claims); err != nil {
		return "", err
	}
	// No leeway: CRED-2 takes no credential past its exp.
	if err := claims.ValidateWithLeeway(jwt.Expected{Issuer: issuer, AnyAudience: jwt.Audience{Audience}, Time: time.Now()}, 0); err != nil {
		return "", err
	}
	switch {
	case claims.Expiry == nil || claims.IssuedAt == nil || claims.Subject == "":
		return "", errors.New("the credential names no instance, or no exp or iat")
	case claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > v1alpha1.MaxCredentialLife:
		return "", errors.New("the credential lives longer than an hour")
	case claims.IssuedAt.Time().Before(s.started):
		return "", errors.New("the credential was issued before the server started")
	}
	return claims.Subject, nil
}

// proofKey verifies the request's one proof for its method, URL and credential, and answers its key's
// thumbprint (PROOF-1).
func (s *server) proofKey(r *http.Request, credential string) (string, error) {
	proofs := r.Header.Values(v1alpha1.HeaderProof)
	if len(proofs) != 1 || len(proofs[0]) > 4096 {
		return "", errors.New("not one proof")
	}
	signed, err := jose.ParseSignedCompact(proofs[0], []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(signed.Signatures) != 1 {
		return "", errors.New("not one ES256 signature")
	}
	header := signed.Signatures[0].Header
	if header.JSONWebKey == nil || !header.JSONWebKey.IsPublic() || header.ExtraHeaders[jose.HeaderType] != "dpop+jwt" {
		return "", errors.New("no public jwk, or not a dpop+jwt")
	}
	payload, err := signed.Verify(header.JSONWebKey)
	if err != nil {
		return "", errors.New("the proof does not verify")
	}
	var claims struct {
		HTM string `json:"htm"`
		HTU string `json:"htu"`
		IAT int64  `json:"iat"`
		JTI string `json:"jti"`
		ATH string `json:"ath"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", errors.New("the proof's claims are not JSON")
	}
	htu, err := url.Parse(claims.HTU)
	if err != nil || htu.Scheme != s.origin.Scheme || htu.Host != s.origin.Host || htu.Path != r.URL.Path || htu.RawQuery != "" {
		return "", errors.New("the proof names another URL")
	}
	digest := sha256.Sum256([]byte(credential))
	iat := time.Unix(claims.IAT, 0)
	p := s.place
	p.mu.Lock()
	now := p.clock()
	p.mu.Unlock()
	switch {
	case claims.HTM != r.Method:
		return "", errors.New("the proof names another method")
	case claims.ATH != base64.RawURLEncoding.EncodeToString(digest[:]):
		return "", errors.New("the proof names another credential")
	case claims.JTI == "" || now.Sub(iat).Abs() > v1alpha1.ProofWindow:
		return "", errors.New("the proof has no jti, or is outside its window")
	}
	sum, err := header.JSONWebKey.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now = p.clock()
	for jti, seen := range p.proofs {
		if now.Sub(seen) > v1alpha1.ProofMemory {
			delete(p.proofs, jti)
		}
	}
	if _, seen := p.proofs[claims.JTI]; seen {
		return "", errors.New("the proof's jti was seen")
	}
	p.proofs[claims.JTI] = now
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

// takeBot takes one call from the bot's limit, and answers 429 where it is spent (LIMIT-1).
func (s *server) takeBot(w http.ResponseWriter, b *bot) bool {
	s.place.mu.Lock()
	allowed := limiter(s.place.botLimits, b.uid, botEvery, botBurst).Allow()
	s.place.mu.Unlock()
	if !allowed {
		retry(w, botEvery)
		refuse(w, http.StatusTooManyRequests, v1alpha1.CodeTemporarilyUnavailable, "the bot's limit")
	}
	return allowed
}

// noBody refuses a request whose body is not empty or {} with 400 invalid, so a member the server does
// not know is never taken in silence (T-2).
func noBody(w http.ResponseWriter, r *http.Request) bool {
	read, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err == nil && len(bytes.TrimSpace(read)) > 0 {
		err = decodeWhole(bytes.NewReader(read), &struct{}{})
	}
	if err != nil {
		refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, "the call takes no body")
		return false
	}
	return true
}

// decodeWhole decodes one JSON value into into, refusing a member it does not name and anything after
// the value (T-2).
func decodeWhole(r io.Reader, into any) error {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("data after the body")
	}
	return nil
}

// acquire creates, starts or recovers the workspace (WS-1).
func (s *server) acquire(w http.ResponseWriter, r *http.Request, c caller) {
	if !noBody(w, r) {
		return
	}
	if reason := c.bot.attachments[c.attachment]; reason != "" {
		refuse(w, http.StatusUnprocessableEntity, v1alpha1.CodeUnsupported, reason)
		return
	}
	if !s.takeBot(w, c.bot) {
		return
	}
	p := s.place
	p.mu.Lock()
	key := spaceKey(c.bot.uid, c.attachment)
	sp, ok := p.spaces[key]
	switch {
	case ok && sp.deleting:
		p.mu.Unlock()
		retry(w, time.Second)
		refuse(w, http.StatusConflict, v1alpha1.CodeDeleting, "the workspace's earlier incarnation is being deleted")
		return
	case !ok:
		sp = &space{phase: workspace.PhaseRunning, files: map[string]entry{}}
		p.spaces[key] = sp
	case sp.phase == workspace.PhaseStopped || sp.phase == workspace.PhaseFailed:
		sp.phase, sp.reason = workspace.PhaseRunning, ""
	}
	answer := s.answer(c, sp)
	p.mu.Unlock()
	s.writeWorkspace(w, answer)
}

// read answers the workspace and takes nothing from the bot's limit (WS-2).
func (s *server) read(w http.ResponseWriter, _ *http.Request, c caller) {
	p := s.place
	p.mu.Lock()
	sp, ok := p.spaces[spaceKey(c.bot.uid, c.attachment)]
	if !ok {
		p.mu.Unlock()
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "the workspace is not acquired")
		return
	}
	answer := s.answer(c, sp)
	p.mu.Unlock()
	s.writeWorkspace(w, answer)
}

// answer is the workspace's state as WS-1 answers it. The caller holds mu.
func (s *server) answer(c caller, sp *space) v1alpha1.Workspace {
	tools := []v1alpha1.Tool{}
	for _, name := range c.bot.sortedTools() {
		tools = append(tools, v1alpha1.Tool{Name: name, URL: toolProxy + name})
	}
	return v1alpha1.Workspace{
		Attachment: c.attachment, Phase: sp.phase, Reason: sp.reason, Model: v1alpha1.Model{BaseURL: ModelBaseURL}, Tools: tools,
	}
}

func (s *server) writeWorkspace(w http.ResponseWriter, answer v1alpha1.Workspace) {
	if answer.Phase == workspace.PhasePending {
		retry(w, time.Second)
	}
	writeJSON(w, http.StatusOK, answer)
}

// files writes the body's files, in order, under the token directory (FILE-1).
func (s *server) files(w http.ResponseWriter, r *http.Request, c caller) {
	// The bound leaves room for base64 and the members around 64 KiB of content.
	var files v1alpha1.Files
	err := decodeWhole(http.MaxBytesReader(w, r.Body, 4*v1alpha1.MaxContentBytes), &files)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		refuse(w, http.StatusRequestEntityTooLarge, v1alpha1.CodeInvalid, "the content is above 64 KiB")
		return
	case err != nil:
		refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, "the body is not Files")
		return
	}
	if len(files.Files) == 0 || len(files.Files) > v1alpha1.MaxFiles {
		refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, fmt.Sprintf("a write carries 1 to %d files", v1alpha1.MaxFiles))
		return
	}
	size := 0
	for _, f := range files.Files {
		if err := v1alpha1.CheckTokenPath(f.Path); err != nil {
			refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, err.Error())
			return
		}
		size += len(f.Content)
	}
	if size > v1alpha1.MaxContentBytes {
		refuse(w, http.StatusRequestEntityTooLarge, v1alpha1.CodeInvalid, "the content is above 64 KiB")
		return
	}
	if !s.takeBot(w, c.bot) {
		return
	}
	p := s.place
	p.mu.Lock()
	defer p.mu.Unlock()
	sp, ok := p.spaces[spaceKey(c.bot.uid, c.attachment)]
	switch {
	case !ok:
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "the workspace is not acquired")
		return
	case sp.phase != workspace.PhaseRunning || sp.deleting:
		refuse(w, http.StatusConflict, v1alpha1.CodeNotRunning, "the workspace is not running")
		return
	}
	for _, f := range files.Files {
		if through := sp.linkAbove(f.Path); through != "" {
			// The files before it stay written.
			writeJSON(w, http.StatusBadRequest, v1alpha1.ErrorBody{
				Error: v1alpha1.CodeInvalid, Description: "the path passes through a link at " + through, Path: f.Path,
			})
			return
		}
		// A link at the path is replaced, never followed.
		sp.files[f.Path] = entry{content: append([]byte(nil), f.Content...)}
	}
	w.WriteHeader(http.StatusNoContent)
}

// linkAbove answers the first directory of path that is a link, or empty where none is (FILE-1).
func (sp *space) linkAbove(path string) string {
	for i := 1; i < len(path); i++ {
		if path[i] == '/' && sp.files[path[:i]].link != "" {
			return path[:i]
		}
	}
	return ""
}

// tokens answers the session's tokens (TOK-1).
func (s *server) tokens(w http.ResponseWriter, r *http.Request, c caller) {
	if !noBody(w, r) {
		return
	}
	p := s.place
	p.mu.Lock()
	issuer := p.issuer
	_, acquired := p.spaces[spaceKey(c.bot.uid, c.attachment)]
	p.mu.Unlock()
	switch {
	case !acquired:
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "the workspace is not acquired")
		return
	case !issuer:
		refuse(w, http.StatusServiceUnavailable, v1alpha1.CodeWaitingForGateway, "the place has no issuer of session tokens")
		return
	}
	if !s.takeBot(w, c.bot) {
		return
	}
	life := int64(tokenLife / time.Second)
	answer := v1alpha1.SessionTokens{Model: v1alpha1.Token{Token: "reference_model_" + newID(), ExpiresIn: life}, Tools: map[string]v1alpha1.Token{}}
	for _, name := range c.bot.sortedTools() {
		answer.Tools[name] = v1alpha1.Token{Token: "reference_tool_" + newID(), ExpiresIn: life}
	}
	writeJSON(w, http.StatusOK, answer)
}

// challenge refuses with 401 and the DPoP challenge (PROOF-1).
func challenge(w http.ResponseWriter, code v1alpha1.Code, description string) {
	w.Header().Set(v1alpha1.HeaderAuthenticate, fmt.Sprintf(`DPoP error=%q, algs="ES256"`, code))
	refuse(w, http.StatusUnauthorized, code, description)
}

func retry(w http.ResponseWriter, after time.Duration) {
	w.Header().Set(v1alpha1.HeaderRetryAfter, strconv.Itoa(max(1, int(after/time.Second))))
}

func refuse(w http.ResponseWriter, status int, code v1alpha1.Code, description string) {
	writeJSON(w, status, v1alpha1.ErrorBody{Error: code, Description: description})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
