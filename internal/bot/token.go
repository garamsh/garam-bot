package bot

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// The protocol's names (api/bot-token/v1alpha1/README.md).
const (
	tokenPath        = "/bot-token/v1alpha1/token"
	headerProof      = "DPoP"
	headerNonce      = "DPoP-Nonce"
	headerRetryAfter = "Retry-After"
	codeUseNonce     = "use_dpop_nonce"
	// maxAnswer bounds what is read of the connector's answer.
	maxAnswer = 64 << 10
)

// The protocol's waits: a failed request waits from firstWait, doubling to lastWait (REN-2, REN-3).
const (
	firstWait = time.Second
	lastWait  = time.Minute
	// nonceTries is how many use_dpop_nonce answers one attempt signs again for (NONCE-1).
	nonceTries = 2
)

// errNoPlace is a connector configured without its trust file or any credential file.
var errNoPlace = errors.New("--connector-url needs --connector-trust-file, and --credential-file or " +
	"--workspace-credential-file")

// TokenSource holds the bot's garam token, which it obtains from the place's connector, bound to a key
// it makes and holds in memory alone, and obtains again before it expires (interface 6). The token's
// value never reaches a log line, an error or the status file.
type TokenSource struct {
	url            string
	credentialFile string
	now            func() time.Time
	key            *ecdsa.PrivateKey

	// trust is the connector's trust as last read, and client the client verifying against it.
	trust  placeTrust
	client *http.Client
	// htu and nonce are what the connector last answered to sign a proof with.
	htu, nonce string

	// renew wakes Run to obtain a token now, the one held having been refused.
	renew chan struct{}

	mu         sync.Mutex
	token      string
	obtainedAt time.Time
	expiresAt  time.Time
	stale      bool
}

// NewTokenSource returns a source obtaining the token from the connector at url, bound to the process's
// key (KEY-1), trusting trustFile's certificates and presenting credentialFile's credential.
func NewTokenSource(url, trustFile, credentialFile string, key *ecdsa.PrivateKey, now func() time.Time) (
	*TokenSource, error,
) {
	if trustFile == "" || credentialFile == "" {
		return nil, errNoPlace
	}
	return &TokenSource{
		url: strings.TrimRight(url, "/"), trust: placeTrust{file: trustFile}, credentialFile: credentialFile,
		now: now, key: key, renew: make(chan struct{}, 1),
	}, nil
}

// Token answers the token last obtained while it lives, and empty before the first, once it expired, or
// once garam refused it.
func (s *TokenSource) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.now().Before(s.expiresAt) {
		return ""
	}
	return s.token
}

// Refused is told that garam answered 401 to token. Where token is the one held, it is dropped and a new
// one is obtained at once (REN-1); a token held since is kept.
func (s *TokenSource) Refused(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" || token != s.token {
		return
	}
	s.token = ""
	select {
	case s.renew <- struct{}{}:
	default:
	}
}

// Status answers when the token was obtained and expires, and whether the latest attempt failed.
func (s *TokenSource) Status() TokenStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return TokenStatus{ObtainedAt: s.obtainedAt, ExpiresAt: s.expiresAt, Stale: s.stale}
}

// Run obtains the token, then obtains it again once two thirds of its life have passed, or at once where
// garam refused it, until ctx ends.
// A failed attempt keeps the token held while it lives, marks it stale and is retried (REN-1 to REN-3).
// report receives each attempt's failure.
func (s *TokenSource) Run(ctx context.Context, report func(error)) {
	wait := firstWait
	for {
		next, err := s.obtain(ctx)
		s.mu.Lock()
		s.stale = err != nil
		s.mu.Unlock()
		if err == nil {
			wait = firstWait
		} else {
			report(err)
			next = wait
			var asked waitError
			if errors.As(err, &asked) && asked.after > next {
				next = min(asked.after, lastWait)
			}
			wait = min(wait*2, lastWait)
		}
		timer := time.NewTimer(next)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-s.renew:
			timer.Stop()
		}
	}
}

// obtain makes one attempt, and answers how long to wait before the next.
func (s *TokenSource) obtain(ctx context.Context) (time.Duration, error) {
	if err := s.readTrust(); err != nil {
		return 0, err
	}
	for range nonceTries + 1 {
		credential, err := os.ReadFile(s.credentialFile)
		if err != nil {
			// The path error names the file and the failure, never its content.
			return 0, fmt.Errorf("read the place credential: %w", err)
		}
		proof := ""
		if s.htu != "" {
			if proof, err = s.proof(); err != nil {
				return 0, err
			}
		}
		answer, err := s.call(ctx, string(bytes.TrimSpace(credential)), proof)
		if err != nil {
			return 0, err
		}
		if answer.refusal == nil {
			obtained := s.now()
			lifetime := time.Duration(answer.expiresIn) * time.Second
			s.mu.Lock()
			s.token, s.obtainedAt, s.expiresAt = answer.token, obtained, obtained.Add(lifetime)
			s.mu.Unlock()
			return lifetime * 2 / 3, nil
		}
		if answer.refusal.Code != codeUseNonce || answer.refusal.HTU == "" {
			return 0, answer.refusal
		}
		s.htu, s.nonce = answer.refusal.HTU, answer.nonce
	}
	return 0, errors.New("the connector asked for a nonce once more than it is answered")
}

// readTrust reads the connector's trust again, and trusts it alone from now on (T-1).
func (s *TokenSource) readTrust() error {
	changed, err := s.trust.read()
	if err != nil || !changed {
		return err
	}
	s.client = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: s.trust.roots, MinVersion: tls.VersionTLS13},
		},
		// A redirect would carry the credential wherever it pointed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return nil
}

// proof is a DPoP proof for the connector's htu and nonce, signed with the process's key (PROOF-1).
func (s *TokenSource) proof() (string, error) {
	claims := map[string]any{"htm": http.MethodPost, "htu": s.htu}
	if s.nonce != "" {
		claims["nonce"] = s.nonce
	}
	return s.sign(claims)
}

// Proof is a DPoP proof of a request of method to url presenting token, signed with the process's key
// (KEY-1): the one token is bound to, with ath its hash (RFC 9449 §4.2).
func (s *TokenSource) Proof(method, url, token string) (string, error) {
	digest := sha256.Sum256([]byte(token))
	return s.sign(map[string]any{"htm": method, "htu": url, "ath": base64.RawURLEncoding.EncodeToString(digest[:])})
}

// sign signs a proof of claims, with its iat and a fresh jti.
func (s *TokenSource) sign(claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.key},
		(&jose.SignerOptions{EmbedJWK: true}).WithType("dpop+jwt"))
	if err != nil {
		return "", fmt.Errorf("sign the proof: %w", err)
	}
	claims["iat"], claims["jti"] = s.now().Unix(), rand.Text()
	proof, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("sign the proof: %w", err)
	}
	return proof, nil
}

// answer is what one call answered: the token, or the refusal and the nonce beside it.
type answer struct {
	token     string
	expiresIn int64
	refusal   *refusal
	nonce     string
}

// refusal is the connector's refusal (T-3). Its message names the code and nothing of the request.
type refusal struct {
	Status int    `json:"-"`
	Code   string `json:"error"`
	HTU    string `json:"htu"`
}

func (r *refusal) Error() string {
	return fmt.Sprintf("the connector refused the token: %d %s", r.Status, r.Code)
}

// waitError is a refusal that names how long to wait (REN-3).
type waitError struct {
	refusal *refusal
	after   time.Duration
}

func (e waitError) Error() string { return e.refusal.Error() }

func (s *TokenSource) call(ctx context.Context, credential, proof string) (answer, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+tokenPath, nil)
	if err != nil {
		return answer{}, fmt.Errorf("ask the connector: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	if proof != "" {
		request.Header.Set(headerProof, proof)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return answer{}, fmt.Errorf("ask the connector: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	if err != nil {
		return answer{}, fmt.Errorf("read the connector's answer: %w", err)
	}
	if response.StatusCode == http.StatusOK {
		var token struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" || token.TokenType != "DPoP" ||
			token.ExpiresIn <= 0 {
			return answer{}, errors.New("the connector answered no DPoP token")
		}
		return answer{token: token.AccessToken, expiresIn: token.ExpiresIn}, nil
	}
	refused := &refusal{Status: response.StatusCode}
	_ = json.Unmarshal(body, refused)
	if seconds, err := strconv.Atoi(response.Header.Get(headerRetryAfter)); err == nil && seconds > 0 {
		return answer{}, waitError{refusal: refused, after: time.Duration(seconds) * time.Second}
	}
	return answer{refusal: refused, nonce: response.Header.Get(headerNonce)}, nil
}
