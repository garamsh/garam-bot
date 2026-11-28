package workspaceaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
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
	// defaultDialTimeout is how long a dial waits for its provider to connect.
	defaultDialTimeout = 30 * time.Second
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
	// LiveBots reads a Bot live, for the deletion sweep alone.
	LiveBots LiveBots
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
	// most and by default 10 seconds, Recheck how often open dials' authority is read again, 2
	// seconds by default, and SweepEvery how often the deletion sweep runs, a minute by default.
	Now          func() time.Time
	BindingFresh time.Duration
	Recheck      time.Duration
	SweepEvery   time.Duration
	// DialTimeout is how long a dial waits for its provider to connect, 30 seconds by default.
	DialTimeout time.Duration
}

// Service serves interface 7 to the bots' processes of the watched namespaces.
type Service interface {
	// Handler answers interface 7's calls.
	Handler() http.Handler
	// Run closes idle dials, closes dials whose authority ended, drops the pins of instances that are
	// gone, and deletes the workspaces of Bots that are gone, until ctx ends (DIAL-3, DIAL-4, PIN-1,
	// ADR 0099 decision 6).
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
	reads       *reads
	sweeper     *sweeper
}

// NewService returns a Service under cfg.
func NewService(cfg Config) (Service, error) {
	if cfg.Keys == nil || cfg.Pods == nil || cfg.Bots == nil || cfg.LiveBots == nil {
		return nil, errors.New("Keys, Pods, Bots and LiveBots are required")
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
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultDialTimeout
	}
	if cfg.SweepEvery <= 0 {
		cfg.SweepEvery = defaultSweepEvery
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
		reads:       newReads(),
		sweeper:     newSweeper(),
	}, nil
}

func (s *service) Run(ctx context.Context) error {
	defer s.dials.closeWhere(func(*dial) bool { return true })
	var loops sync.WaitGroup
	every := func(period time.Duration, pass func(context.Context)) {
		loops.Go(func() {
			tick := time.NewTicker(period)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					pass(ctx)
				}
			}
		})
	}
	// The local passes read nothing, so no stuck read delays the idle close (DIAL-3).
	every(s.cfg.Recheck, func(context.Context) {
		s.dials.closeIdle()
		s.proofs.forget()
		s.bindings.forget()
		s.botLimits.forgetFull()
		s.instLimits.forgetFull()
	})
	// Each pass that reads is bounded by its period, and none waits on another.
	every(s.cfg.Recheck, bounded(s.cfg.Recheck, s.recheckDials))
	every(s.cfg.Recheck, bounded(s.cfg.Recheck, s.recheckPins))
	every(s.cfg.SweepEvery, bounded(s.cfg.SweepEvery, func(ctx context.Context) { s.sweep(ctx, 0) }))
	<-ctx.Done()
	loops.Wait()
	return ctx.Err()
}

// bounded is pass with a deadline of period.
func bounded(period time.Duration, pass func(context.Context)) func(context.Context) {
	return func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, period)
		defer cancel()
		pass(ctx)
	}
}

// spaceKey is a workspace, by its place's provider and its name.
type spaceKey struct {
	provider string
	name     workspace.Name
}

// rechecked holds what one pass of recheckDials has read, so each Bot, Pod and workspace is read once
// however many dials it carries.
type rechecked struct {
	bots   map[Bot]read[Declared]
	pods   map[podKey]read[Pod]
	spaces map[spaceKey]read[workspace.Workspace]
}

// read is one answer of a read: the value, whether it exists, and the error.
type read[T any] struct {
	value  T
	exists bool
	err    error
}

// recheckDials closes each open dial whose instance ended, whose Bot is gone or made again, or whose
// workspace stopped or is deleted (DIAL-4). A read that fails keeps the dial until the next pass.
func (s *service) recheckDials(ctx context.Context) {
	r := rechecked{bots: map[Bot]read[Declared]{}, pods: map[podKey]read[Pod]{}, spaces: map[spaceKey]read[workspace.Workspace]{}}
	for _, d := range s.dials.snapshot() {
		if s.lapsed(ctx, d, r) {
			s.dials.release(d)
		}
	}
}

// lapsed answers whether the dial's authority has ended, reading the Bot, then the Pod, then the
// workspace, so a dial whose instance ended is closed without a provider being asked.
func (s *service) lapsed(ctx context.Context, d *dial, r rechecked) bool {
	bot, ok := r.bots[d.bot]
	if !ok {
		bot.value, bot.exists, bot.err = s.cfg.Bots.Bot(ctx, d.bot)
		r.bots[d.bot] = bot
	}
	if bot.err != nil {
		return false
	}
	if !bot.exists || bot.value.UID != d.botUID {
		return true
	}
	key := podKey{d.instance.namespace, d.instance.name}
	pod, ok := r.pods[key]
	if !ok {
		pod.value, pod.exists, pod.err = s.bindings.pod(ctx, key.namespace, key.name, false)
		r.pods[key] = pod
	}
	if pod.err != nil {
		return false
	}
	if !pod.exists || pod.value.UID != d.instance.pod || pod.value.Container != d.instance.container {
		return true
	}
	place, ok := s.cfg.Places[d.provider]
	if !ok {
		return true
	}
	space := spaceKey{d.provider, d.workspace}
	ws, ok := r.spaces[space]
	if !ok {
		ws.value, ws.err = s.reads.get(ctx, space, place.Provider)
		r.spaces[space] = ws
	}
	if errors.Is(ws.err, workspace.ErrNotFound) {
		return true
	}
	return ws.err == nil && ws.value.Status.Phase != workspace.PhaseRunning
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
