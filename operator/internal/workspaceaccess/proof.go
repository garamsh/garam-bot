package workspaceaccess

import (
	"crypto"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// maxProof bounds a proof read, as a DPoP proof of ES256 is well under a KiB.
const maxProof = 4 << 10

// proofs verifies each request's DPoP proof and remembers the jti of each it took (PROOF-1).
type proofs struct {
	// origins are the scheme://host[:port] a process reaches the server at, the only ones a htu names.
	origins map[string]bool
	now     func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time
}

func newProofs(origins []string, now func() time.Time) (*proofs, error) {
	held := make(map[string]bool, len(origins))
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("the origin %q is not https://host[:port]", origin)
		}
		held[u.Scheme+"://"+u.Host] = true
	}
	if len(held) == 0 {
		return nil, errors.New("no origin is configured")
	}
	return &proofs{origins: held, now: now, seen: map[string]time.Time{}}, nil
}

// verify answers the thumbprint of the key of the request's one proof, for its method, URL and
// credential, or errProof.
func (p *proofs) verify(r *http.Request, credential string) (string, error) {
	thumbprint, err := p.check(r, credential)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errProof, err)
	}
	return thumbprint, nil
}

func (p *proofs) check(r *http.Request, credential string) (string, error) {
	held := r.Header.Values(access.HeaderProof)
	if len(held) != 1 || len(held[0]) > maxProof {
		return "", errors.New("not one proof")
	}
	signed, err := jose.ParseSignedCompact(held[0], []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(signed.Signatures) != 1 {
		return "", errors.New("not one ES256 signature")
	}
	header := signed.Signatures[0].Header
	if header.JSONWebKey == nil || !header.JSONWebKey.IsPublic() || header.ExtraHeaders[jose.HeaderType] != "dpop+jwt" {
		return "", errors.New("no public jwk, or not a dpop+jwt")
	}
	payload, err := signed.Verify(header.JSONWebKey)
	if err != nil {
		return "", errors.New("it does not verify")
	}
	var claims struct {
		HTM string `json:"htm"`
		HTU string `json:"htu"`
		IAT int64  `json:"iat"`
		JTI string `json:"jti"`
		ATH string `json:"ath"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", errors.New("its claims are not JSON")
	}
	htu, err := url.Parse(claims.HTU)
	if err != nil || !p.origins[htu.Scheme+"://"+htu.Host] || htu.Path != r.URL.Path || htu.RawQuery != "" ||
		htu.Fragment != "" {
		return "", errors.New("it names another URL")
	}
	digest := sha256.Sum256([]byte(credential))
	now := p.now()
	switch {
	case claims.HTM != r.Method:
		return "", errors.New("it names another method")
	case claims.ATH != base64.RawURLEncoding.EncodeToString(digest[:]):
		return "", errors.New("it names another credential")
	case claims.JTI == "" || now.Sub(time.Unix(claims.IAT, 0)).Abs() > access.ProofWindow:
		return "", errors.New("it has no jti, or its iat is outside the window")
	}
	sum, err := header.JSONWebKey.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", errors.New("its key has no thumbprint")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if at, seen := p.seen[claims.JTI]; seen && now.Sub(at) <= access.ProofMemory {
		return "", errors.New("its jti was seen")
	}
	p.seen[claims.JTI] = now
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

// forget drops the jtis seen longer ago than a proof is remembered, on Run's tick rather than per
// request.
func (p *proofs) forget() {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for jti, at := range p.seen {
		if now.Sub(at) > access.ProofMemory {
			delete(p.seen, jti)
		}
	}
}
