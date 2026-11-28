// Package gatewayfake is interface 5's reference gateway: it issues and checks its own model tokens,
// forwards the model surface to an upstream it is given with a key it is given, and records each call
// it forwards and each it refuses. It is the stub interface 2's MODEL-1 case runs against.
package gatewayfake

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// Config is what a Gateway serves under.
type Config struct {
	// Upstream is where the model surface is forwarded, and Key the provider's key it sets (KEY-2).
	Upstream *url.URL
	Key      string
	// Transport carries the forwarded calls. Nil is a transport bounded by UpstreamHeaderTimeout.
	Transport http.RoundTripper
	// SigningKey signs the model tokens. A gateway made again with the same key accepts the tokens
	// the earlier one issued (TOKEN-2).
	SigningKey *ecdsa.PrivateKey
	// Credentials checks an issuer's credential, and Issuers are the issuers it serves (ISS-1).
	Credentials *Credentials
	Issuers     []string
	// Now is the gateway's clock. Nil is time.Now.
	Now func() time.Time
	// TokenLife is a model token's life. Zero is v1alpha1.MaxTokenLife.
	TokenLife time.Duration
	// BotBurst and BotEvery are each bot's limit on the model surface, IssuerBurst and IssuerEvery
	// each issuer's on the issue surface (LIMIT-1, LIMIT-2). Zero is 60 calls, one back a second.
	BotBurst, IssuerBurst int
	BotEvery, IssuerEvery time.Duration
}

// Request is one model call the gateway forwarded.
type Request struct {
	// Bot is the bot the call's token names.
	Bot  v1alpha1.TokenRequest
	Path string
}

// Usage is the record of one forwarded call (USE-1).
type Usage struct {
	Bot          v1alpha1.TokenRequest
	Model        string
	Status       int
	InputTokens  int64
	OutputTokens int64
}

// Gateway serves both surfaces of interface 5.
type Gateway struct {
	cfg    Config
	tokens *tokens
	proxy  *httputil.ReverseProxy

	mu       sync.Mutex
	requests []Request
	refused  int
	usage    []Usage
	bots     map[string]*rate.Limiter
	issuers  map[string]*rate.Limiter
}

// New returns the Gateway under cfg.
func New(cfg Config) (*Gateway, error) {
	if cfg.Upstream == nil || cfg.Key == "" || cfg.SigningKey == nil || cfg.Credentials == nil {
		// A gateway with no key serves no model surface (KEY-2).
		return nil, errors.New("a gateway needs its upstream, the provider's key, its signing key and its issuers' credentials")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TokenLife <= 0 || cfg.TokenLife > v1alpha1.MaxTokenLife {
		cfg.TokenLife = v1alpha1.MaxTokenLife
	}
	if cfg.BotBurst <= 0 {
		cfg.BotBurst, cfg.BotEvery = 60, time.Second
	}
	if cfg.IssuerBurst <= 0 {
		cfg.IssuerBurst, cfg.IssuerEvery = 60, time.Second
	}
	if cfg.Transport == nil {
		cfg.Transport = &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: v1alpha1.UpstreamHeaderTimeout}
	}
	g := &Gateway{
		cfg: cfg, tokens: &tokens{key: cfg.SigningKey, now: cfg.Now},
		bots: map[string]*rate.Limiter{}, issuers: map[string]*rate.Limiter{},
	}
	g.proxy = &httputil.ReverseProxy{
		Rewrite:        g.rewrite,
		Transport:      cfg.Transport,
		FlushInterval:  -1,
		ModifyResponse: g.answered,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeProvider(w, http.StatusBadGateway, v1alpha1.ErrorAPI, "the upstream could not be reached")
		},
	}
	return g, nil
}

// Issue answers a model token for bot, as the issue surface does, for a caller in process.
func (g *Gateway) Issue(bot v1alpha1.TokenRequest) (v1alpha1.ModelToken, error) {
	if err := bot.Check(); err != nil {
		return v1alpha1.ModelToken{}, err
	}
	token, err := g.tokens.issue(bot, g.cfg.TokenLife)
	if err != nil {
		return v1alpha1.ModelToken{}, err
	}
	return v1alpha1.ModelToken{Token: token, ExpiresIn: int64(g.cfg.TokenLife / time.Second)}, nil
}

// Requests are the model calls the gateway forwarded, in order.
func (g *Gateway) Requests() []Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.requests)
}

// Refused is how many model calls the gateway refused for their token (MOD-1).
func (g *Gateway) Refused() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refused
}

// Usage are the records of the calls the gateway forwarded (USE-1).
func (g *Gateway) Usage() []Usage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.usage)
}

// ServeHTTP serves the issue surface under PathPrefix and the model surface at its paths.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recover() != nil {
			writeError(w, http.StatusServiceUnavailable, v1alpha1.CodeTemporarilyUnavailable, "the gateway failed")
		}
	}()
	if r.URL.Path == v1alpha1.TokensPath {
		g.issue(w, r)
		return
	}
	g.model(w, r)
}

func (g *Gateway) issue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, v1alpha1.CodeInvalid, "POST alone")
		return
	}
	issuer, err := g.cfg.Credentials.Verify(bearer(r), g.cfg.Now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, v1alpha1.CodeInvalidCredential, err.Error())
		return
	}
	if !slices.Contains(g.cfg.Issuers, issuer) {
		writeError(w, http.StatusForbidden, v1alpha1.CodeForbidden, "the credential names no issuer the gateway serves")
		return
	}
	if !g.allow(g.issuers, issuer, g.cfg.IssuerBurst, g.cfg.IssuerEvery) {
		w.Header().Set(v1alpha1.HeaderRetryAfter, strconv.Itoa(retryAfter(g.cfg.IssuerEvery)))
		writeError(w, http.StatusTooManyRequests, v1alpha1.CodeTemporarilyUnavailable, "the issuer's limit is reached")
		return
	}
	var request v1alpha1.TokenRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, v1alpha1.CodeInvalid, "the body is no TokenRequest")
		return
	}
	if err := request.Check(); err != nil {
		writeError(w, http.StatusBadRequest, v1alpha1.CodeInvalid, err.Error())
		return
	}
	answer, err := g.Issue(request)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, v1alpha1.CodeTemporarilyUnavailable, "the token could not be signed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

func (g *Gateway) model(w http.ResponseWriter, r *http.Request) {
	bot, err := g.tokens.verify(bearer(r))
	if err != nil {
		g.mu.Lock()
		g.refused++
		g.mu.Unlock()
		writeProvider(w, http.StatusUnauthorized, v1alpha1.ErrorAuthentication, "the model token is missing, invalid or expired")
		return
	}
	if r.Method != http.MethodPost || !slices.Contains(v1alpha1.ModelPaths, r.URL.Path) {
		writeProvider(w, http.StatusNotFound, v1alpha1.ErrorNotFound, "the gateway serves no such call")
		return
	}
	if !g.allow(g.bots, bot.UID, g.cfg.BotBurst, g.cfg.BotEvery) {
		w.Header().Set(v1alpha1.HeaderRetryAfter, strconv.Itoa(retryAfter(g.cfg.BotEvery)))
		writeProvider(w, http.StatusTooManyRequests, v1alpha1.ErrorRateLimit, "the bot's limit is reached")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, v1alpha1.MaxRequestBody+1))
	if err != nil {
		writeProvider(w, http.StatusBadRequest, "invalid_request_error", "the body could not be read")
		return
	}
	if len(body) > v1alpha1.MaxRequestBody {
		writeProvider(w, http.StatusRequestEntityTooLarge, v1alpha1.ErrorRequestTooLarge, "the body is above 32 MiB")
		return
	}
	var named struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &named)
	g.mu.Lock()
	g.requests = append(g.requests, Request{Bot: bot, Path: r.URL.Path})
	g.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	ctx := withCall(r.Context(), &call{bot: bot, model: named.Model})
	g.proxy.ServeHTTP(w, r.WithContext(ctx))
}

var errRedirect = errors.New("the upstream answered a redirect")

// rewrite forwards a call with none of its headers but ForwardedHeaders, and the provider's key (MOD-2).
func (g *Gateway) rewrite(p *httputil.ProxyRequest) {
	p.SetURL(g.cfg.Upstream)
	p.Out.URL.Path, p.Out.URL.RawPath = joinPath(g.cfg.Upstream.Path, p.In.URL.Path), ""
	p.Out.Host = g.cfg.Upstream.Host
	header := http.Header{}
	for _, name := range v1alpha1.ForwardedHeaders {
		if values := p.In.Header.Values(name); len(values) > 0 {
			header[http.CanonicalHeaderKey(name)] = slices.Clone(values)
		}
	}
	header.Set(v1alpha1.HeaderProviderKey, g.cfg.Key)
	p.Out.Header = header
}

// answered drops the upstream's cookies, and records the call's usage as its body passes (MOD-2, USE-1).
func (g *Gateway) answered(response *http.Response) error {
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		// A redirect is neither followed nor passed on (MOD-5).
		return errRedirect
	}
	response.Header.Del("Set-Cookie")
	c := callOf(response.Request.Context())
	if c == nil {
		return nil
	}
	record := func(u counted) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.usage = append(g.usage, Usage{
			Bot: c.bot, Model: c.model, Status: response.StatusCode,
			InputTokens: u.input, OutputTokens: u.output,
		})
	}
	response.Body = &metered{body: response.Body, stream: isStream(response), done: record}
	return nil
}

func (g *Gateway) allow(limiters map[string]*rate.Limiter, key string, burst int, every time.Duration) bool {
	g.mu.Lock()
	limiter, ok := limiters[key]
	if !ok {
		limiter = rate.NewLimiter(rate.Every(every), burst)
		limiters[key] = limiter
	}
	g.mu.Unlock()
	return limiter.AllowN(g.cfg.Now(), 1)
}

func retryAfter(every time.Duration) int {
	return max(1, int((every+time.Second-1)/time.Second))
}

func joinPath(base, path string) string {
	if base == "" || base == "/" {
		return path
	}
	return fmt.Sprintf("%s%s", trimSlash(base), path)
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func bearer(r *http.Request) string {
	value := r.Header.Get(v1alpha1.HeaderAuthorization)
	const prefix = "Bearer "
	if len(value) <= len(prefix) || value[:len(prefix)] != prefix {
		return ""
	}
	return value[len(prefix):]
}

func writeError(w http.ResponseWriter, status int, code v1alpha1.Code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v1alpha1.ErrorBody{Error: code, Description: description})
}

func writeProvider(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v1alpha1.NewProviderError(kind, message))
}
