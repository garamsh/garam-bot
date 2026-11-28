//go:build conformance

// Package server is interface 5's server suite: it reaches a gateway through its two surfaces and the
// fixture conformance.md names, never through its internals.
package server

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// The environment a run is given.
const (
	EnvBaseURL   = "CONFORMANCE_BASE_URL"
	EnvTrustFile = "CONFORMANCE_TRUST_FILE"
	EnvFixture   = "CONFORMANCE_FIXTURE"
)

var (
	baseURL string
	client  *http.Client
	fixture []string
	bots    atomic.Int32
)

func TestMain(m *testing.M) {
	baseURL = strings.TrimRight(os.Getenv(EnvBaseURL), "/")
	fixture = strings.Fields(os.Getenv(EnvFixture))
	trust, err := os.ReadFile(os.Getenv(EnvTrustFile))
	if baseURL == "" || len(fixture) == 0 || err != nil {
		fmt.Fprintf(os.Stderr, "%s, %s and %s name the gateway under test\n", EnvBaseURL, EnvTrustFile, EnvFixture)
		os.Exit(2)
	}
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
}

func upstream(t *testing.T) []seen {
	t.Helper()
	var answer struct {
		Seen []seen `json:"seen"`
	}
	run(t, &answer, "seen")
	return answer.Seen
}

type limits struct {
	BotBurst           int `json:"botBurst"`
	BotEverySeconds    int `json:"botEverySeconds"`
	IssuerBurst        int `json:"issuerBurst"`
	IssuerEverySeconds int `json:"issuerEverySeconds"`
}

type answer struct {
	status int
	header http.Header
	body   []byte
}

func do(t *testing.T, method, path, authorization string, body []byte, header http.Header) answer {
	t.Helper()
	request, err := http.NewRequest(method, baseURL+path, bytes.NewReader(body))
	require.NoError(t, err)
	for name, values := range header {
		request.Header[name] = values
	}
	if authorization != "" {
		request.Header.Set(v1alpha1.HeaderAuthorization, authorization)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return answer{status: response.StatusCode, header: response.Header, body: read}
}

// newBot is a bot of its own for one case.
func newBot() v1alpha1.TokenRequest {
	n := bots.Add(1)
	return v1alpha1.TokenRequest{Namespace: "conformance", Name: fmt.Sprintf("bot-%d", n), UID: fmt.Sprintf("uid-%d-%d", time.Now().UnixNano(), n)}
}

func issueAs(t *testing.T, cred string, body any) answer {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return do(t, http.MethodPost, v1alpha1.TokensPath, "Bearer "+cred, encoded, http.Header{"Content-Type": {"application/json"}})
}

// tokenFor issues a model token for bot.
func tokenFor(t *testing.T, bot v1alpha1.TokenRequest) v1alpha1.ModelToken {
	t.Helper()
	a := issueAs(t, credential(t), bot)
	require.Equal(t, http.StatusOK, a.status, "control: a token is issued: %s", a.body)
	var token v1alpha1.ModelToken
	require.NoError(t, json.Unmarshal(a.body, &token))
	return token
}

var message = []byte(`{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"Reply with the word ready."}]}`)

func call(t *testing.T, token, path string, header http.Header) answer {
	t.Helper()
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "application/json")
	header.Set("Anthropic-Version", "2023-06-01")
	authorization := ""
	if token != "" {
		authorization = "Bearer " + token
	}
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

func TestServer(t *testing.T) {
	t.Run("ISS-1/an issuer's credential that is missing, does not verify, has expired, names another audience or lives past an hour is refused", func(t *testing.T) {
		bot := newBot()
		for name, cred := range map[string]string{
			"missing":          "",
			"not a credential": "not-a-credential",
			"expired":          credential(t, "--expired"),
			"another audience": credential(t, "--audience", "garam-workspace"),
			"past an hour":     credential(t, "--lifetime", "2h"),
		} {
			t.Run(name, func(t *testing.T) {
				wantCode(t, issueAs(t, cred, bot), http.StatusUnauthorized, v1alpha1.CodeInvalidCredential)
			})
		}
		a := issueAs(t, credential(t), bot)
		assert.Equal(t, http.StatusOK, a.status, "control: the issuer's own credential is refused: %s", a.body)
	})
	t.Run("ISS-1/a credential naming no issuer the gateway serves is forbidden", func(t *testing.T) {
		wantCode(t, issueAs(t, credential(t, "--issuer", "another-issuer"), newBot()), http.StatusForbidden, v1alpha1.CodeForbidden)
		assert.Equal(t, http.StatusOK, issueAs(t, credential(t), newBot()).status, "control")
	})
	t.Run("ISS-2/a request missing a member, naming one it does not know, or above the bound is refused", func(t *testing.T) {
		cred := credential(t)
		long := strings.Repeat("a", v1alpha1.MaxIdentity+1)
		for name, body := range map[string]any{
			"no uid":         map[string]string{"namespace": "conformance", "name": "bot"},
			"unknown member": map[string]string{"namespace": "conformance", "name": "bot", "uid": "u", "model": "x"},
			"long name":      map[string]string{"namespace": "conformance", "name": long, "uid": "u"},
		} {
			a := issueAs(t, cred, body)
			assert.Equal(t, http.StatusBadRequest, a.status, "%s: %s", name, a.body)
		}
		assert.Equal(t, http.StatusOK, issueAs(t, cred, newBot()).status, "control")
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
		token := tokenFor(t, newBot())
		run(t, nil, "restart")
		assert.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.MessagesPath, nil).status)
	})
	t.Run("MOD-1/a call with no token, a token not issued, or one altered is refused", func(t *testing.T) {
		token := tokenFor(t, newBot())
		before := len(upstream(t))
		for name, value := range map[string]string{"none": "", "not issued": "not-a-token", "altered": token.Token + "x"} {
			t.Run(name, func(t *testing.T) {
				wantProvider(t, call(t, value, v1alpha1.MessagesPath, nil), http.StatusUnauthorized, v1alpha1.ErrorAuthentication)
			})
		}
		assert.Len(t, upstream(t), before, "a refused call reaches the upstream")
		assert.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.MessagesPath, nil).status, "control")
	})
	t.Run("MOD-2/a call is forwarded with its path, query and body, its own credentials removed and the provider's key set", func(t *testing.T) {
		key := providerKey(t)
		token := tokenFor(t, newBot())
		header := http.Header{
			"X-Api-Key": {"sk-workspace-own"}, "Cookie": {"session=1"}, "X-Custom": {"kept?"},
			"Anthropic-Beta": {"feature-1"},
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
		for _, name := range []string{"Authorization", "Cookie", "X-Custom"} {
			assert.NotContains(t, last.Header, name, "the call's %s reaches the upstream", name)
		}
		assert.NotContains(t, a.header, "Set-Cookie", "the upstream's cookie reaches the harness")
		assert.Contains(t, string(a.body), `"ready"`, "the upstream's body is not passed on")
		noKey(t, a, key)
	})
	t.Run("MOD-2/a streamed answer is passed on as each part arrives", func(t *testing.T) {
		token := tokenFor(t, newBot())
		run(t, nil, "answer", "--stream")
		request, err := http.NewRequest(http.MethodPost, baseURL+v1alpha1.MessagesPath, bytes.NewReader(message))
		require.NoError(t, err)
		request.Header.Set(v1alpha1.HeaderAuthorization, "Bearer "+token.Token)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		lines := bufio.NewReader(response.Body)
		first, err := lines.ReadString('\n')
		require.NoError(t, err, "the first event is held until the stream ends")
		assert.Equal(t, "event: message_start\n", first)
		run(t, nil, "release")
		rest, err := io.ReadAll(lines)
		require.NoError(t, err)
		assert.Contains(t, string(rest), "message_delta", "control: the stream's second event arrives")
	})
	t.Run("MOD-3/a path the provider API does not name is refused and forwarded nowhere", func(t *testing.T) {
		token := tokenFor(t, newBot())
		before := len(upstream(t))
		wantProvider(t, call(t, token.Token, "/v1/files", nil), http.StatusNotFound, v1alpha1.ErrorNotFound)
		assert.Len(t, upstream(t), before)
		assert.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.CountTokensPath, nil).status, "control: count_tokens is forwarded")
	})
	t.Run("MOD-4/a body above 32 MiB is refused and forwarded nowhere", func(t *testing.T) {
		token := tokenFor(t, newBot())
		before := len(upstream(t))
		large := bytes.Repeat([]byte("x"), v1alpha1.MaxRequestBody+1)
		a := do(t, http.MethodPost, v1alpha1.MessagesPath, "Bearer "+token.Token, large, http.Header{"Content-Type": {"application/json"}})
		wantProvider(t, a, http.StatusRequestEntityTooLarge, v1alpha1.ErrorRequestTooLarge)
		assert.Len(t, upstream(t), before)
	})
	t.Run("MOD-5/an upstream redirect or hang-up is answered 502 and not followed", func(t *testing.T) {
		token := tokenFor(t, newBot())
		key := providerKey(t)
		for _, script := range []string{"--redirect", "--close"} {
			run(t, nil, "answer", script)
			a := call(t, token.Token, v1alpha1.MessagesPath, nil)
			wantProvider(t, a, http.StatusBadGateway, v1alpha1.ErrorAPI)
			assert.Empty(t, a.header.Get("Location"), "the upstream's redirect reaches the harness")
			noKey(t, a, key)
		}
		run(t, nil, "answer", "--status", "529")
		a := call(t, token.Token, v1alpha1.MessagesPath, nil)
		assert.Equal(t, 529, a.status, "control: the upstream's own status is passed on")
	})
	t.Run("USE-1/each forwarded call is recorded for the bot its token names, with the provider's usage", func(t *testing.T) {
		bot := newBot()
		token := tokenFor(t, bot)
		require.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.MessagesPath, nil).status)
		var answer struct {
			Usage []struct {
				Namespace    string `json:"namespace"`
				Name         string `json:"name"`
				UID          string `json:"uid"`
				Model        string `json:"model"`
				Status       int    `json:"status"`
				InputTokens  int64  `json:"inputTokens"`
				OutputTokens int64  `json:"outputTokens"`
			} `json:"usage"`
		}
		run(t, &answer, "usage")
		found := false
		for _, u := range answer.Usage {
			if u.UID == bot.UID {
				found = true
				assert.Equal(t, bot.Name, u.Name)
				assert.Equal(t, "claude-test", u.Model)
				assert.Equal(t, http.StatusOK, u.Status)
				assert.Positive(t, u.InputTokens)
				assert.Positive(t, u.OutputTokens)
			}
		}
		assert.True(t, found, "the call is recorded for no bot its token names")
	})
	t.Run("LIMIT-1/a bot past its limit is answered 429 alone", func(t *testing.T) {
		var l limits
		run(t, &l, "limits")
		token, other := tokenFor(t, newBot()), tokenFor(t, newBot())
		for range l.BotBurst {
			require.Equal(t, http.StatusOK, call(t, token.Token, v1alpha1.CountTokensPath, nil).status)
		}
		a := call(t, token.Token, v1alpha1.CountTokensPath, nil)
		wantProvider(t, a, http.StatusTooManyRequests, v1alpha1.ErrorRateLimit)
		assert.NotEmpty(t, a.header.Get(v1alpha1.HeaderRetryAfter))
		assert.Equal(t, http.StatusOK, call(t, other.Token, v1alpha1.CountTokensPath, nil).status, "control: another bot's calls are taken")
	})
	t.Run("LIMIT-2/an issuer past its limit is answered 429", func(t *testing.T) {
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
		assert.NotEmpty(t, a.header.Get(v1alpha1.HeaderRetryAfter))
	})
}
