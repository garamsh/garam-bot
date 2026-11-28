package workspaceaccess

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

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

const (
	// leeway is the clock skew a credential's iat and nbf are read with. Its exp is read with none:
	// CRED-2 takes no credential past it.
	leeway = 30 * time.Second
	// keysFresh is how long the API server's keys are used before they are read again, and keysRetry
	// how soon after a read a credential signed by a key not held reads them again.
	keysFresh = 5 * time.Minute
	keysRetry = 10 * time.Second
	// maxCredential bounds the credential read, as a projected ServiceAccount token is a few KiB.
	maxCredential = 16 << 10
)

// serviceAccountPrefix begins the sub of every ServiceAccount token, system:serviceaccount:<ns>:<name>.
const serviceAccountPrefix = "system:serviceaccount:"

// credentials verifies the place credential, a projected ServiceAccount token of the workspace audience.
// Unlike interface 6's, it is reused while it lives (CRED-2).
type credentials struct {
	keys     Keys
	audience string
	now      func() time.Time
	// startedAt is when this process started. A credential issued before it is refused, as a pin
	// held before a restart is lost (PIN-1).
	startedAt time.Time

	reads singleflight.Group

	mu     sync.Mutex
	issuer Issuer
	readAt time.Time
}

func newCredentials(keys Keys, audience string, now func() time.Time) *credentials {
	// A credential's iat is in whole seconds.
	return &credentials{keys: keys, audience: audience, now: now, startedAt: now().Truncate(time.Second)}
}

// bound is what a verified credential names: the Bot whose ServiceAccount it is, and the Pod it was
// issued to.
type bound struct {
	bot Bot
	pod boundPod
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

// verify answers what raw names, or errCredential where it does not verify against the API server's
// keys, has expired, names another audience, lives longer than an hour, carries no iat, was issued
// before this process started, or is bound to no Pod of its ServiceAccount (CRED-2).
func (c *credentials) verify(ctx context.Context, raw string) (bound, error) {
	if raw == "" || len(raw) > maxCredential {
		return bound{}, fmt.Errorf("%w: it is missing or too long", errCredential)
	}
	verified, err := c.check(ctx, raw)
	if err != nil {
		return bound{}, fmt.Errorf("%w: %w", errCredential, err)
	}
	return verified, nil
}

func (c *credentials) check(ctx context.Context, raw string) (bound, error) {
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return bound{}, errors.New("not a signed JWT")
	}
	if len(token.Headers) != 1 {
		return bound{}, errors.New("not one signature")
	}
	issuer, err := c.issuerFor(ctx, token.Headers[0].KeyID)
	if err != nil {
		return bound{}, err
	}
	var claims jwt.Claims
	var private kubernetesClaims
	if err := token.Claims(issuer.Keys, &claims, &private); err != nil {
		return bound{}, errors.New("its signature does not verify")
	}
	now := c.now()
	expected := jwt.Expected{Issuer: issuer.URL, AnyAudience: jwt.Audience{c.audience}, Time: now}
	if err := claims.ValidateWithLeeway(expected, leeway); err != nil {
		return bound{}, err
	}
	switch {
	case claims.Expiry == nil || claims.IssuedAt == nil:
		return bound{}, errors.New("it carries no exp or no iat")
	case !now.Before(claims.Expiry.Time()):
		return bound{}, errors.New("it has expired")
	case claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > access.MaxCredentialLife:
		return bound{}, fmt.Errorf("it lives longer than %v", access.MaxCredentialLife)
	case claims.IssuedAt.Time().Before(c.startedAt):
		return bound{}, errors.New("it was issued before this process started")
	}
	bot, err := botOf(claims.Subject)
	if err != nil {
		return bound{}, err
	}
	held := private.Kubernetes
	if held == nil || held.Namespace != bot.Namespace || held.ServiceAccount.Name != bot.Name ||
		held.Pod == nil || held.Pod.Name == "" || held.Pod.UID == "" {
		return bound{}, errors.New("it is bound to no Pod of its ServiceAccount")
	}
	return bound{bot: bot, pod: *held.Pod}, nil
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
