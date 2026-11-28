//go:build conformance

// Package server is interface 5's server suite: it reaches a gateway through its two surfaces and the
// fixture conformance.md names, never through its internals.
package server

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
	"github.com/garamsh/garam-bot/api/model-gateway/v1alpha1/gatewayfake"
)

// The environment a run is given.
const (
	EnvBaseURL   = "CONFORMANCE_BASE_URL"
	EnvTrustFile = "CONFORMANCE_TRUST_FILE"
	EnvFixture   = "CONFORMANCE_FIXTURE"
	// EnvProfile is the profile the gateway is run in, `place` or `model` (PRO-1, PRO-2). Empty is
	// `place`.
	EnvProfile = "CONFORMANCE_PROFILE"
)

var (
	baseURL string
	client  *http.Client
	fixture []string
	place   bool
	bots    atomic.Int32
)

func TestMain(m *testing.M) {
	baseURL = strings.TrimRight(os.Getenv(EnvBaseURL), "/")
	fixture = strings.Fields(os.Getenv(EnvFixture))
	trust, err := os.ReadFile(os.Getenv(EnvTrustFile))
	profile := os.Getenv(EnvProfile)
	if baseURL == "" || len(fixture) == 0 || err != nil || profile != "" && profile != "place" && profile != "model" {
		fmt.Fprintf(os.Stderr, "%s, %s and %s name the gateway under test, and %s is place or model\n", EnvBaseURL, EnvTrustFile, EnvFixture, EnvProfile)
		os.Exit(2)
	}
	place = profile != "model"
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(trust)
	client = &http.Client{
		Timeout:   time.Minute,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		// The suite reads every answer as the gateway gave it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	os.Exit(m.Run())
}

// placeProfile skips a case of the place profile in a run of the model profile alone (PRO-2).
func placeProfile(t *testing.T) {
	t.Helper()
	if !place {
		t.Skip("PRO-2: the place profile's, and this run is of the model profile")
	}
}

// run asks the fixture, and decodes its answer into into.
func run(t *testing.T, into any, args ...string) {
	t.Helper()
	command := exec.Command(fixture[0], append(append([]string{}, fixture[1:]...), args...)...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	require.NoError(t, err, "fixture %v: %s", args, stderr.String())
	if into != nil {
		require.NoError(t, json.Unmarshal(out, into), "fixture %v answered %s", args, out)
	}
}

func credential(t *testing.T, args ...string) string {
	t.Helper()
	var answer struct {
		Credential string `json:"credential"`
	}
	run(t, &answer, append([]string{"credential"}, args...)...)
	return answer.Credential
}

func providerKey(t *testing.T) string {
	t.Helper()
	var answer struct {
		Key string `json:"key"`
	}
	run(t, &answer, "provider-key")
	require.NotEmpty(t, answer.Key)
	return answer.Key
}

type seen struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Query  string              `json:"query"`
	Header map[string][]string `json:"header"`
	Body   []byte              `json:"body"`
	Size   int64               `json:"size"`
}

func upstream(t *testing.T, args ...string) []seen {
	t.Helper()
	var answer struct {
		Seen []seen `json:"seen"`
	}
	run(t, &answer, append([]string{"seen"}, args...)...)
	return answer.Seen
}

type limits struct {
	BotBurst           int `json:"botBurst"`
	BotEverySeconds    int `json:"botEverySeconds"`
	IssuerBurst        int `json:"issuerBurst"`
	IssuerEverySeconds int `json:"issuerEverySeconds"`
}

type usage struct {
	Namespace                string `json:"namespace"`
	Name                     string `json:"name"`
	UID                      string `json:"uid"`
	Model                    string `json:"model"`
	Status                   int    `json:"status"`
	InputTokens              int64  `json:"inputTokens"`
	OutputTokens             int64  `json:"outputTokens"`
	CacheCreationInputTokens int64  `json:"cacheCreationInputTokens"`
	CacheReadInputTokens     int64  `json:"cacheReadInputTokens"`
}

// usageOf are the usage records of bot's calls.
func usageOf(t *testing.T, bot v1alpha1.TokenRequest) []usage {
	t.Helper()
	var answer struct {
		Usage []usage `json:"usage"`
	}
	run(t, &answer, "usage")
	var of []usage
	for _, u := range answer.Usage {
		if u.UID == bot.UID {
			of = append(of, u)
		}
	}
	return of
}

type answer struct {
	status int
	header http.Header
	body   []byte
}

// request is a call of path below the base URL. A path is sent as it is written, neither cleaned nor
// decoded (T-2).
func request(t *testing.T, method, path, authorization string, body io.Reader, header http.Header) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, baseURL+path, body)
	require.NoError(t, err)
	for name, values := range header {
		r.Header[name] = values
	}
	if authorization != "" {
		r.Header.Set(v1alpha1.HeaderAuthorization, authorization)
	}
	return r
}

func send(t *testing.T, r *http.Request) answer {
	t.Helper()
	response, err := client.Do(r)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return answer{status: response.StatusCode, header: response.Header, body: read}
}

func do(t *testing.T, method, path, authorization string, body []byte, header http.Header) answer {
	t.Helper()
	return send(t, request(t, method, path, authorization, bytes.NewReader(body), header))
}

// newBot is a bot of its own for one case.
func newBot() v1alpha1.TokenRequest {
	n := bots.Add(1)
	return v1alpha1.TokenRequest{Namespace: "conformance", Name: fmt.Sprintf("bot-%d", n), UID: fmt.Sprintf("uid-%d-%d", time.Now().UnixNano(), n)}
}

func issueRaw(t *testing.T, cred string, body []byte) answer {
	t.Helper()
	return do(t, http.MethodPost, v1alpha1.TokensPath, "Bearer "+cred, body, http.Header{"Content-Type": {"application/json"}})
}

func issueAs(t *testing.T, cred string, body any) answer {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return issueRaw(t, cred, encoded)
}

// tokenFor is a model token for bot: from the issue surface in the place profile, and from the
// fixture in the model profile alone.
func tokenFor(t *testing.T, bot v1alpha1.TokenRequest) v1alpha1.ModelToken {
	t.Helper()
	var token v1alpha1.ModelToken
	if !place {
		run(t, &token, "token", "--namespace", bot.Namespace, "--name", bot.Name, "--uid", bot.UID)
		require.NotEmpty(t, token.Token)
		return token
	}
	a := issueAs(t, credential(t), bot)
	require.Equal(t, http.StatusOK, a.status, "control: a token is issued: %s", a.body)
	require.NoError(t, json.Unmarshal(a.body, &token))
	return token
}

var message = []byte(`{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"Reply with the word ready."}]}`)

func headerOf(token string, header http.Header) (string, http.Header) {
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "application/json")
	header.Set("Anthropic-Version", "2023-06-01")
	if token == "" {
		return "", header
	}
	return "Bearer " + token, header
}

func call(t *testing.T, token, path string, header http.Header) answer {
	t.Helper()
	authorization, header := headerOf(token, header)
	return do(t, http.MethodPost, path, authorization, message, header)
}

func wantCode(t *testing.T, a answer, status int, code v1alpha1.Code) {
	t.Helper()
	require.Equal(t, status, a.status, "%s", a.body)
	var body v1alpha1.ErrorBody
	require.NoError(t, json.Unmarshal(a.body, &body), "%s", a.body)
	assert.Equal(t, code, body.Error)
}

func wantProvider(t *testing.T, a answer, status int, kind string) {
	t.Helper()
	require.Equal(t, status, a.status, "%s", a.body)
	var body v1alpha1.ProviderError
	require.NoError(t, json.Unmarshal(a.body, &body), "%s", a.body)
	assert.Equal(t, "error", body.Type)
	assert.Equal(t, kind, body.Error.Type)
}

// wantRetryAfter fails where an answer's Retry-After is no positive whole number of seconds.
func wantRetryAfter(t *testing.T, a answer) {
	t.Helper()
	seconds, err := strconv.Atoi(a.header.Get(v1alpha1.HeaderRetryAfter))
	require.NoError(t, err, "Retry-After %q is no whole number of seconds", a.header.Get(v1alpha1.HeaderRetryAfter))
	assert.Positive(t, seconds)
}

// noKey fails where an answer carries the provider's key in its headers or its body (KEY-1, SEC-1).
func noKey(t *testing.T, a answer, key string) {
	t.Helper()
	assert.NotContains(t, string(a.body), key, "the provider's key reaches an answer's body")
	for name, values := range a.header {
		for _, value := range values {
			assert.NotContains(t, value, key, "the provider's key reaches the answer's %s", name)
		}
	}
}

// claimsOf and withClaims read and replace a JWT's payload, keeping its header and signature, so a
// case can show that an altered token or credential is refused.
func claimsOf(t *testing.T, jwt string) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	require.Len(t, parts, 3, "the fixture's credential is no JWT")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}

func withClaims(t *testing.T, jwt string, claims map[string]any) string {
	t.Helper()
	parts := strings.Split(jwt, ".")
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	return parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]
}

func TestServer(t *testing.T) {
	t.Run("ISS-1/an issuer's credential that is missing, not signed by a key the place trusts, altered, expired, of another audience or past an hour is refused", func(t *testing.T) {
		placeProfile(t)
		bot := newBot()
		served := credential(t)
		forbidden := credential(t, "--issuer", "another-issuer")
		altered := claimsOf(t, forbidden)
		altered["sub"] = claimsOf(t, served)["sub"]
		require.NotEqual(t, claimsOf(t, forbidden)["sub"], altered["sub"], "control: the altered credential names another issuer than it was signed for")
		for name, cred := range map[string]string{
			"missing":          "",
			"not a credential": "not-a-credential",
			"another key":      credential(t, "--foreign"),
			"altered":          withClaims(t, forbidden, altered),
			"expired":          credential(t, "--expired"),
			"another audience": credential(t, "--audience", "garam-workspace"),
			"past an hour":     credential(t, "--lifetime", "2h"),
		} {
			t.Run(name, func(t *testing.T) {
				wantCode(t, issueAs(t, cred, bot), http.StatusUnauthorized, v1alpha1.CodeInvalidCredential)
			})
		}
		a := issueAs(t, served, bot)
		assert.Equal(t, http.StatusOK, a.status, "control: the issuer's own credential is refused: %s", a.body)
	})
	t.Run("ISS-1/a credential naming no issuer the gateway serves is forbidden", func(t *testing.T) {
		placeProfile(t)
		wantCode(t, issueAs(t, credential(t, "--issuer", "another-issuer"), newBot()), http.StatusForbidden, v1alpha1.CodeForbidden)
		assert.Equal(t, http.StatusOK, issueAs(t, credential(t), newBot()).status, "control")
	})
	t.Run("ISS-2/a request missing a member, naming one it does not know, naming one twice, not one object, or above the bound is refused", func(t *testing.T) {
		placeProfile(t)
		cred := credential(t)
		long := strings.Repeat("a", v1alpha1.MaxIdentity+1)
		for name, body := range map[string]string{
			"no uid":          `{"namespace":"conformance","name":"bot"}`,
			"unknown member":  `{"namespace":"conformance","name":"bot","uid":"u","model":"x"}`,
			"another case":    `{"NAMESPACE":"conformance","name":"bot","uid":"u"}`,
			"a member twice":  `{"namespace":"conformance","name":"bot","uid":"u","uid":"v"}`,
			"trailing data":   `{"namespace":"conformance","name":"bot","uid":"u"} x`,
			"two objects":     `{"namespace":"conformance","name":"bot","uid":"u"}{}`,
			"not a string":    `{"namespace":"conformance","name":"bot","uid":7}`,
			"a long name":     `{"namespace":"conformance","name":"` + long + `","uid":"u"}`,
			"not json at all": `namespace=conformance`,
		} {
			t.Run(name, func(t *testing.T) {
				wantCode(t, issueRaw(t, cred, []byte(body)), http.StatusBadRequest, v1alpha1.CodeInvalid)
			})
		}
		control, err := json.Marshal(newBot())
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, issueRaw(t, cred, control).status, "control")
	})
	t.Run("ISS-3/the issue surface refuses its path and method, then the credential, then the issuer, then the body", func(t *testing.T) {
		placeProfile(t)
		bad := []byte(`{"namespace":"conformance"}`)
		wantCode(t, do(t, http.MethodPost, v1alpha1.PathPrefix+"tokens", "", bad, nil), http.StatusNotFound, v1alpha1.CodeNotFound)
		wantCode(t, do(t, http.MethodGet, v1alpha1.TokensPath, "", nil, nil), http.StatusMethodNotAllowed, v1alpha1.CodeInvalid)
		wantCode(t, issueRaw(t, "", bad), http.StatusUnauthorized, v1alpha1.CodeInvalidCredential)
		wantCode(t, issueRaw(t, credential(t, "--issuer", "another-issuer"), bad), http.StatusForbidden, v1alpha1.CodeForbidden)
		wantCode(t, issueRaw(t, credential(t), bad), http.StatusBadRequest, v1alpha1.CodeInvalid)
	})
	t.Run("T-4/the Bearer scheme is taken in any case", func(t *testing.T) {
		bot := newBot()
		if place {
			encoded, err := json.Marshal(bot)
			require.NoError(t, err)
			a := do(t, http.MethodPost, v1alpha1.TokensPath, "bearer "+credential(t), encoded, http.Header{"Content-Type": {"application/json"}})
			assert.Equal(t, http.StatusOK, a.status, "the issue surface: %s", a.body)
		}
		token := tokenFor(t, bot)
		_, header := headerOf("", nil)
		a := do(t, http.MethodPost, v1alpha1.MessagesPath, "BEARER "+token.Token, message, header)
		assert.Equal(t, http.StatusOK, a.status, "the model surface: %s", a.body)
	})
	t.Run("TOKEN-1/a token lives at most an hour and is refused once it expires", func(t *testing.T) {
		token := tokenFor(t, newBot())
		require.Positive(t, token.ExpiresIn)
		require.LessOrEqual(t, token.ExpiresIn, int64(v1alpha1.MaxTokenLife/time.Second))
		require.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.MessagesPath, nil).status, "control: the token is taken")
		run(t, nil, "clock", "--advance", fmt.Sprintf("%ds", token.ExpiresIn+1))
		wantProvider(t, call(t, token.Token, v1alpha1.MessagesPath, nil), http.StatusUnauthorized, v1alpha1.ErrorAuthentication)
	})
	t.Run("TOKEN-2/a token issued before a restart is taken after it", func(t *testing.T) {
		placeProfile(t)
		token := tokenFor(t, newBot())
		run(t, nil, "restart")
		assert.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.MessagesPath, nil).status)
	})
	t.Run("MOD-1/a call with no token, a token not issued, one of another key or one altered is refused", func(t *testing.T) {
		bot := newBot()
		token := tokenFor(t, bot)
		var foreign v1alpha1.ModelToken
		run(t, &foreign, "token", "--namespace", bot.Namespace, "--name", bot.Name, "--uid", bot.UID, "--foreign")
		claims := claimsOf(t, token.Token)
		other := tokenFor(t, newBot())
		for name, value := range claimsOf(t, other.Token) {
			if name != "exp" && name != "iat" {
				claims[name] = value
			}
		}
		before := len(upstream(t))
		for name, value := range map[string]string{
			"none": "", "not issued": "not-a-token", "another key": foreign.Token,
			"altered": withClaims(t, token.Token, claims), "signature altered": token.Token + "x",
		} {
			t.Run(name, func(t *testing.T) {
				wantProvider(t, call(t, value, v1alpha1.MessagesPath, nil), http.StatusUnauthorized, v1alpha1.ErrorAuthentication)
			})
		}
		assert.Len(t, upstream(t), before, "a refused call reaches the upstream")
		assert.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.MessagesPath, nil).status, "control")
	})
	t.Run("MOD-2/a call is forwarded with its path, query and body, no header but the provider API's own, the provider's key and the transport's", func(t *testing.T) {
		key := providerKey(t)
		token := tokenFor(t, newBot())
		header := http.Header{
			"X-Api-Key": {"sk-workspace-own"}, "Cookie": {"session=1"}, "X-Custom": {"kept?"},
			"Proxy-Authorization": {"Basic d29ya3NwYWNlOnNlY3JldA=="}, "Forwarded": {"for=10.0.0.7"},
			"X-Forwarded-For": {"10.0.0.7"}, "Anthropic-Dangerous-Direct-Browser-Access": {"true"},
			"Anthropic-Beta": {"feature-1"}, "Accept": {"application/json"},
		}
		a := call(t, token.Token, v1alpha1.MessagesPath+"?beta=true", header)
		require.Equal(t, http.StatusOK, a.status, "%s", a.body)
		got := upstream(t)
		require.NotEmpty(t, got)
		last := got[len(got)-1]
		assert.Equal(t, http.MethodPost, last.Method)
		assert.Equal(t, v1alpha1.MessagesPath, last.Path)
		assert.Equal(t, "beta=true", last.Query)
		assert.Equal(t, message, last.Body)
		assert.Equal(t, []string{key}, last.Header[v1alpha1.HeaderProviderKey], "the provider's key is not the one set")
		assert.Equal(t, []string{"feature-1"}, last.Header["Anthropic-Beta"])
		assert.Equal(t, []string{"2023-06-01"}, last.Header["Anthropic-Version"])
		allowed := slices.Concat(v1alpha1.ForwardedHeaders, v1alpha1.TransportHeaders, []string{v1alpha1.HeaderProviderKey})
		for name := range last.Header {
			assert.True(t, slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, name) }),
				"the upstream receives the header %s, which MOD-2 does not admit", name)
		}
		assert.NotContains(t, a.header, "Set-Cookie", "the upstream's cookie reaches the harness")
		assert.Equal(t, gatewayfake.ProviderRequestID, a.header.Get(gatewayfake.ProviderHeader), "the upstream's own header is not passed on")
		assert.Contains(t, string(a.body), `"ready"`, "the upstream's body is not passed on")
		noKey(t, a, key)
	})
	t.Run("MOD-2/an upstream's refusal is passed on with its status and Retry-After", func(t *testing.T) {
		token := tokenFor(t, newBot())
		run(t, nil, "answer", "--status", "429", "--retry-after", "17")
		a := call(t, token.Token, v1alpha1.MessagesPath, nil)
		assert.Equal(t, http.StatusTooManyRequests, a.status)
		assert.Equal(t, "17", a.header.Get(v1alpha1.HeaderRetryAfter))
		assert.Equal(t, gatewayfake.ProviderRequestID, a.header.Get(gatewayfake.ProviderHeader))
	})
	t.Run("MOD-2/a streamed answer is passed on as each part arrives", func(t *testing.T) {
		token := tokenFor(t, newBot())
		run(t, nil, "answer", "--stream")
		authorization, header := headerOf(token.Token, nil)
		response, err := client.Do(request(t, http.MethodPost, v1alpha1.MessagesPath, authorization, bytes.NewReader(message), header))
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		lines := bufio.NewReader(response.Body)
		first, err := lines.ReadString('\n')
		require.NoError(t, err, "the first event is held until the stream ends")
		assert.Equal(t, "event: message_start\n", first)
		run(t, nil, "release")
		rest, err := io.ReadAll(lines)
		require.NoError(t, err)
		assert.Contains(t, string(rest), "message_delta", "control: the stream's last event arrives")
	})
	t.Run("MOD-3/a method and path the provider API table does not name are refused and forwarded nowhere", func(t *testing.T) {
		token := tokenFor(t, newBot())
		before := len(upstream(t))
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/v1/files"},
			{http.MethodPost, "/v1/messages/batches"},
			{http.MethodGet, "/v1/messages/batches"},
			{http.MethodGet, v1alpha1.MessagesPath},
			{http.MethodPut, v1alpha1.MessagesPath},
			{http.MethodPost, "/v1/messages/"},
			{http.MethodPost, "/v1//messages"},
			{http.MethodPost, "/v1/messages/../files"},
			{http.MethodPost, "/v1/messages%2Fcount_tokens"},
			{http.MethodPost, "/V1/messages"},
		} {
			t.Run(c.method+" "+c.path, func(t *testing.T) {
				authorization, header := headerOf(token.Token, nil)
				wantProvider(t, do(t, c.method, c.path, authorization, message, header), http.StatusNotFound, v1alpha1.ErrorNotFound)
			})
		}
		assert.Len(t, upstream(t), before, "a refused call reaches the upstream")
		assert.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.CountTokensPath, nil).status, "control: count_tokens is forwarded")
		assert.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.MessagesPath, nil).status, "control: messages is forwarded")
	})
	t.Run("MOD-4/a body above 32 MiB is refused and forwarded nowhere, and one of 32 MiB is forwarded", func(t *testing.T) {
		token := tokenFor(t, newBot())
		authorization, header := headerOf(token.Token, nil)
		before := len(upstream(t))
		large := bytes.Repeat([]byte("x"), v1alpha1.MaxRequestBody+1)
		wantProvider(t, do(t, http.MethodPost, v1alpha1.MessagesPath, authorization, large, header), http.StatusRequestEntityTooLarge, v1alpha1.ErrorRequestTooLarge)
		// A reader of no known length is sent chunked, with no Content-Length.
		chunked := request(t, http.MethodPost, v1alpha1.MessagesPath, authorization, io.MultiReader(bytes.NewReader(large)), header)
		chunked.ContentLength = -1
		wantProvider(t, send(t, chunked), http.StatusRequestEntityTooLarge, v1alpha1.ErrorRequestTooLarge)
		assert.Len(t, upstream(t), before, "a refused call reaches the upstream")
		whole := do(t, http.MethodPost, v1alpha1.MessagesPath, authorization, large[:v1alpha1.MaxRequestBody], header)
		require.Equal(t, http.StatusOK, whole.status, "a body of 32 MiB is refused: %s", whole.body)
		got := upstream(t)
		require.Len(t, got, before+1)
		assert.EqualValues(t, v1alpha1.MaxRequestBody, got[len(got)-1].Size, "a body of 32 MiB is not forwarded whole")
	})
	t.Run("MOD-5/an upstream redirect is answered 502 and no call reaches its Location, nor does a hang-up", func(t *testing.T) {
		key := providerKey(t)
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				// A bot of its own for each, so no call is refused for its limit.
				token := tokenFor(t, newBot())
				run(t, nil, "answer", "--redirect", strconv.Itoa(status))
				a := call(t, token.Token, v1alpha1.MessagesPath, nil)
				wantProvider(t, a, http.StatusBadGateway, v1alpha1.ErrorAPI)
				assert.Empty(t, a.header.Get("Location"), "the upstream's redirect reaches the harness")
				noKey(t, a, key)
			})
		}
		assert.Empty(t, upstream(t, "--redirected"), "a call reaches a redirect's Location")
		token := tokenFor(t, newBot())
		run(t, nil, "answer", "--close")
		a := call(t, token.Token, v1alpha1.MessagesPath, nil)
		wantProvider(t, a, http.StatusBadGateway, v1alpha1.ErrorAPI)
		noKey(t, a, key)
		run(t, nil, "answer", "--status", "529")
		assert.Equal(t, 529, call(t, token.Token, v1alpha1.MessagesPath, nil).status, "control: the upstream's own status is passed on")
	})
	t.Run("MOD-6/an upstream that hangs up in the middle of a stream breaks the answer off", func(t *testing.T) {
		token := tokenFor(t, newBot())
		run(t, nil, "answer", "--stream", "--break")
		authorization, header := headerOf(token.Token, nil)
		response, err := client.Do(request(t, http.MethodPost, v1alpha1.MessagesPath, authorization, bytes.NewReader(message), header))
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		require.Equal(t, http.StatusOK, response.StatusCode, "control: the stream began")
		read, err := io.ReadAll(response.Body)
		assert.Error(t, err, "the broken stream ends as a whole answer ends")
		assert.Contains(t, string(read), "message_start", "control: the stream's first event arrived")
		assert.NotContains(t, string(read), `"error"`, "the gateway adds to a broken stream")
	})
	t.Run("MOD-7/the model surface refuses the token, then the method and path, then the limit, then the size", func(t *testing.T) {
		var l limits
		run(t, &l, "limits")
		_, header := headerOf("", nil)
		large := bytes.Repeat([]byte("x"), v1alpha1.MaxRequestBody+1)
		wantProvider(t, do(t, http.MethodPost, "/v1/files", "", message, header), http.StatusUnauthorized, v1alpha1.ErrorAuthentication)
		wantProvider(t, do(t, http.MethodPost, v1alpha1.MessagesPath, "", large, header), http.StatusUnauthorized, v1alpha1.ErrorAuthentication)
		token := tokenFor(t, newBot())
		authorization := "Bearer " + token.Token
		for range l.BotBurst {
			require.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.CountTokensPath, nil).status, "control: within the bot's limit")
		}
		wantProvider(t, do(t, http.MethodPost, "/v1/files", authorization, message, header), http.StatusNotFound, v1alpha1.ErrorNotFound)
		wantProvider(t, do(t, http.MethodPost, v1alpha1.MessagesPath, authorization, large, header), http.StatusTooManyRequests, v1alpha1.ErrorRateLimit)
	})
	t.Run("USE-1/each forwarded call is recorded for the bot its token names, with each of the provider's counts", func(t *testing.T) {
		bot := newBot()
		token := tokenFor(t, bot)
		require.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.MessagesPath, nil).status)
		records := usageOf(t, bot)
		require.Len(t, records, 1, "the call is recorded for no bot its token names, or more than once")
		u := records[0]
		assert.Equal(t, bot.Name, u.Name)
		assert.Equal(t, bot.Namespace, u.Namespace)
		assert.Equal(t, "claude-test", u.Model)
		assert.Equal(t, http.StatusOK, u.Status)
		assert.EqualValues(t, gatewayfake.ProviderInputTokens, u.InputTokens)
		assert.EqualValues(t, gatewayfake.ProviderOutputTokens, u.OutputTokens)
		assert.EqualValues(t, gatewayfake.ProviderCacheCreationTokens, u.CacheCreationInputTokens)
		assert.EqualValues(t, gatewayfake.ProviderCacheReadTokens, u.CacheReadInputTokens)
	})
	t.Run("USE-1/a stream past a MiB is recorded with the counts its last events report", func(t *testing.T) {
		bot := newBot()
		token := tokenFor(t, bot)
		run(t, nil, "answer", "--stream", "--long")
		authorization, header := headerOf(token.Token, nil)
		response, err := client.Do(request(t, http.MethodPost, v1alpha1.MessagesPath, authorization, bytes.NewReader(message), header))
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		run(t, nil, "release")
		read, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Greater(t, len(read), 1<<20, "control: the stream runs past a MiB")
		require.Contains(t, string(read), "message_delta")
		records := usageOf(t, bot)
		require.Len(t, records, 1)
		assert.EqualValues(t, gatewayfake.ProviderInputTokens, records[0].InputTokens, "message_start's count")
		assert.EqualValues(t, gatewayfake.ProviderOutputTokens, records[0].OutputTokens, "message_delta's count, past a MiB")
	})
	t.Run("LIMIT-1/a bot past its limit is answered 429 alone, and forwarded nowhere", func(t *testing.T) {
		var l limits
		run(t, &l, "limits")
		token, other := tokenFor(t, newBot()), tokenFor(t, newBot())
		for range l.BotBurst {
			require.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.CountTokensPath, nil).status)
		}
		before := len(upstream(t))
		a := call(t, token.Token, v1alpha1.CountTokensPath, nil)
		wantProvider(t, a, http.StatusTooManyRequests, v1alpha1.ErrorRateLimit)
		wantRetryAfter(t, a)
		assert.Len(t, upstream(t), before, "a call past the limit reaches the upstream")
		assert.Equal(t, http.StatusOK, call(t, other.Token, v1alpha1.CountTokensPath, nil).status, "control: another bot's calls are taken")
	})
	t.Run("LIMIT-2/an issuer past its limit is answered 429", func(t *testing.T) {
		placeProfile(t)
		var l limits
		run(t, &l, "limits")
		// The cases above spent from the issuer's limit too: the gateway's clock is moved until it is full.
		run(t, nil, "clock", "--advance", fmt.Sprintf("%ds", l.IssuerBurst*l.IssuerEverySeconds))
		cred := credential(t)
		for range l.IssuerBurst {
			require.Equal(t, http.StatusOK, issueAs(t, cred, newBot()).status)
		}
		a := issueAs(t, cred, newBot())
		wantCode(t, a, http.StatusTooManyRequests, v1alpha1.CodeTemporarilyUnavailable)
		wantRetryAfter(t, a)
	})
}
