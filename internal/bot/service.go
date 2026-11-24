package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	"github.com/garamsh/garam-bot/api/declaration/v1alpha1/manifest"
)

// Service runs one Bot.
type Service interface {
	// Run reads the Bot and the token, then runs the Bot until ctx ends. It returns nil when ctx
	// ends, and an error wrapping ErrBotInvalid or ErrTokenUnreadable for a start that failed.
	Run(ctx context.Context) error
}

// Options are what a Service runs on.
type Options struct {
	// BotFile is the path of the file holding the one Bot to run.
	BotFile string
	// TokenFile is the path of the file holding the bot's garam token. Empty runs the bot with no
	// garam identity.
	TokenFile string
	// StatusFile is the path the status contract's file is written at.
	StatusFile string
	// TokenInterval is how often the token file is read again.
	TokenInterval time.Duration
	// HeartbeatInterval is how often the status file is rewritten.
	HeartbeatInterval time.Duration
	// Now is the clock.
	Now func() time.Time
	// Logger receives what the process reports.
	Logger *slog.Logger
}

type service struct {
	options Options
}

// NewService returns a Service running on options.
func NewService(options Options) Service {
	return &service{options: options}
}

func (s *service) Run(ctx context.Context) error {
	o := s.options
	// time.NewTicker panics on a period that is not positive, so one is refused before anything is
	// read or written.
	if o.HeartbeatInterval <= 0 {
		return fmt.Errorf("heartbeat interval %s: want a positive duration", o.HeartbeatInterval)
	}
	if o.TokenInterval <= 0 {
		return fmt.Errorf("token interval %s: want a positive duration", o.TokenInterval)
	}
	bot, err := readBot(o.BotFile)
	if err != nil {
		return err
	}
	var token *TokenFile
	if o.TokenFile != "" {
		if token, err = ReadTokenFile(o.TokenFile, o.Now()); err != nil {
			return err
		}
	}
	ref := BotRef{Namespace: bot.Namespace, Name: bot.Name, Generation: bot.Generation}
	write := func(state State) error {
		status := Status{State: state, Heartbeat: o.Now(), Bot: ref}
		if token != nil {
			tokenStatus := token.Status()
			status.Token = &tokenStatus
		}

		return WriteStatus(o.StatusFile, status)
	}
	if err := write(StateRunning); err != nil {
		return err
	}
	o.Logger.Info("bot running", "namespace", ref.Namespace, "name", ref.Name, "generation", ref.Generation,
		"harness", bot.Spec.Agent.Harness, "garamIdentity", token != nil)

	heartbeat := time.NewTicker(o.HeartbeatInterval)
	defer heartbeat.Stop()
	var refresh <-chan time.Time
	if token != nil {
		ticker := time.NewTicker(o.TokenInterval)
		defer ticker.Stop()
		refresh = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			o.Logger.Info("bot stopping", "namespace", ref.Namespace, "name", ref.Name)

			return write(StateStopped)
		case <-heartbeat.C:
			if err := write(StateRunning); err != nil {
				// A heartbeat that cannot be written lets the status go stale, which the probe reports.
				o.Logger.Error("status not written", "error", err)
			}
		case <-refresh:
			changed, err := token.Refresh(o.Now())
			switch {
			case err != nil:
				o.Logger.Warn("token not read again; keeping the token last read", "error", err)
			case changed:
				o.Logger.Info("token read again", "file", o.TokenFile)
			}
		}
	}
}

func readBot(path string) (*v1alpha1.Bot, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: %w", ErrBotInvalid, errNoPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBotInvalid, err)
	}
	object, err := manifest.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBotInvalid, path, err)
	}
	bot, ok := object.(*v1alpha1.Bot)
	if !ok {
		return nil, fmt.Errorf("%w: %s holds a %s, not a Bot", ErrBotInvalid, path, object.GetObjectKind().GroupVersionKind().Kind)
	}

	return bot, nil
}

var errNoPath = errors.New("no path given")
