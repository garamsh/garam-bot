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
