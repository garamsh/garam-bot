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
	// Run reads the Bot, then runs the Bot until ctx ends, obtaining its garam token where a connector
	// and a credential file are configured, and driving its first attachment's workspace where a
	// connector and a workspace credential file are, its adapter's session with it. It returns nil when ctx ends, and an error wrapping ErrBotInvalid for a Bot file that
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
	// CredentialFile holds the place's credential naming the bot for interface 6, read again for each
	// request. Empty, the bot runs with no garam identity.
	CredentialFile string
	// WorkspaceCredentialFile holds the place's credential naming the bot for interface 7, read again
	// for each request. Empty, or for a Bot with no attachment, the bot runs with no workspace.
	WorkspaceCredentialFile string
	// InboxDir is the directory of message files the bot's workspace is given tasks from, one task per
	// file, until G4. Empty, its session is opened and given no task.
	InboxDir string
	// MailboxURL, MailboxOrigin, GaramRootFile and BotGRN, given together, have a garam-owned bot's
	// messages come from its garam mailbox: garam's machine listener as dialled, its origin, the root
	// its certificate chains to, and the bot's GRN. They need CredentialFile, the bot's garam identity,
	// and exclude InboxDir.
	MailboxURL    string
	MailboxOrigin string
	GaramRootFile string
	BotGRN        string
	// Sleep waits d, or until ctx ends. Time's own where nil.
	Sleep func(ctx context.Context, d time.Duration) error
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
	var stops []func()
	token, space, err := s.placeClients(bot)
	if err != nil {
		return err
	}
	messages, err := s.messages(token)
	if err != nil {
		return err
	}
	background := func(run func(context.Context)) {
		ctx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		stops = append(stops, func() {
			stop()
			<-done
		})
		go func() {
			defer close(done)
			run(ctx)
		}()
	}
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	if token != nil {
		background(func(ctx context.Context) {
			token.Run(ctx, func(err error) {
				o.Logger.Warn("garam token not obtained; keeping the token held", "error", err)
			})
		})
	}
	var driver *Driver
	if space != nil {
		background(func(ctx context.Context) {
			space.Run(ctx, func(err error) {
				o.Logger.Warn("workspace not ready; retrying", "attachment", space.cfg.Attachment, "error", err)
			})
		})
		if driver, err = NewDriver(DriverConfig{Bot: bot, Place: space, Messages: messages, Sleep: o.Sleep, Logger: o.Logger}); err != nil {
			return err
		}
		background(driver.Run)
	}
	ref := BotRef{Namespace: bot.Namespace, Name: bot.Name, Generation: bot.Generation}
	write := func(state State) error {
		status := Status{State: state, Heartbeat: o.Now(), Bot: ref}
		if token != nil {
			tokenStatus := token.Status()
			status.Token = &tokenStatus
		}
		if space != nil {
			spaceStatus := space.Status()
			status.Workspace = &spaceStatus
			sessionStatus := driver.Status()
			status.Session = &sessionStatus
		}

		return WriteStatus(o.StatusFile, status)
	}
	if err := write(StateRunning); err != nil {
		return err
	}
	o.Logger.Info("bot running", "namespace", ref.Namespace, "name", ref.Name, "generation", ref.Generation,
		"harness", bot.Spec.Agent.Harness, "garamIdentity", token != nil, "workspace", space != nil)

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

// placeClients builds what reaches the place's connector, both bound to one key the process makes
// (KEY-1): interface 6's token source where a credential file is given, and interface 7's workspace of
// the Bot's first attachment where a workspace credential file is given and the Bot has one.
func (s *service) placeClients(bot *v1alpha1.Bot) (*TokenSource, *Workspace, error) {
	o := s.options
	if o.ConnectorURL == "" {
		if o.CredentialFile != "" || o.WorkspaceCredentialFile != "" {
			return nil, nil, errNoConnector
		}
		return nil, nil, nil
	}
	if o.ConnectorTrustFile == "" || (o.CredentialFile == "" && o.WorkspaceCredentialFile == "") {
		return nil, nil, errNoPlace
	}
	key, err := NewKey()
	if err != nil {
		return nil, nil, err
	}
	var token *TokenSource
	if o.CredentialFile != "" {
		if token, err = NewTokenSource(o.ConnectorURL, o.ConnectorTrustFile, o.CredentialFile, key, o.Now); err != nil {
			return nil, nil, err
		}
	}
	var space *Workspace
	if o.WorkspaceCredentialFile != "" && len(bot.Spec.Workspaces) > 0 {
		space, err = NewWorkspace(WorkspaceConfig{
			URL: o.ConnectorURL, TrustFile: o.ConnectorTrustFile, CredentialFile: o.WorkspaceCredentialFile,
			Attachment: bot.Spec.Workspaces[0].Name, Key: key, Now: o.Now, Sleep: o.Sleep,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return token, space, nil
}

// messages is the bot's message source: its garam mailbox where it is configured, the inbox where
// that is, and none otherwise.
func (s *service) messages(token *TokenSource) (MessageSource, error) {
	o := s.options
	// NewMailbox refuses some of the mailbox's options without the rest.
	mailbox := o.MailboxURL != "" || o.MailboxOrigin != "" || o.GaramRootFile != "" || o.BotGRN != ""
	switch {
	case !mailbox && o.InboxDir != "":
		return NewInbox(o.InboxDir, 0, o.Sleep, o.Logger), nil
	case !mailbox:
		return nil, nil
	case token == nil:
		return nil, errMailboxIdentity
	case o.InboxDir != "":
		return nil, errTwoSources
	}
	return NewMailbox(MailboxConfig{
		URL: o.MailboxURL, Origin: o.MailboxOrigin, RootFile: o.GaramRootFile, GRN: o.BotGRN,
		Identity: token, Now: o.Now, Logger: o.Logger,
	})
}

// The mailbox's configurations that fail the start.
var (
	errMailboxIdentity = errors.New("--mailbox-url needs --credential-file, the bot's garam identity")
	errTwoSources      = errors.New("--mailbox-url and --inbox-dir are two message sources; give one")
)

// errNoConnector is a credential file given with no connector to present it to.
var errNoConnector = errors.New("--credential-file and --workspace-credential-file need --connector-url")

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
