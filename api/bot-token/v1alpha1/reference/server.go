package reference

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	v1alpha1 "github.com/garamsh/garam-bot/api/bot-token/v1alpha1"
)

// The files serve and the fixture subcommands share in the directory.
const (
	// TrustFile holds the serving certificate, PEM, for a client to trust (T-1).
	TrustFile = "ca.pem"
	// URLFile holds the base URL the server is reached at.
	URLFile = "url"
	// CredentialKeyFile holds the key the place's credentials are signed with, readable by this user alone.
	CredentialKeyFile = "credential-key.pem"
	// BotsFile holds the bots the place runs, which the server reads at each request.
	BotsFile = "bots.json"
	// BindingsDir holds, for each token answered, the thumbprint of the key it is bound to.
	BindingsDir = "bindings"
)

const (
	// Audience is the audience the place's credentials name for the server.
	Audience = "bot-token-reference"
	// issuer is the iss of the place's credentials.
	issuer = "bot-token-reference"
	// TokenEndpoint is the path, below the base URL, of the token endpoint the reference stands in for
	// garam with. A proof names it as its htu.
	TokenEndpoint = "/oauth/token"
	// lifetime is how long a token answered is accepted for.
	lifetime = time.Hour
	// proofWindow is how far a proof's iat may be from the server's clock.
	proofWindow = time.Minute
)

// Owner is who changes a bot at the place: garam, which declared it, or the place itself.
type Owner string

const (
	OwnerGaram   Owner = "garam"
	OwnerCluster Owner = "cluster"
)

// Bots are the bots the place runs, keyed by "<namespace>/<name>".
type Bots map[string]Owner

// server answers interface 6 from memory and from the files of its directory. It stands in for the
// connector and for garam at once: it issues the place's credentials, holds its bots, and mints the
// tokens it answers.
type server struct {
	dir     string
	baseURL string
	keys    jose.JSONWebKeySet
	nonce   string

	mu     sync.Mutex
	spent  map[string]bool
	proofs map[string]time.Time
}

func newServer(dir, baseURL string, credentialKey *ecdsa.PublicKey) *server {
	return &server{
		dir: dir, baseURL: baseURL, nonce: rand.Text(),
		keys:  jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: credentialKey, KeyID: "reference", Algorithm: string(jose.ES256)}}},
		spent: map[string]bool{}, proofs: map[string]time.Time{},
	}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+v1alpha1.TokenPath, s.token)
	return mux
}

// token is the one call (CRED-2, CRED-3, BOT-1, NONCE-1, EXCH-1, TOK-1).
func (s *server) token(w http.ResponseWriter, r *http.Request) {
	credential, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	bot, err := s.verify(credential)
	if err != nil {
		refuse(w, http.StatusUnauthorized, v1alpha1.ErrorBody{Error: v1alpha1.CodeInvalidCredential})
		return
	}
	bots, err := readBots(s.dir)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, v1alpha1.ErrorBody{Error: v1alpha1.CodeTemporarilyUnavailable})
		return
	}
	if bots[bot] != OwnerGaram {
		refuse(w, http.StatusForbidden, v1alpha1.ErrorBody{Error: v1alpha1.CodeUnknownBot})
		return
	}
	digest := sha256.Sum256([]byte(credential))
	spentKey := hex.EncodeToString(digest[:])
	w.Header().Set(v1alpha1.HeaderNonce, s.nonce)
	proofs := r.Header.Values(v1alpha1.HeaderProof)
	if len(proofs) == 0 {
		s.askNonce(w)
		return
	}
	key, err := s.proofKey(proofs)
	var nonce nonceError
	switch {
	case errors.As(err, &nonce):
		s.askNonce(w)
		return
	case err != nil:
		refuse(w, http.StatusBadRequest, v1alpha1.ErrorBody{Error: v1alpha1.CodeInvalidDPoPProof})
		return
	}
	s.mu.Lock()
	if s.spent[spentKey] {
		s.mu.Unlock()
		refuse(w, http.StatusUnauthorized, v1alpha1.ErrorBody{Error: v1alpha1.CodeInvalidCredential})
		return
	}
	s.spent[spentKey] = true
	s.mu.Unlock()
	token := "reference_bt_" + rand.Text()
	if err := s.bind(token, key); err != nil {
		refuse(w, http.StatusServiceUnavailable, v1alpha1.ErrorBody{Error: v1alpha1.CodeTemporarilyUnavailable})
		return
	}
	writeJSON(w, http.StatusOK, v1alpha1.Token{
		AccessToken: token, TokenType: v1alpha1.TokenTypeDPoP, ExpiresIn: int64(lifetime / time.Second),
	})
}

func (s *server) askNonce(w http.ResponseWriter) {
	refuse(w, http.StatusBadRequest, v1alpha1.ErrorBody{Error: v1alpha1.CodeUseDPoPNonce, HTU: s.baseURL + TokenEndpoint})
}

// verify answers the bot a credential names, "<namespace>/<name>", where it is one the server signed
// for its audience and it has not expired.
func (s *server) verify(credential string) (string, error) {
	token, err := jwt.ParseSigned(credential, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return "", err
	}
	var claims jwt.Claims
	if err := token.Claims(s.keys, &claims); err != nil {
		return "", err
	}
	if err := claims.Validate(jwt.Expected{Issuer: issuer, AnyAudience: jwt.Audience{Audience}, Time: time.Now()}); err != nil {
		return "", err
	}
	if claims.Expiry == nil || !strings.Contains(claims.Subject, "/") {
		return "", errors.New("the credential names no bot")
	}
	return claims.Subject, nil
}

// nonceError is a proof that carries no nonce, or another than the server's.
type nonceError struct{}

func (nonceError) Error() string { return "the proof carries no current nonce" }

// proofKey verifies the one proof as garam's token endpoint does, and answers its key's thumbprint.
func (s *server) proofKey(proofs []string) (string, error) {
	if len(proofs) != 1 || len(proofs[0]) > 4096 {
		return "", errors.New("not one proof")
	}
	signed, err := jose.ParseSignedCompact(proofs[0], []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(signed.Signatures) != 1 {
		return "", errors.New("not one ES256 signature")
	}
	header := signed.Signatures[0].Header
	if header.JSONWebKey == nil || !header.JSONWebKey.IsPublic() || header.ExtraHeaders[jose.HeaderType] != "dpop+jwt" {
		return "", errors.New("no public jwk, or not a dpop+jwt")
	}
	payload, err := signed.Verify(header.JSONWebKey)
	if err != nil {
		return "", err
	}
	var claims struct {
		HTM   string `json:"htm"`
		HTU   string `json:"htu"`
		IAT   int64  `json:"iat"`
		JTI   string `json:"jti"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	iat := time.Unix(claims.IAT, 0)
	if claims.HTM != http.MethodPost || claims.HTU != s.baseURL+TokenEndpoint || claims.JTI == "" ||
		time.Since(iat).Abs() > proofWindow {
		return "", errors.New("the proof names another method or URL, or is outside its minute")
	}
	if claims.Nonce != s.nonce {
		return "", nonceError{}
	}
	sum, err := header.JSONWebKey.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.proofs[claims.JTI]; seen {
		return "", errors.New("the proof was spent")
	}
	s.proofs[claims.JTI] = iat
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

// bind records the thumbprint of the key token is bound to, for the fixture's binding subcommand.
func (s *server) bind(token, thumbprint string) error {
	dir := filepath.Join(s.dir, BindingsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, bindingName(token)), []byte(thumbprint), 0o600)
}

func bindingName(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func readBots(dir string) (Bots, error) {
	read, err := os.ReadFile(filepath.Join(dir, BotsFile))
	if errors.Is(err, os.ErrNotExist) {
		return Bots{}, nil
	}
	if err != nil {
		return nil, err
	}
	bots := Bots{}
	if err := json.Unmarshal(read, &bots); err != nil {
		return nil, fmt.Errorf("%s: %w", BotsFile, err)
	}
	return bots, nil
}

func refuse(w http.ResponseWriter, status int, body v1alpha1.ErrorBody) {
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
