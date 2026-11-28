package bot_test

import (
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// htu is the token endpoint URL the stand-in connector asks proofs to name.
const htu = "https://garam.test/oauth/token"

// connector stands in for the place's connector (interface 6): it asks for its nonce, verifies each
// proof by the key it carries, and answers a token bound to that key.
type connector struct {
	server *httptest.Server
	// trust and credential are the files a bot's process reads them from.
	trust, credential string

	mu sync.Mutex
	// expiresIn is the lifetime answered, and refuse the status answered in place of a token where it
	// is not zero.
	expiresIn int
	refuse    int
	// credentials are the credentials presented with a proof, proofs the proofs' raw protected headers,
	// and bound each token answered with the RFC 7638 thumbprint of its key.
	credentials []string
	headers     []string
	bound       map[string]string
}

func newConnector(t *testing.T) *connector {
	t.Helper()
	c := &connector{expiresIn: 3600, bound: map[string]string{}}
	c.server = httptest.NewTLSServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.server.Close)
	directory := t.TempDir()
	c.trust, c.credential = filepath.Join(directory, "ca.crt"), filepath.Join(directory, "token")
	writeFile(t, c.trust, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.server.Certificate().Raw})))
	writeFile(t, c.credential, "credential-1\n")
	return c
}

func (c *connector) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/bot-token/v1alpha1/token" {
		answer(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	credential, ok := cutBearer(r.Header.Get("Authorization"))
	if !ok {
		answer(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credential"})
		return
	}
	proof := r.Header.Get("DPoP")
	if proof == "" {
		w.Header().Set("DPoP-Nonce", "nonce-1")
		answer(w, http.StatusBadRequest, map[string]string{"error": "use_dpop_nonce", "htu": htu})
		return
	}
	key, header, claims, ok := verify(proof)
	if !ok || claims["htm"] != http.MethodPost || claims["htu"] != htu || claims["jti"] == "" {
		answer(w, http.StatusBadRequest, map[string]string{"error": "invalid_dpop_proof"})
		return
	}
	if claims["nonce"] != "nonce-1" {
		w.Header().Set("DPoP-Nonce", "nonce-1")
		answer(w, http.StatusBadRequest, map[string]string{"error": "use_dpop_nonce", "htu": htu})
		return
	}
	c.credentials = append(c.credentials, credential)
	c.headers = append(c.headers, header)
	if c.refuse != 0 {
		answer(w, c.refuse, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	token := "garam_bt_" + rand.Text()
	sum, _ := key.Thumbprint(crypto.SHA256)
	c.bound[token] = base64.RawURLEncoding.EncodeToString(sum)
	answer(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "DPoP", "expires_in": c.expiresIn})
}

func (c *connector) set(expiresIn, refuse int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expiresIn, c.refuse = expiresIn, refuse
}

// boundTo answers the thumbprint of the key token is bound to, empty for a token never answered.
func (c *connector) boundTo(token string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bound[token]
}

func (c *connector) presented() (credentials, headers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.credentials...), append([]string(nil), c.headers...)
}

func cutBearer(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return "", false
	}
	return header[len(prefix):], true
}

// verify verifies proof by the key its header carries, as garam does, and answers that key, the raw
// protected header and the claims.
func verify(proof string) (*jose.JSONWebKey, string, map[string]any, bool) {
	signed, err := jose.ParseSignedCompact(proof, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(signed.Signatures) != 1 {
		return nil, "", nil, false
	}
	signature := signed.Signatures[0]
	if signature.Header.JSONWebKey == nil || signature.Header.ExtraHeaders[jose.HeaderType] != "dpop+jwt" {
		return nil, "", nil, false
	}
	payload, err := signed.Verify(signature.Header.JSONWebKey)
	if err != nil {
		return nil, "", nil, false
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil, "", nil, false
	}
	header, err := base64.RawURLEncoding.DecodeString(strings.SplitN(proof, ".", 2)[0])
	if err != nil {
		return nil, "", nil, false
	}
	return signature.Header.JSONWebKey, string(header), claims, true
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
