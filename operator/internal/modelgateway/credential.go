package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/sync/singleflight"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

const (
	// leeway is the clock skew a credential's iat and nbf are read with, within ISS-1's bound. Its exp
	// is read with none.
	leeway = gateway.CredentialLeeway
	// keysFresh is how long the API server's keys are used before they are read again, and keysRetry
	// how soon after a read a credential signed by a key not held reads them again.
	keysFresh = 5 * time.Minute
	keysRetry = 10 * time.Second
	// maxCredential bounds the credential read, as a projected ServiceAccount token is a few KiB.
	maxCredential = 16 << 10
)

// serviceAccountPrefix begins the sub of every ServiceAccount token, system:serviceaccount:<ns>:<name>.
const serviceAccountPrefix = "system:serviceaccount:"

var (
	// errCredential is an issuer's credential refused (ISS-1).
	errCredential = errors.New("the credential is refused")
	// errKeysUnread is the API server's keys not read, which refuses no credential: the gateway is
	// unavailable until it reads them.
	errKeysUnread = errors.New("the API server's keys could not be read")
)

// credentials verifies an issuer's credential: a projected ServiceAccount token of gateway.Audience,
// signed by the API server.
type credentials struct {
	keys Keys
	now  func() time.Time

	reads singleflight.Group

	mu     sync.Mutex
	issuer Issuer
	readAt time.Time
}

// verify answers the ServiceAccount raw names, as <namespace>/<name>, or errCredential where it is
// missing, does not verify against the API server's keys, has expired, names another audience, lives
// longer than an hour, or carries no iat (ISS-1).
func (c *credentials) verify(ctx context.Context, raw string) (string, error) {
	if raw == "" || len(raw) > maxCredential {
		return "", fmt.Errorf("%w: it is missing or too long", errCredential)
	}
	name, err := c.check(ctx, raw)
	if err != nil && !errors.Is(err, errKeysUnread) {
		return "", fmt.Errorf("%w: %w", errCredential, err)
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

func (c *credentials) check(ctx context.Context, raw string) (string, error) {
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return "", errors.New("not a signed JWT")
	}
	if len(token.Headers) != 1 {
		return "", errors.New("not one signature")
	}
	issuer, err := c.issuerFor(ctx, token.Headers[0].KeyID)
	if err != nil {
		return "", err
	}
	var claims jwt.Claims
	if err := token.Claims(issuer.Keys, &claims); err != nil {
		return "", errors.New("its signature does not verify")
	}
	now := c.now()
	expected := jwt.Expected{Issuer: issuer.URL, AnyAudience: jwt.Audience{gateway.Audience}, Time: now}
	if err := claims.ValidateWithLeeway(expected, leeway); err != nil {
		return "", err
	}
	switch {
	case claims.Expiry == nil || claims.IssuedAt == nil:
		return "", errors.New("it carries no exp or no iat")
	case !now.Before(claims.Expiry.Time()):
		return "", errors.New("it has expired")
	case claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > gateway.MaxTokenLife:
		return "", fmt.Errorf("it lives longer than %v", gateway.MaxTokenLife)
	}
	namespace, name, ok := strings.Cut(strings.TrimPrefix(claims.Subject, serviceAccountPrefix), ":")
	if !strings.HasPrefix(claims.Subject, serviceAccountPrefix) || !ok || namespace == "" || name == "" ||
		strings.Contains(name, ":") {
		return "", errors.New("it names no ServiceAccount")
	}
	return namespace + "/" + name, nil
}

// issuerFor answers the issuer, read again where kid names no key held and the last read is old enough.
func (c *credentials) issuerFor(ctx context.Context, kid string) (Issuer, error) {
	c.mu.Lock()
	issuer, readAt := c.issuer, c.readAt
	c.mu.Unlock()
	age := c.now().Sub(readAt)
	held := len(issuer.Keys.Key(kid)) > 0
	if !readAt.IsZero() && (held && age < keysFresh || !held && age < keysRetry) {
		if !held {
			return Issuer{}, errors.New("its key is not the API server's")
		}
		return issuer, nil
	}
	read, err, _ := c.reads.Do("keys", func() (any, error) {
		read, err := c.keys.Keys(ctx)
		if err != nil {
			return Issuer{}, err
		}
		c.mu.Lock()
		c.issuer, c.readAt = read, c.now()
		c.mu.Unlock()
		return read, nil
	})
	if err != nil {
		return Issuer{}, fmt.Errorf("%w: %w", errKeysUnread, err)
	}
	issuer = read.(Issuer)
	if len(issuer.Keys.Key(kid)) == 0 {
		return Issuer{}, errors.New("its key is not the API server's")
	}
	return issuer, nil
}
