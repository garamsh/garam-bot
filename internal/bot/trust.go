package bot

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"os"
)

// NewKey makes the process's ES256 key, which both interface 6's token and interface 7's proofs are
// bound to (KEY-1). It is held in memory alone, never written or sent.
func NewKey() (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("make the process's key: %w", err)
	}
	return key, nil
}

// placeTrust is the connector's trust as last read from its file (T-1 of interfaces 6 and 7).
type placeTrust struct {
	file  string
	pem   []byte
	roots *x509.CertPool
}

// read reads the file again, and answers whether its certificates changed since the last read, so a
// client is built anew to trust them alone.
func (t *placeTrust) read() (bool, error) {
	data, err := os.ReadFile(t.file)
	if err != nil {
		return false, fmt.Errorf("read the connector's trust: %w", err)
	}
	if t.roots != nil && bytes.Equal(data, t.pem) {
		return false, nil
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return false, fmt.Errorf("%s holds no PEM certificate", t.file)
	}
	t.pem, t.roots = data, roots
	return true, nil
}
