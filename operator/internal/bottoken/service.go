package bottoken

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	// DefaultAudience is the audience a Bot's projected ServiceAccount token names for the server.
	DefaultAudience = "garam-connector"
	// defaultValidity is how long a serving certificate is valid, and defaultPropagation how long a new
	// one is published before it is served, so every bot's process trusts it first.
	defaultValidity    = 90 * 24 * time.Hour
	defaultPropagation = 10 * time.Minute
	// readTimeout bounds how long a request's headers take to arrive.
	readTimeout = 10 * time.Second
)

// Config is what a Service is built from.
type Config struct {
	// Record is the cluster record's GRN, grn:<org>/iam/cluster/<id>.
	Record string
	// Namespaces are the watched namespaces. A Bot of any other is unknown (BOT-1).
	Namespaces []string
	// Audience is the audience a credential names for this server, DefaultAudience when empty.
	Audience string
	Keys     Keys
	Bots     Bots
	Pods     Pods
	Trust    TrustStore
	// HTTP reaches garam's token endpoint, TokenURL, trusting the configured root alone and presenting
	// nothing. TokenURL is read for each exchange, as a rotation may change it. Actor answers the
	// cluster's own token, the exchange's actor_token, and Invalidate drops one garam answered 401 to.
	HTTP       *http.Client
	TokenURL   func() string
	Actor      func(ctx context.Context) (string, error)
	Invalidate func(token string)
	// DNSNames are the names the serving certificate is issued for.
	DNSNames []string
	Log      *slog.Logger
	// Now, Validity and Propagation default to the clock, 90 days and 10 minutes.
	Now         func() time.Time
	Validity    time.Duration
	Propagation time.Duration
}

// Service serves interface 6 to the bots' processes of the watched namespaces.
type Service interface {
	// Serve publishes the serving certificate, then answers on listener until ctx ends, replacing the
	// certificate before it expires.
	Serve(ctx context.Context, listener net.Listener) error
}

type service struct {
	cfg         Config
	namespaces  map[string]bool
	credentials *credentials
	exchanger   *exchanger
	limits      *limits
}

// NewService returns a Service under cfg.
func NewService(cfg Config) Service {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Audience == "" {
		cfg.Audience = DefaultAudience
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Validity <= 0 {
		cfg.Validity = defaultValidity
	}
	if cfg.Propagation <= 0 {
		cfg.Propagation = defaultPropagation
	}
	namespaces := make(map[string]bool, len(cfg.Namespaces))
	for _, namespace := range cfg.Namespaces {
		namespaces[namespace] = true
	}
	return &service{
		cfg:         cfg,
		namespaces:  namespaces,
		credentials: newCredentials(cfg.Keys, cfg.Audience, cfg.Now),
		exchanger: &exchanger{http: cfg.HTTP, tokenURL: cfg.TokenURL, actor: cfg.Actor,
			invalidate: cfg.Invalidate},
		limits: newLimits(cfg.Now),
	}
}
