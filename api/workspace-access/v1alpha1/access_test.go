package v1alpha1_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

func TestCheckTokenPath_FILE1(t *testing.T) {
	for _, ok := range []string{"/run/garam-bot/agent/token", "/run/garam-bot/model-token", "/run/garam-bot/a"} {
		assert.NoError(t, v1alpha1.CheckTokenPath(ok), ok)
	}
	for _, bad := range []string{
		"/run/garam-bot/", "/run/garam-bot", "/run/garam-bot-x/token", "/run/garam-bot/../etc/passwd",
		"/run/garam-bot/a/", "/run/garam-bot//a", "/run/garam-bot/./a", "run/garam-bot/a", "/etc/passwd", "",
	} {
		assert.Error(t, v1alpha1.CheckTokenPath(bad), bad)
	}
}

func root(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gateway root"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestCheckTrust_TRUST1(t *testing.T) {
	one, next := root(t), root(t)
	block, _ := pem.Decode([]byte(one))
	// A root under another type, which a client's pool of roots skips.
	otherType := string(pem.EncodeToMemory(&pem.Block{Type: "TRUSTED CERTIFICATE", Bytes: block.Bytes}))
	for name, ok := range map[string]string{
		"none":                    "",
		"one root":                one,
		"a root and the next":     one + next,
		"roots with space around": "\n" + one + "\n\n" + next + "  \n",
	} {
		assert.NoError(t, v1alpha1.CheckTrust(ok), name)
	}
	for name, bad := range map[string]string{
		"not PEM":                   "a root",
		"text after a root":         one + "and more",
		"a root of another type":    otherType,
		"a key, not a certificate":  "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		"a certificate of no X.509": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
		"above its bound":           strings.Repeat(one, v1alpha1.MaxTrustBytes/len(one)+1),
	} {
		assert.Error(t, v1alpha1.CheckTrust(bad), name)
	}
}
