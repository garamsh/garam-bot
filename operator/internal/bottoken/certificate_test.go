package bottoken_test

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/operator/internal/bottoken"
)

func TestServe_IsTrustedByThePublishedBundleAlone(t *testing.T) {
	s := start(t)
	other := start(t)

	answered := s.call(t, s.api.credentialFor(t, watched, mira), "")
	assert.Equal(t, useNonce, answered.code(), "a client trusting the published bundle is answered")

	s.client = other.client
	request, err := http.NewRequest(http.MethodPost, s.url, nil)
	require.NoError(t, err)
	_, err = s.client.Do(request)
	assert.ErrorContains(t, err, "certificate", "a client trusting another server's bundle is refused")
}

func TestServe_PublishesTheNextCertificateBeforeItServesIt(t *testing.T) {
	const validity, propagation = 3 * time.Second, 1 * time.Second
	s := start(t, func(c *bottoken.Config) { c.Validity, c.Propagation = validity, propagation })
	first := s.trust.published()
	require.Len(t, first, 1)
	current := certificatesOf(t, first[0])
	require.Len(t, current, 1)
	assert.Equal(t, current[0], s.served(t))

	require.Eventually(t, func() bool { return len(s.trust.published()) == 2 }, validity, 20*time.Millisecond)
	published := certificatesOf(t, s.trust.published()[1])
	require.Len(t, published, 2, "the next certificate is published beside the one served")
	next := published[0]
	assert.Equal(t, current[0], published[1])
	assert.Equal(t, current[0], s.served(t), "the next certificate is not served before it propagated")

	assert.Eventually(t, func() bool { return bytes.Equal(s.served(t), next) }, 2*propagation, 20*time.Millisecond,
		"the next certificate is served once it propagated")
}

func TestServe_KeepsServingAndPublishesAgainAfterAFailedPublish(t *testing.T) {
	// Renewal is due at 4 s; the one failed publish is made again 1 s later, before the current
	// certificate expires at 6 s.
	const validity, propagation = 6 * time.Second, 500 * time.Millisecond
	s := start(t, func(c *bottoken.Config) { c.Validity, c.Propagation = validity, propagation })
	current := certificatesOf(t, s.trust.published()[0])[0]
	s.trust.fail(1)

	require.Eventually(t, func() bool { return s.trust.failures() == 1 }, validity, 20*time.Millisecond)
	assert.Equal(t, current, s.served(t), "the current certificate is served while the next is not published")
	assert.Equal(t, useNonce, s.call(t, s.api.credentialFor(t, watched, mira), "").code(), "the server still answers")

	require.Eventually(t, func() bool { return len(s.trust.published()) == 2 }, 2*time.Second, 20*time.Millisecond,
		"the next certificate is published again after a back-off")
	next := certificatesOf(t, s.trust.published()[1])[0]
	assert.Eventually(t, func() bool { return bytes.Equal(s.served(t), next) }, 2*time.Second, 20*time.Millisecond)
}

func TestServe_PublishesTheFirstCertificateAgainBeforeServing(t *testing.T) {
	s := start(t, func(c *bottoken.Config) {
		held, ok := c.Trust.(*trust)
		require.True(t, ok)
		held.fail(1)
	})
	require.Equal(t, 1, s.trust.failures())

	assert.Equal(t, useNonce, s.call(t, s.api.credentialFor(t, watched, mira), "").code(),
		"the server serves once its certificate is published, after a failed publish")
}

// served is the DER of the certificate the server answers a handshake with.
func (s *server) served(t *testing.T) []byte {
	t.Helper()
	address, err := url.Parse(s.url)
	require.NoError(t, err)
	// The certificate is read, not trusted, so the handshake verifies nothing.
	conn, err := tls.Dial("tcp", address.Host, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) //nolint:gosec
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	return conn.ConnectionState().PeerCertificates[0].Raw
}

// certificatesOf is the DER of each certificate in bundle, in order.
func certificatesOf(t *testing.T, bundle []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for {
		var block *pem.Block
		block, bundle = pem.Decode(bundle)
		if block == nil {
			return out
		}
		require.Equal(t, "CERTIFICATE", block.Type)
		out = append(out, block.Bytes)
	}
}
