//go:build conformance

// Package server is conformance.md's server suite. It reaches the server under test only through the
// protocol and the fixture command CONFORMANCE_FIXTURE names.
package server

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/garamsh/garam-bot/api/bot-token/v1alpha1"
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
)

func TestMain(m *testing.M) {
	baseURL = strings.TrimRight(os.Getenv(EnvBaseURL), "/")
	fixture = strings.Fields(os.Getenv(EnvFixture))
	trust, err := os.ReadFile(os.Getenv(EnvTrustFile))
	if baseURL == "" || len(fixture) == 0 || err != nil {
		fmt.Fprintf(os.Stderr, "%s, %s and %s name the server under test\n", EnvBaseURL, EnvTrustFile, EnvFixture)
		os.Exit(2)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(trust) {
		fmt.Fprintf(os.Stderr, "%s holds no PEM certificate\n", os.Getenv(EnvTrustFile))
		os.Exit(2)
	}
	client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}}
	os.Exit(m.Run())
}

func TestServer(t *testing.T) {
	t.Run("server/transport/refuses-tls-below-1.3", transportRefusesOldTLS)
	t.Run("server/token/bound-to-the-proofs-key", tokenBoundToTheProofsKey)
	t.Run("server/token/asks-for-a-nonce-with-htu", tokenAsksForANonce)
	t.Run("server/credential/refuses-missing-or-unverifiable", credentialRefusesUnverifiable)
	t.Run("server/credential/spent-whatever-key", credentialSpentWhateverKey)
	t.Run("server/credential/one-token-at-once", credentialOneTokenAtOnce)
	t.Run("server/bot/refuses-a-bot-not-garams", botRefusesNotGarams)
	t.Run("server/limit/each-bot-alone", limitEachBotAlone)
	t.Run("server/secrets/no-token-in-a-refusal", secretsNoTokenInARefusal)
}

var bots atomic.Int32

// garamBot places a garam-owned bot of a namespace of its own, and answers its namespace and name.
func garamBot(t *testing.T) (string, string) {
	t.Helper()
	namespace, name := fmt.Sprintf("conformance-%d", bots.Add(1)), "mira"
	run(t, "bot", namespace, name, "--owner", "garam")
	return namespace, name
}

func transportRefusesOldTLS(t *testing.T) {
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12, //nolint:gosec // only the version is under test
	}}}
	_, err := old.Post(baseURL+v1alpha1.TokenPath, "", nil)
	assert.Error(t, err, "a TLS 1.2 handshake is refused (T-1)")
	// Control: the same call over TLS 1.3 is answered.
	namespace, name := garamBot(t)
	assert.Equal(t, v1alpha1.CodeUseDPoPNonce, call(t, credential(t, namespace, name), "").code)
}

func tokenBoundToTheProofsKey(t *testing.T) {
	namespace, name := garamBot(t)
	p := newProcess(t)

	answered := p.obtain(t, credential(t, namespace, name))

	require.Equal(t, http.StatusOK, answered.status, answered.raw)
	var token v1alpha1.Token
	require.NoError(t, json.Unmarshal([]byte(answered.raw), &token))
	assert.Equal(t, v1alpha1.TokenTypeDPoP, token.TokenType)
	assert.Positive(t, token.ExpiresIn)
	assert.Equal(t, p.thumbprint(t), binding(t, token.AccessToken), "the token is bound to the key that signed the proof")
}

func tokenAsksForANonce(t *testing.T) {
	namespace, name := garamBot(t)
	credential := credential(t, namespace, name)

	asked := call(t, credential, "")

	require.Equal(t, http.StatusBadRequest, asked.status)
	assert.Equal(t, v1alpha1.CodeUseDPoPNonce, asked.code)
	assert.NotEmpty(t, asked.body.HTU, "the answer names the htu a proof is signed for (NONCE-1)")
	p := newProcess(t)
	withoutNonce := call(t, credential, p.proof(t, asked.body.HTU, ""))
	assert.Equal(t, v1alpha1.CodeUseDPoPNonce, withoutNonce.code, "a proof without a nonce is asked for one")
	assert.NotEmpty(t, withoutNonce.nonce, "the nonce is answered")
}

func credentialRefusesUnverifiable(t *testing.T) {
	namespace, name := garamBot(t)
	for label, credential := range map[string]string{
		"missing":              "",
		"not a credential":     "garbage",
		"for another audience": runCredential(t, namespace, name, "--audience", "another-audience"),
		"expired":              runCredential(t, namespace, name, "--expired"),
		"living over an hour":  runCredential(t, namespace, name, "--lifetime", "61m"),
		"issued before start":  runCredential(t, namespace, name, "--before-start"),
	} {
		refused := call(t, credential, "")
		assert.Equal(t, http.StatusUnauthorized, refused.status, label)
		assert.Equal(t, v1alpha1.CodeInvalidCredential, refused.code, label)
	}
	unschemed := callWith(t, runCredential(t, namespace, name), "")
	assert.Equal(t, http.StatusUnauthorized, unschemed.status, "a credential without the Bearer scheme")
	assert.Equal(t, v1alpha1.CodeInvalidCredential, unschemed.code)
	// Control: the bot's credential as the place issues it is taken.
	assert.Equal(t, v1alpha1.CodeUseDPoPNonce, call(t, credential(t, namespace, name), "").code)
}

// limitAttempts bounds how many tokens the limit test obtains for one bot before it expects a 429. A
// server's limit is its own (LIMIT-1); this one is far above any a process needs in a renewal.
const limitAttempts = 50

func limitEachBotAlone(t *testing.T) {
	namespace, name := garamBot(t)
	other, otherName := garamBot(t)
	p := newProcess(t)
	var limited answered
	for range limitAttempts {
		answer := p.obtain(t, credential(t, namespace, name))
		if answer.status != http.StatusOK {
			limited = answer
			break
		}
	}
	require.Equal(t, http.StatusTooManyRequests, limited.status, "a bot's exchanges are limited (LIMIT-1)")
	assert.Equal(t, v1alpha1.CodeTemporarilyUnavailable, limited.code)
	assert.NotEmpty(t, limited.retryAfter, "the limit names how long to wait")

	// Control: another bot's exchanges are not limited by the first's.
	assert.Equal(t, http.StatusOK, p.obtain(t, credential(t, other, otherName)).status)
}

func credentialSpentWhateverKey(t *testing.T) {
	namespace, name := garamBot(t)
	credential := credential(t, namespace, name)
	require.Equal(t, http.StatusOK, newProcess(t).obtain(t, credential).status)

	other := newProcess(t)
	refused := other.obtain(t, credential)
	assert.Equal(t, http.StatusUnauthorized, refused.status, "a spent credential obtains nothing (CRED-3)")
	assert.Equal(t, v1alpha1.CodeInvalidCredential, refused.code)

	// Control: a fresh credential of the same bot, with the same other key, obtains a token.
	assert.Equal(t, http.StatusOK, other.obtain(t, runCredential(t, namespace, name)).status)
}

func credentialOneTokenAtOnce(t *testing.T) {
	namespace, name := garamBot(t)
	first := newProcess(t)
	asked := first.ask(t, credential(t, namespace, name))
	credential := credential(t, namespace, name)

	statuses := make([]int, 8)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Go(func() {
			p := newProcess(t)
			statuses[i] = call(t, credential, p.proof(t, asked.htu, asked.nonce)).status
		})
	}
	wg.Wait()

	ok := 0
	for _, status := range statuses {
		if status == http.StatusOK {
			ok++
		}
	}
	assert.Equal(t, 1, ok, "of requests carrying one credential at once, one obtains a token (CRED-3): %v", statuses)
}

func botRefusesNotGarams(t *testing.T) {
	namespace, name := garamBot(t)
	run(t, "bot", namespace, "local", "--owner", "cluster")
	for label, credential := range map[string]string{
		"declared at the place":   credential(t, namespace, "local"),
		"of another namespace":    credential(t, namespace+"-other", name),
		"the place does not hold": credential(t, namespace, "nobody"),
	} {
		refused := newProcess(t).obtain(t, credential)
		assert.Equal(t, http.StatusForbidden, refused.status, label)
		assert.Equal(t, v1alpha1.CodeUnknownBot, refused.code, label)
	}
	// Control: the garam-owned bot of the same namespace and name obtains a token.
	assert.Equal(t, http.StatusOK, newProcess(t).obtain(t, credential(t, namespace, name)).status)
}

func secretsNoTokenInARefusal(t *testing.T) {
	namespace, name := garamBot(t)
	credential := credential(t, namespace, name)
	p := newProcess(t)
	answered := p.obtain(t, credential)
	require.Equal(t, http.StatusOK, answered.status)
	var token v1alpha1.Token
	require.NoError(t, json.Unmarshal([]byte(answered.raw), &token))

	refused := p.obtain(t, credential)

	assert.NotContains(t, refused.raw, token.AccessToken, "a refusal carries no token (SEC-1)")
	assert.NotContains(t, refused.raw, credential, "a refusal carries no credential (SEC-1)")
}

// answered is one call's answer.
type answered struct {
	status int
	code   v1alpha1.Code
	nonce  string
	body   v1alpha1.ErrorBody
	raw    string
	// retryAfter is the answer's Retry-After.
	retryAfter string
}

func call(t *testing.T, credential, proof string) answered {
	t.Helper()
	if credential == "" {
		return callWith(t, "", proof)
	}
	return callWith(t, "Bearer "+credential, proof)
}

// callWith makes the one call with authorization as its Authorization header, where it is not empty.
func callWith(t *testing.T, authorization, proof string) answered {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, baseURL+v1alpha1.TokenPath, nil)
	require.NoError(t, err)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if proof != "" {
		request.Header.Set(v1alpha1.HeaderProof, proof)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	read, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	a := answered{status: response.StatusCode, nonce: response.Header.Get(v1alpha1.HeaderNonce), raw: string(read),
		retryAfter: response.Header.Get("Retry-After")}
	if a.status != http.StatusOK {
		require.NoError(t, json.Unmarshal(read, &a.body), "a refusal is ErrorBody (T-3): %s", read)
		a.code = a.body.Error
	}
	return a
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

func (p process) thumbprint(t *testing.T) string {
	t.Helper()
	sum, err := (&jose.JSONWebKey{Key: &p.key.PublicKey}).Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(sum)
}

func (p process) proof(t *testing.T, htu, nonce string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: p.key},
		(&jose.SignerOptions{EmbedJWK: true}).WithType("dpop+jwt"))
	require.NoError(t, err)
	claims := map[string]any{"htm": http.MethodPost, "htu": htu, "iat": time.Now().Unix(), "jti": rand.Text()}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

// asked is the htu and nonce a process signs its proof with.
type asked struct {
	htu, nonce string
}

// ask makes the calls a process makes before its proof can carry the nonce, and answers what to sign.
func (p process) ask(t *testing.T, credential string) asked {
	t.Helper()
	first := call(t, credential, "")
	require.Equal(t, v1alpha1.CodeUseDPoPNonce, first.code, first.raw)
	a := asked{htu: first.body.HTU, nonce: first.nonce}
	if a.nonce == "" {
		relayed := call(t, credential, p.proof(t, a.htu, ""))
		require.Equal(t, v1alpha1.CodeUseDPoPNonce, relayed.code, relayed.raw)
		a.nonce = relayed.nonce
	}
	return a
}

// obtain makes the calls a process makes for a token, and answers the last.
func (p process) obtain(t *testing.T, credential string) answered {
	t.Helper()
	first := call(t, credential, "")
	if first.code != v1alpha1.CodeUseDPoPNonce {
		return first
	}
	nonce := first.nonce
	if nonce == "" {
		relayed := call(t, credential, p.proof(t, first.body.HTU, ""))
		if relayed.code != v1alpha1.CodeUseDPoPNonce {
			return relayed
		}
		nonce = relayed.nonce
	}
	return call(t, credential, p.proof(t, first.body.HTU, nonce))
}

// run runs one fixture subcommand, and answers what it printed.
func run(t *testing.T, args ...string) []byte {
	t.Helper()
	command := exec.Command(fixture[0], append(append([]string(nil), fixture[1:]...), args...)...) //nolint:gosec
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	require.NoError(t, err, "fixture %v: %s", args, stderr.String())
	return out
}

func runCredential(t *testing.T, args ...string) string {
	t.Helper()
	var answer struct {
		Credential string `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(run(t, append([]string{"credential"}, args...)...), &answer))
	require.NotEmpty(t, answer.Credential)
	return answer.Credential
}

func credential(t *testing.T, namespace, name string) string {
	t.Helper()
	return runCredential(t, namespace, name)
}

func binding(t *testing.T, token string) string {
	t.Helper()
	var answer struct {
		JKT string `json:"jkt"`
	}
	require.NoError(t, json.Unmarshal(run(t, "binding", token), &answer))
	return answer.JKT
}
