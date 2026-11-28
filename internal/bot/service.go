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
	// Run reads the Bot, then runs the Bot until ctx ends, obtaining its garam token where a connector is
	// configured. It returns nil when ctx ends, and an error wrapping ErrBotInvalid for a Bot file that
	// failed the start.
	Run(ctx context.Context) error
}

// Options are what a Service runs on.
type Options struct {
	// BotFile is the path of the file holding the one Bot to run.
	BotFile string
	// ConnectorURL is the base URL of the place's connector, which answers the bot's garam token
	// (interface 6). Empty runs the bot with no garam identity.
	ConnectorURL string
	// ConnectorTrustFile holds the certificates the connector is trusted by, read again as it changes.
	ConnectorTrustFile string
	// CredentialFile holds the place's credential naming the bot, read again for each request.
	CredentialFile string
	// StatusFile is the path the status contract's file is written at.
	StatusFile string
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
	bot, err := readBot(o.BotFile)
	if err != nil {
		return err
	}
	var token *TokenSource
	if o.ConnectorURL != "" {
		if token, err = NewTokenSource(o.ConnectorURL, o.ConnectorTrustFile, o.CredentialFile, o.Now); err != nil {
			return err
		}
		ctx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		defer func() {
			stop()
			<-done
		}()
		go func() {
			defer close(done)
			token.Run(ctx, func(err error) {
				o.Logger.Warn("garam token not obtained; keeping the token held", "error", err)
			})
		}()
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
