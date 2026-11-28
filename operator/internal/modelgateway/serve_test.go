package modelgateway_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/api/model-gateway/v1alpha1/gatewayfake"

	"github.com/garamsh/garam-bot/operator/internal/certificate"
	"github.com/garamsh/garam-bot/operator/internal/modelgateway"
)

// serveTLS serves handler through modelgateway.Serve on a certificate for 127.0.0.1, and answers its
// base URL and a client that trusts it and speaks HTTP/2.
func serveTLS(t *testing.T, handler http.Handler) (string, *http.Client) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gateway"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	private, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0o600))
	certificates, err := certificate.NewReloader(certFile, keyFile)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = modelgateway.Serve(ctx, listener, handler, certificates.GetCertificate, slog.New(slog.DiscardHandler))
	}()
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true,
	}}
	return "https://" + listener.Addr().String(), client
}

func TestServe_ServesHTTP2AndBreaksOffAStreamThatBreaks(t *testing.T) {
	r, key := modelRig(t)
	base, client := serveTLS(t, r.gateway)
	token := r.modelToken(t, key, bot("team-a", "reviewer", "uid-a"))
	r.provider.Script(gatewayfake.Answer{}, gatewayfake.Answer{Stream: true, Break: true})
	call := func() *http.Response {
		request, err := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(messages))
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		require.NoError(t, err)
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	whole := call()
	assert.Equal(t, "HTTP/2.0", whole.Proto, "the gateway does not serve HTTP/2")
	_, err := io.ReadAll(whole.Body)
	require.NoError(t, err, "control: a whole answer ends cleanly over HTTP/2")
	broken := call()
	require.Equal(t, http.StatusOK, broken.StatusCode)
	_, err = io.ReadAll(broken.Body)
	assert.Error(t, err, "a stream the upstream broke ends cleanly over HTTP/2 (MOD-6)")
}
