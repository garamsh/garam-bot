package workspaceaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

const (
	// DefaultAudience is the audience a Bot's workspace credential names (ADR 0099 decision 1).
	DefaultAudience = "garam-workspace"
	// DefaultMaxDials is the cap on the dials open in all (DIAL-3).
	DefaultMaxDials = 256
	// defaultRecheck is how often the authority of open dials and pins is read again (DIAL-4, PIN-1).
	defaultRecheck = 2 * time.Second
)

// Place is what the connector runs of one workspace provider: the provider, and what its workspaces
// reach and may send to (ADR 0099 decision 7).
type Place struct {
	Provider workspace.Provider
	// ModelURL is the model gateway its workspaces reach, and ToolProxyURL the tool proxy each tool's
	// URL is below.
	ModelURL     string
	ToolProxyURL string
	// Egress are the rules every workspace of the provider is given.
	Egress []workspace.EgressRule
}

// Config is what a Service is built from.
type Config struct {
	Release Release
	// Namespaces are the watched namespaces. A Bot of any other is unknown (BOT-1).
	Namespaces []string
	// Audience is the audience a credential names for this server, DefaultAudience when empty.
	Audience string
	// Origins are the https://host[:port] the processes reach the server at, which a proof's htu names.
	Origins []string
	Keys    Keys
	Pods    Pods
	Bots    Bots
	// Places are the providers the connector runs, by the name a WorkspaceClass's spec.provider gives.
	Places map[string]Place
	// Images are the workspace image of each harness, repository@sha256:<digest> (ADR 0099 decision 8).
	Images map[string]string
	// Issuer obtains session tokens. Nil, the call answers waiting_for_gateway (TOK-1).
	Issuer SessionIssuer
	// MaxDials caps the dials open in all, DefaultMaxDials when 0 or less.
	MaxDials int
	Log      *slog.Logger
	// Now is the clock. BindingFresh is how long a credential's binding to its instance is kept, at
	// most and by default 10 seconds, and Recheck how often open dials' authority is read again, 2
	// seconds by default.
	Now          func() time.Time
	BindingFresh time.Duration
	Recheck      time.Duration
}

// Service serves interface 7 to the bots' processes of the watched namespaces.
type Service interface {
	// Handler answers interface 7's calls.
	Handler() http.Handler
	// Run closes idle dials, closes dials whose authority ended, and drops the pins of instances that
	// are gone, until ctx ends (DIAL-3, DIAL-4, PIN-1).
	Run(ctx context.Context) error
}

type service struct {
	cfg         Config
	namespaces  map[string]bool
	credentials *credentials
	bindings    *bindings
	proofs      *proofs
	pins        *pins
	botLimits   *limits
	instLimits  *limits
	dials       *dials
}

// NewService returns a Service under cfg.
func NewService(cfg Config) (Service, error) {
	if cfg.Keys == nil || cfg.Pods == nil || cfg.Bots == nil {
		return nil, errors.New("Keys, Pods and Bots are required")
	}
	if cfg.Release.Namespace == "" || cfg.Release.Name == "" {
		return nil, errors.New("the release is required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Audience == "" {
		cfg.Audience = DefaultAudience
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxDials <= 0 {
		cfg.MaxDials = DefaultMaxDials
	}
	if cfg.Recheck <= 0 {
		cfg.Recheck = defaultRecheck
	}
	proofs, err := newProofs(cfg.Origins, cfg.Now)
	if err != nil {
		return nil, fmt.Errorf("the origins: %w", err)
	}
	namespaces := make(map[string]bool, len(cfg.Namespaces))
	for _, namespace := range cfg.Namespaces {
		namespaces[namespace] = true
	}
	return &service{
		cfg:         cfg,
		namespaces:  namespaces,
		credentials: newCredentials(cfg.Keys, cfg.Audience, cfg.Now),
		bindings:    newBindings(cfg.Pods, cfg.BindingFresh, cfg.Now),
		proofs:      proofs,
		pins:        newPins(),
		botLimits:   newLimits("the bot's limit", botEvery, botBurst, cfg.Now),
		instLimits:  newLimits("the instance's request limit", instanceEvery, instanceBurst, cfg.Now),
		dials:       newDials(cfg.MaxDials, cfg.Now),
	}, nil
}

func (s *service) Run(ctx context.Context) error {
	tick := time.NewTicker(s.cfg.Recheck)
	defer tick.Stop()
	defer s.dials.closeWhere(func(*dial) bool { return true })
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		s.dials.closeIdle()
		s.recheckDials(ctx)
		s.recheckPins(ctx)
	}
}

// recheckDials closes each open dial whose instance ended, whose Bot is gone or made again, or whose
// workspace stopped or is deleted (DIAL-4). A read that fails keeps the dial until the next.
func (s *service) recheckDials(ctx context.Context) {
	for _, d := range s.dials.snapshot() {
		if s.lapsed(ctx, d) {
			s.dials.release(d)
		}
	}
}

func (s *service) lapsed(ctx context.Context, d *dial) bool {
	declared, ok, err := s.cfg.Bots.Bot(ctx, d.bot)
	if err != nil {
		return false
	}
	if !ok || declared.UID != d.botUID {
		return true
	}
	pod, exists, err := s.bindings.pod(ctx, d.instance.namespace, d.instance.name, false)
	if err != nil {
		return false
	}
	if !exists || pod.UID != d.instance.pod || pod.Container != d.instance.container {
		return true
	}
	place, ok := s.cfg.Places[d.provider]
	if !ok {
		return true
	}
	ws, err := place.Provider.Get(ctx, d.workspace)
	if errors.Is(err, workspace.ErrNotFound) {
		return true
	}
	return err == nil && ws.Status.Phase != workspace.PhaseRunning
}

// recheckPins drops the pin of every instance whose Pod is gone, or whose runtime container is another
// now (PIN-1).
func (s *service) recheckPins(ctx context.Context) {
	for _, i := range s.pins.instances() {
		pod, exists, err := s.bindings.pod(ctx, i.namespace, i.name, false)
		if err != nil {
			continue
		}
		switch {
		case !exists || pod.UID != i.pod:
			s.pins.drop(i.pod, "")
			s.instLimits.forget(i.pod)
		case pod.Container != i.container:
			s.pins.drop(i.pod, pod.Container)
		}
	}
}
