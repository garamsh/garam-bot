package bot_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/internal/bot"
)

// eventually is how long a test waits for the source, and tick how often it looks.
const (
	eventually = 10 * time.Second
	tick       = 10 * time.Millisecond
)

// reports collects what Run reports of each failed attempt.
type reports struct {
	mu     sync.Mutex
	errors []error
}

func (r *reports) report(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, err)
}

func (r *reports) read() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errors...)
}

// running is a TokenSource of c, run until the test ends.
func running(t *testing.T, c *connector) (*bot.TokenSource, *reports) {
	t.Helper()
	source, err := bot.NewTokenSource(c.server.URL, c.trust, c.credential, time.Now)
	require.NoError(t, err)
	r := &reports{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		source.Run(ctx, r.report)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return source, r
}

func TestTokenSource_ObtainsATokenBoundToAKeyOfItsOwn(t *testing.T) {
	c := newConnector(t)
	source, _ := running(t, c)
	other, _ := running(t, c)

	require.Eventually(t, func() bool { return source.Token() != "" && other.Token() != "" }, eventually, tick)
	credentials, headers := c.presented()
	assert.Equal(t, []string{"credential-1", "credential-1"}, credentials, "the credential is presented as the file holds it")
	assert.NotEmpty(t, c.boundTo(source.Token()), "the token is the connector's, bound to the proof's key")
	assert.NotEqual(t, c.boundTo(source.Token()), c.boundTo(other.Token()), "each process makes a key of its own")
	for _, header := range headers {
		var protected struct {
			JWK map[string]any `json:"jwk"`
		}
		require.NoError(t, json.Unmarshal([]byte(header), &protected))
		require.Equal(t, "EC", protected.JWK["kty"], "the proof carries the public key")
		assert.NotContains(t, protected.JWK, "d", "the private key never leaves the process")
	}
	status := source.Status()
	assert.False(t, status.Stale)
	assert.WithinDuration(t, status.ObtainedAt.Add(time.Hour), status.ExpiresAt, time.Second)
}

func TestTokenSource_ObtainsANewTokenBeforeTwoThirdsOfItsLifeWithTheCredentialReadAgain(t *testing.T) {
	c := newConnector(t)
	c.set(3, 0)
	source, _ := running(t, c)
	require.Eventually(t, func() bool { return source.Token() != "" }, eventually, tick)
	first := source.Token()
	obtained := time.Now()
	writeFile(t, c.credential, "credential-2")

	require.Eventually(t, func() bool { return source.Token() != first }, eventually, tick)

	assert.Less(t, time.Since(obtained), 3*time.Second, "the token is replaced before it expires")
	assert.Equal(t, c.boundTo(first), c.boundTo(source.Token()), "the process keeps its key")
	credentials, _ := c.presented()
	assert.Equal(t, "credential-2", credentials[len(credentials)-1], "the credential is read again for each request")
}

func TestTokenSource_KeepsTheTokenAndMarksItStaleWhileTheConnectorRefuses(t *testing.T) {
	c := newConnector(t)
	c.set(3, 0)
	source, r := running(t, c)
	require.Eventually(t, func() bool { return source.Token() != "" }, eventually, tick)
	held := source.Token()
	c.set(3, http.StatusUnauthorized)

	require.Eventually(t, func() bool { return source.Status().Stale }, eventually, tick)
	assert.Equal(t, held, source.Token(), "the token held is kept")

	c.set(3, 0)
	require.Eventually(t, func() bool { return !source.Status().Stale && source.Token() != held }, eventually, tick)
	for _, err := range r.read() {
		assert.NotContains(t, err.Error(), "credential-1", "a failure carries no credential")
		assert.NotContains(t, err.Error(), held, "a failure carries no token")
	}
}

func TestTokenSource_AnswersNoTokenPastItsExpiry(t *testing.T) {
	c := newConnector(t)
	c.set(2, 0)
	source, _ := running(t, c)
	require.Eventually(t, func() bool { return source.Token() != "" }, eventually, tick)
	c.set(2, http.StatusServiceUnavailable)
	expiresAt := source.Status().ExpiresAt

	require.Eventually(t, func() bool { return source.Token() == "" }, eventually, tick,
		"the token is not answered once it expired, though no new one was obtained")
	assert.False(t, time.Now().Before(expiresAt))
	assert.True(t, source.Status().Stale)
}

func TestTokenSource_ObtainsANewTokenAtOnceWhenGaramRefusesTheOneHeld(t *testing.T) {
	c := newConnector(t)
	source, _ := running(t, c)
	require.Eventually(t, func() bool { return source.Token() != "" }, eventually, tick)
	held := source.Token()
	credentials, _ := c.presented()

	source.Refused("garam_bt_another")
	require.Equal(t, held, source.Token(), "a token not held refuses nothing")
	source.Refused(held)

	assert.NotEqual(t, held, source.Token(), "the refused token is not answered")
	require.Eventually(t, func() bool { return source.Token() != "" }, 2*time.Second, tick,
		"a new token is obtained at once, not at two thirds of an hour")
	assert.NotEqual(t, held, source.Token())
	again, _ := c.presented()
	assert.Greater(t, len(again), len(credentials))
}

func TestTokenSource_TrustsTheTrustFileAloneAndReadsItAgain(t *testing.T) {
	c := newConnector(t)
	right, err := os.ReadFile(c.trust)
	require.NoError(t, err)
	writeFile(t, c.trust, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: anotherCertificate(t)})))
	source, r := running(t, c)

	require.Eventually(t, func() bool { return len(r.read()) > 0 }, eventually, tick)
	assert.Empty(t, source.Token(), "a connector the trust file does not name is not trusted")
	assert.ErrorContains(t, r.read()[0], "certificate")

	writeFile(t, c.trust, string(right))
	require.Eventually(t, func() bool { return source.Token() != "" }, eventually, tick,
		"the trust file is read again, and the connector it names is trusted")
}

// anotherCertificate is a certificate for 127.0.0.1 under a key of its own, unlike every httptest
// server's, which share one.
func anotherCertificate(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return der
}

func TestNewTokenSource_RefusesAConnectorWithoutItsFiles(t *testing.T) {
	_, err := bot.NewTokenSource("https://connector.test", "", "/token", time.Now)
	assert.Error(t, err)
	_, err = bot.NewTokenSource("https://connector.test", "/ca.crt", "", time.Now)
	assert.Error(t, err)
	// Control: both files named.
	_, err = bot.NewTokenSource("https://connector.test", "/ca.crt", "/token", time.Now)
	assert.NoError(t, err)
}
