package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// Config is what a Gateway serves under.
type Config struct {
	// BasePath is the path of the gateway's base URL, below which both surfaces are (T-2). Empty is
	// the root.
	BasePath string
	// Upstream is the provider's API the model surface is forwarded to, and Key the organization's own
	// key for it, read for each call (KEY-1, KEY-2).
	Upstream *url.URL
	Key      ProviderKey
	// Transport carries the forwarded calls. Nil is a transport bounded by UpstreamHeaderTimeout.
	Transport http.RoundTripper
	// SigningKeys holds the key model tokens are signed with (TOKEN-2).
	SigningKeys SigningKeys
	// Keys reads the API server's keys an issuer's credential is verified against, and Issuer is the
	// one ServiceAccount, as <namespace>/<name>, the gateway issues model tokens to (ISS-1).
	Keys   Keys
	Issuer string
	// Record receives each forwarded call's usage (USE-1). It is called once per call, as its answer
	// ends, and must not block.
	Record func(Usage)
	// Now is the gateway's clock. Nil is time.Now.
	Now func() time.Time
	// TokenLife is a model token's life. Zero is gateway.MaxTokenLife.
	TokenLife time.Duration
	// Limits are each bot's on the model surface and each issuer's on the issue surface.
	Limits Limits
	// BodyTimeout bounds the read of a model call's body, and StallTimeout how long an answer may pass
	// no byte before the call is broken off. Zero is 2 and 5 minutes.
	BodyTimeout, StallTimeout time.Duration
	// Log receives the gateway's own failures, which name no key, token or content. Nil discards them.
	Log *slog.Logger
}

// Gateway serves both surfaces of interface 5.
type Gateway struct {
	cfg         Config
	tokens      *tokens
	credentials *credentials
	proxy       *httputil.ReverseProxy
	bots        *limiter
	issuers     *limiter
	calls       *inFlight
	log         *slog.Logger
}

// maxTokenRequest bounds the issue surface's body, which names three members of at most 253 bytes.
const maxTokenRequest = 4 << 10

// New loads the signing key and returns the Gateway under cfg. It refuses a Config with no upstream or
// no key, so a gateway with no key of the organization's serves no model call (KEY-2).
func New(ctx context.Context, cfg Config) (*Gateway, error) {
	switch {
	case cfg.Upstream == nil || cfg.Upstream.Scheme != "https" && cfg.Upstream.Scheme != "http" || cfg.Upstream.Host == "":
		return nil, errors.New("the upstream is no http or https URL")
	case cfg.Key == nil || cfg.Key.Current() == "":
		return nil, errors.New("the provider's key is empty")
	case cfg.SigningKeys == nil || cfg.Keys == nil || cfg.Record == nil:
		return nil, errors.New("the gateway needs its signing keys, the API server's keys and a usage recorder")
	case !validIssuer(cfg.Issuer):
		return nil, errors.New("the issuer is no <namespace>/<name> of a ServiceAccount")
	}
	cfg.BasePath = strings.TrimRight(cfg.BasePath, "/")
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TokenLife <= 0 || cfg.TokenLife > gateway.MaxTokenLife {
		cfg.TokenLife = gateway.MaxTokenLife
	}
	if cfg.BodyTimeout <= 0 {
		cfg.BodyTimeout = 2 * time.Minute
	}
	if cfg.StallTimeout <= 0 {
		cfg.StallTimeout = 5 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Transport == nil {
		cfg.Transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: gateway.UpstreamHeaderTimeout,
			ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second,
			// The answer's body passes on as the upstream encoded it.
			DisableCompression: true,
		}
	}
	key, err := cfg.SigningKeys.Load(ctx)
	if err != nil {
		return nil, err
	}
	signed, err := newTokens(key, cfg.Now)
	if err != nil {
		return nil, err
	}
	g := &Gateway{
		cfg: cfg, tokens: signed, credentials: &credentials{keys: cfg.Keys, now: cfg.Now},
		bots: newLimiter(cfg.Limits.Bot, cfg.Now), issuers: newLimiter(cfg.Limits.Issuer, cfg.Now),
		calls: newInFlight(cfg.Limits.BotCalls, cfg.Limits.Calls), log: cfg.Log,
	}
	g.proxy = &httputil.ReverseProxy{
		Rewrite:        g.rewrite,
		Transport:      cfg.Transport,
		FlushInterval:  -1,
		ModifyResponse: g.answered,
		ErrorHandler:   g.failed,
		ErrorLog:       slog.NewLogLogger(cfg.Log.Handler(), slog.LevelWarn),
	}
	return g, nil
}

func validIssuer(issuer string) bool {
	namespace, name, ok := strings.Cut(issuer, "/")
	return ok && namespace != "" && name != "" && !strings.ContainsAny(issuer, ": ") && !strings.Contains(name, "/")
}

// ServeHTTP serves the issue surface below PathPrefix and the model surface at its paths, both below
// BasePath. Paths are compared as the request carries them (T-2).
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	answer := &started{ResponseWriter: w}
	defer func() {
		v := recover()
		switch {
		case v == nil:
		case v == http.ErrAbortHandler:
			// An answer already begun is broken off, never added to (MOD-6).
			panic(http.ErrAbortHandler)
		case answer.begun:
			g.log.Error("the gateway failed while it answered", "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			panic(http.ErrAbortHandler)
		default:
			g.log.Error("the gateway failed", "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			writeProvider(w, http.StatusInternalServerError, gateway.ErrorAPI, "the gateway failed")
		}
	}()
	path, below := strings.CutPrefix(r.URL.EscapedPath(), g.cfg.BasePath)
	if !below {
		path = ""
	}
	if strings.HasPrefix(path, gateway.PathPrefix) {
		g.issue(answer, r, path)
		return
	}
	g.model(answer, r, path)
}

// started notes whether an answer has begun, so a failure after it is not answered into it.
type started struct {
	http.ResponseWriter
	begun bool
}

func (s *started) WriteHeader(status int) {
	s.begun = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *started) Write(b []byte) (int, error) {
	s.begun = true
	return s.ResponseWriter.Write(b)
}

func (s *started) Flush() {
	s.begun = true
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *started) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (g *Gateway) issue(w http.ResponseWriter, r *http.Request, path string) {
	// The order of refusals is ISS-3's.
	if path != gateway.TokensPath {
		writeError(w, http.StatusNotFound, gateway.CodeNotFound, "the issue surface serves no such call")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, gateway.CodeInvalid, "POST alone")
		return
	}
	issuer, err := g.credentials.verify(r.Context(), gateway.BearerToken(r.Header.Get(gateway.HeaderAuthorization)))
	switch {
	case errors.Is(err, errCredential):
		writeError(w, http.StatusUnauthorized, gateway.CodeInvalidCredential, "the credential is missing, invalid or expired")
		return
	case err != nil:
		w.Header().Set(gateway.HeaderRetryAfter, strconv.Itoa(int(keysRetry/time.Second)))
		writeError(w, http.StatusServiceUnavailable, gateway.CodeTemporarilyUnavailable, "the API server's keys could not be read")
		return
	}
	if issuer != g.cfg.Issuer {
		writeError(w, http.StatusForbidden, gateway.CodeForbidden, "the credential names no issuer the gateway serves")
		return
	}
	if wait, ok := g.issuers.allow(issuer); !ok {
		w.Header().Set(gateway.HeaderRetryAfter, strconv.Itoa(wait))
		writeError(w, http.StatusTooManyRequests, gateway.CodeTemporarilyUnavailable, "the issuer's limit is reached")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTokenRequest+1))
	if err != nil || len(body) > maxTokenRequest {
		writeError(w, http.StatusBadRequest, gateway.CodeInvalid, "the body is no TokenRequest")
		return
	}
	request, err := gateway.ParseTokenRequest(body)
	if err == nil {
		err = request.Check()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, gateway.CodeInvalid, err.Error())
		return
	}
	token, err := g.tokens.issue(request, g.cfg.TokenLife)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, gateway.CodeTemporarilyUnavailable, "the token could not be signed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(gateway.ModelToken{Token: token, ExpiresIn: int64(g.cfg.TokenLife / time.Second)})
}

func (g *Gateway) model(w http.ResponseWriter, r *http.Request, path string) {
	// The order of refusals is MOD-7's.
	bot, err := g.tokens.verify(gateway.BearerToken(r.Header.Get(gateway.HeaderAuthorization)))
	if err != nil {
		writeProvider(w, http.StatusUnauthorized, gateway.ErrorAuthentication, "the model token is missing, invalid or expired")
		return
	}
	if r.Method != http.MethodPost || !slices.Contains(gateway.ModelPaths, path) {
		writeProvider(w, http.StatusNotFound, gateway.ErrorNotFound, "the gateway serves no such call")
		return
	}
	if wait, ok := g.bots.allow(bot.UID); !ok {
		w.Header().Set(gateway.HeaderRetryAfter, strconv.Itoa(wait))
		writeProvider(w, http.StatusTooManyRequests, gateway.ErrorRateLimit, "the bot's limit is reached")
		return
	}
	// Calls held at once are bounded too, per bot and in all, as each holds its body in memory: a
	// bot past its own is refused as past its limit, and the gateway past its own is unavailable.
	release, refused := g.calls.take(bot.UID)
	switch refused {
	case refusedBot:
		w.Header().Set(gateway.HeaderRetryAfter, "1")
		writeProvider(w, http.StatusTooManyRequests, gateway.ErrorRateLimit, "the bot holds as many calls as it may")
		return
	case refusedGateway:
		w.Header().Set(gateway.HeaderRetryAfter, "1")
		writeProvider(w, http.StatusServiceUnavailable, gateway.ErrorAPI, "the gateway holds as many calls as it may")
		return
	}
	defer release()
	if r.ContentLength > gateway.MaxRequestBody {
		writeProvider(w, http.StatusRequestEntityTooLarge, gateway.ErrorRequestTooLarge, "the body is above 32 MiB")
		return
	}
	// The body is read within BodyTimeout, so a body sent a byte at a time holds no call for long.
	control := http.NewResponseController(w)
	_ = control.SetReadDeadline(time.Now().Add(g.cfg.BodyTimeout))
	body, err := io.ReadAll(io.LimitReader(r.Body, gateway.MaxRequestBody+1))
	_ = control.SetReadDeadline(time.Time{})
	if err != nil {
		writeProvider(w, http.StatusBadRequest, errorInvalidRequest, "the body could not be read")
		return
	}
	if len(body) > gateway.MaxRequestBody {
		writeProvider(w, http.StatusRequestEntityTooLarge, gateway.ErrorRequestTooLarge, "the body is above 32 MiB")
		return
	}
	var named struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &named)
	key := g.cfg.Key.Current()
	if key == "" {
		// A ProviderKey answers none only where it is broken; the call is forwarded nowhere (KEY-2).
		writeProvider(w, http.StatusServiceUnavailable, gateway.ErrorAPI, "the gateway holds no provider key")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	c := &call{bot: bot, model: boundedModel(named.Model), key: key, cancel: cancel}
	g.proxy.ServeHTTP(w, r.WithContext(withCall(ctx, c)))
}

var (
	errRedirect = errors.New("the upstream answered a redirect")
	errStalled  = errors.New("the upstream's answer passed no byte for too long")
)

// errorInvalidRequest is the provider API's error type for a call it cannot read.
const errorInvalidRequest = "invalid_request_error"

// rewrite forwards a call to the upstream at its path below the upstream's, with none of its headers
// but ForwardedHeaders, and the provider's key, so no credential of the workspace's reaches the
// provider (MOD-2).
func (g *Gateway) rewrite(p *httputil.ProxyRequest) {
	path, _ := strings.CutPrefix(p.In.URL.Path, g.cfg.BasePath)
	p.SetURL(g.cfg.Upstream)
	p.Out.URL.Path, p.Out.URL.RawPath = strings.TrimRight(g.cfg.Upstream.Path, "/")+path, ""
	p.Out.Host = g.cfg.Upstream.Host
	header := http.Header{}
	for _, name := range gateway.ForwardedHeaders {
		if values := p.In.Header.Values(name); len(values) > 0 {
			header[http.CanonicalHeaderKey(name)] = slices.Clone(values)
		}
	}
	header.Set(gateway.HeaderProviderKey, callOf(p.In.Context()).key)
	p.Out.Header = header
}

// answered refuses a redirect, drops the upstream's cookies, breaks the call off where its answer
// stalls, and records the call's usage as its body passes (MOD-2, MOD-5, USE-1).
func (g *Gateway) answered(response *http.Response) error {
	c := callOf(response.Request.Context())
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		// A redirect is neither followed nor passed on (MOD-5). failed records it.
		c.status = response.StatusCode
		return errRedirect
	}
	response.Header.Del("Set-Cookie")
	status := response.StatusCode
	body := newStalling(response.Body, g.cfg.StallTimeout, func() { c.cancel(errStalled) })
	response.Body = &metered{body: body, stream: isStream(response), done: func(n counted) {
		g.cfg.Record(Usage{
			Bot: c.bot, Model: c.model, Status: status, InputTokens: n.input, OutputTokens: n.output,
			CacheCreationInputTokens: n.cacheCreation, CacheReadInputTokens: n.cacheRead,
		})
	}}
	return nil
}

// failed answers a call the upstream did not answer, or answered with a redirect, 502, logs why, and
// records it: USE-1 records every call the gateway forwards. The error names the upstream's URL and
// no header, so it carries no key.
func (g *Gateway) failed(w http.ResponseWriter, r *http.Request, err error) {
	c := callOf(r.Context())
	status := http.StatusBadGateway
	if errors.Is(err, errRedirect) {
		status = c.status
		g.log.Warn("the upstream answered a redirect", "namespace", c.bot.Namespace, "bot", c.bot.Name, "status", status)
	} else {
		g.log.Warn("the upstream could not be reached",
			"namespace", c.bot.Namespace, "bot", c.bot.Name, "path", r.URL.Path, "error", err)
	}
	g.cfg.Record(Usage{Bot: c.bot, Model: c.model, Status: status})
	writeProvider(w, http.StatusBadGateway, gateway.ErrorAPI, "the upstream could not be reached")
}

func writeError(w http.ResponseWriter, status int, code gateway.Code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(gateway.ErrorBody{Error: code, Description: description})
}

func writeProvider(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(gateway.NewProviderError(kind, message))
}
