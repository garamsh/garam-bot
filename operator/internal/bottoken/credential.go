package bottoken

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/sync/singleflight"
)

const (
	// leeway is the clock skew a credential's times are read with.
	leeway = 30 * time.Second
	// keysFresh is how long the API server's keys are used before they are read again, and keysRetry
	// how soon after a read a credential signed by a key not held reads them again.
	keysFresh = 5 * time.Minute
	keysRetry = 10 * time.Second
	// maxCredential bounds the credential read, as a projected ServiceAccount token is a few KiB.
	maxCredential = 16 << 10
	// maxLifetime bounds a credential's life, exp - iat. The manager projects one of 10 minutes; the
	// bound refuses one a person issued for longer by TokenRequest (CRED-2).
	maxLifetime = time.Hour
)

// serviceAccountPrefix begins the sub of every ServiceAccount token, system:serviceaccount:<ns>:<name>.
const serviceAccountPrefix = "system:serviceaccount:"

// credentials verifies the place credential, a projected ServiceAccount token, and spends each once
// (CRED-2, CRED-3).
type credentials struct {
	keys     Keys
	audience string
	now      func() time.Time
	// startedAt is when this process started. A credential issued before it is refused, as one spent
	// before a restart is not in spent (CRED-3).
	startedAt time.Time

	// reads makes the credentials that find the keys old or unknown at once wait on one read.
	reads singleflight.Group

	mu      sync.Mutex
	issuer  Issuer
	readAt  time.Time
	pending map[[sha256.Size]byte]bool
	// spent holds each spent credential's digest until it expires, when it can no longer verify.
	spent map[[sha256.Size]byte]time.Time
}

func newCredentials(keys Keys, audience string, now func() time.Time) *credentials {
	return &credentials{
		// A credential's iat is in whole seconds.
		keys: keys, audience: audience, now: now, startedAt: now().Truncate(time.Second),
		pending: map[[sha256.Size]byte]bool{}, spent: map[[sha256.Size]byte]time.Time{},
	}
}

// claim is a credential verified and held for one request: no other request takes it until release.
type claim struct {
	bot Bot
	// pod is the Pod the credential was issued to, which must run the Bot (CRED-2).
	pod    boundPod
	digest [sha256.Size]byte
	expiry time.Time
}

// boundPod is the Pod a projected ServiceAccount token names in its kubernetes.io claim.
type boundPod struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// kubernetesClaims is the kubernetes.io claim of a projected ServiceAccount token.
type kubernetesClaims struct {
	Kubernetes *struct {
		Namespace      string    `json:"namespace"`
		Pod            *boundPod `json:"pod"`
		ServiceAccount struct {
			Name string `json:"name"`
		} `json:"serviceaccount"`
	} `json:"kubernetes.io"`
}

// take verifies raw and holds it for one request. It answers errCredential for one that does not
// verify, was spent, or another request holds.
func (c *credentials) take(ctx context.Context, raw string) (claim, error) {
	if raw == "" || len(raw) > maxCredential {
		return claim{}, errCredential
	}
	verified, err := c.verify(ctx, raw)
	if err != nil {
		return claim{}, fmt.Errorf("%w: %w", errCredential, err)
	}
	digest := sha256.Sum256([]byte(raw))
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for spent, until := range c.spent {
		if now.After(until) {
			delete(c.spent, spent)
		}
	}
	if _, spent := c.spent[digest]; spent || c.pending[digest] {
		return claim{}, fmt.Errorf("%w: it was spent", errCredential)
	}
	c.pending[digest] = true
	verified.digest = digest
	return verified, nil
}

// release ends the request holding cl, spending the credential where the request obtained a token.
func (c *credentials) release(cl claim, obtained bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, cl.digest)
	if obtained {
		c.spent[cl.digest] = cl.expiry.Add(leeway)
	}
}

// verify answers the ServiceAccount raw names, the Bot of the same namespace and name, the Pod it was
// issued to, and when it expires.
func (c *credentials) verify(ctx context.Context, raw string) (claim, error) {
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return claim{}, errors.New("not a signed JWT")
	}
	if len(token.Headers) != 1 {
		return claim{}, errors.New("not one signature")
	}
	issuer, err := c.issuerFor(ctx, token.Headers[0].KeyID)
	if err != nil {
		return claim{}, err
	}
	var claims jwt.Claims
	var private kubernetesClaims
	if err := token.Claims(issuer.Keys, &claims, &private); err != nil {
		return claim{}, errors.New("its signature does not verify")
	}
	expected := jwt.Expected{Issuer: issuer.URL, AnyAudience: jwt.Audience{c.audience}, Time: c.now()}
	if err := claims.ValidateWithLeeway(expected, leeway); err != nil {
		return claim{}, err
	}
	switch {
	case claims.Expiry == nil || claims.IssuedAt == nil:
		return claim{}, errors.New("it carries no exp or no iat")
	case claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > maxLifetime:
		return claim{}, fmt.Errorf("it lives longer than %v", maxLifetime)
	case claims.IssuedAt.Time().Before(c.startedAt):
		return claim{}, errors.New("it was issued before this process started")
	}
	bot, err := botOf(claims.Subject)
	if err != nil {
		return claim{}, err
	}
	bound := private.Kubernetes
	if bound == nil || bound.Namespace != bot.Namespace || bound.ServiceAccount.Name != bot.Name ||
		bound.Pod == nil || bound.Pod.Name == "" || bound.Pod.UID == "" {
		return claim{}, errors.New("it is bound to no Pod of its ServiceAccount")
	}
	return claim{bot: bot, pod: *bound.Pod, expiry: claims.Expiry.Time()}, nil
}

// issuerFor answers the issuer, read again where kid names no key held and the last read is old enough.
func (c *credentials) issuerFor(ctx context.Context, kid string) (Issuer, error) {
	c.mu.Lock()
	issuer, readAt := c.issuer, c.readAt
	c.mu.Unlock()
	now := c.now()
	stale := now.Sub(readAt) > keysFresh
	unknown := len(issuer.Keys.Key(kid)) == 0 && now.Sub(readAt) > keysRetry
	if issuer.URL != "" && !stale && !unknown {
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
		if issuer.URL != "" {
			return issuer, nil
		}
		return Issuer{}, fmt.Errorf("read the API server's keys: %w", err)
	}
	return read.(Issuer), nil
}

// botOf is the Bot a ServiceAccount's sub names: the ServiceAccount of a Bot has the Bot's name.
func botOf(subject string) (Bot, error) {
	rest, ok := strings.CutPrefix(subject, serviceAccountPrefix)
	namespace, name, found := strings.Cut(rest, ":")
	if !ok || !found || namespace == "" || name == "" || strings.Contains(name, ":") {
		return Bot{}, errors.New("its sub names no ServiceAccount")
	}
	return Bot{Namespace: namespace, Name: name}, nil
}
