package bot_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/internal/bot"
)

const mira = `apiVersion: bot.garam.sh/v1alpha1
kind: Bot
metadata: {name: mira, namespace: game-team, generation: 4}
spec:
  persona: {role: Game Lead}
  agent: {harness: claude-code}
`

type files struct {
	bot, status string
}

func newFiles(t *testing.T) files {
	t.Helper()
	directory := t.TempDir()

	return files{
		bot:    filepath.Join(directory, "bot.yaml"),
		status: filepath.Join(directory, "status.json"),
	}
}

func options(f files) bot.Options {
	return bot.Options{
		BotFile:           f.bot,
		StatusFile:        f.status,
		HeartbeatInterval: 10 * time.Millisecond,
		Now:               time.Now,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func readStatus(t *testing.T, path string) bot.Status {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var status bot.Status
	require.NoError(t, json.Unmarshal(data, &status))

	return status
}

// runInBackground starts the service and returns a stop that cancels it and returns Run's error.
func runInBackground(t *testing.T, o bot.Options) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- bot.NewService(o).Run(ctx) }()

	return func() error {
		cancel()

		return <-done
	}
}

func TestRun_RunsTheBotAndReportsItRunning(t *testing.T) {
	f := newFiles(t)
	writeFile(t, f.bot, mira)
	stop := runInBackground(t, options(f))

	require.Eventually(t, func() bool { return bot.Probe(f.status, time.Now()) == nil }, 5*time.Second, 10*time.Millisecond)
	status := readStatus(t, f.status)
	assert.Equal(t, bot.BotRef{Namespace: "game-team", Name: "mira", Generation: 4}, status.Bot)

	require.NoError(t, stop())
	assert.Equal(t, bot.StateStopped, readStatus(t, f.status).State)
	require.ErrorIs(t, bot.Probe(f.status, time.Now()), bot.ErrNotRunning)
}

func TestRun_KeepsTheHeartbeatFresh(t *testing.T) {
	f := newFiles(t)
	writeFile(t, f.bot, mira)
	stop := runInBackground(t, options(f))
	defer func() { require.NoError(t, stop()) }()

	require.Eventually(t, func() bool { return bot.Probe(f.status, time.Now()) == nil }, 5*time.Second, 10*time.Millisecond)
	first := readStatus(t, f.status).Heartbeat
	require.Eventually(t, func() bool { return readStatus(t, f.status).Heartbeat.After(first) }, 5*time.Second, 10*time.Millisecond)
	assert.Nil(t, readStatus(t, f.status).Token, "a bot with no garam identity reports no token")
}

func TestRun_ReportsTheGaramTokenItObtainsAndRunsWhileItIsRefused(t *testing.T) {
	f := newFiles(t)
	writeFile(t, f.bot, mira)
	c := newConnector(t)
	c.set(3, 0)
	o := options(f)
	o.ConnectorURL, o.ConnectorTrustFile, o.CredentialFile = c.server.URL, c.trust, c.credential
	stop := runInBackground(t, o)
	defer func() { require.NoError(t, stop()) }()
	require.Eventually(t, func() bool { return bot.Probe(f.status, time.Now()) == nil }, 5*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		token := readStatus(t, f.status).Token

		return token != nil && !token.ObtainedAt.IsZero() && !token.Stale
	}, 5*time.Second, 10*time.Millisecond)
	obtained := readStatus(t, f.status).Token
	assert.Equal(t, obtained.ObtainedAt.Add(3*time.Second), obtained.ExpiresAt)

	c.set(3, http.StatusServiceUnavailable)
	require.Eventually(t, func() bool { return readStatus(t, f.status).Token.Stale }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, bot.Probe(f.status, time.Now()), "a stale token does not stop the bot")
	data, err := os.ReadFile(f.status)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "garam_bt_", "the status carries no token")
}

func TestRun_RefusesToStartOnAnInvalidBotOrConfiguration(t *testing.T) {
	valid := func(t *testing.T, f files) {
		writeFile(t, f.bot, mira)
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f files)
		edit  func(o *bot.Options)
		// want is the reason the start fails with, and empty for the control, which starts.
		want bot.Reason
	}{
		{name: "the control: a Bot starts", setup: valid},
		{name: "the control: a Bot with its connector starts", setup: valid, edit: func(o *bot.Options) {
			o.ConnectorURL, o.ConnectorTrustFile, o.CredentialFile = "https://connector.test", "/ca.crt", "/token"
		}},
		{name: "no bot file", setup: func(*testing.T, files) {}, want: bot.ReasonBotInvalid},
		{name: "a bot file that is not a Bot", want: bot.ReasonBotInvalid, setup: func(t *testing.T, f files) {
			writeFile(t, f.bot, "apiVersion: bot.garam.sh/v1alpha1\nkind: BotPool\nmetadata: {name: p}\nspec: {maxBotsPerPod: 5}\n")
		}},
		{name: "a Bot the spec refuses", want: bot.ReasonBotInvalid, setup: func(t *testing.T, f files) {
			writeFile(t, f.bot, mira+"  image: example.com/agent:1\n")
		}},
		{name: "the control: a Bot with its connector for its workspace alone starts", setup: valid, edit: func(o *bot.Options) {
			o.ConnectorURL, o.ConnectorTrustFile, o.WorkspaceCredentialFile = "https://connector.test", "/ca.crt", "/token"
		}},
		{name: "a connector without any credential file", want: bot.ReasonFailed, setup: valid, edit: func(o *bot.Options) {
			o.ConnectorURL, o.ConnectorTrustFile = "https://connector.test", "/ca.crt"
		}},
		{name: "a connector without its trust file", want: bot.ReasonFailed, setup: valid, edit: func(o *bot.Options) {
			o.ConnectorURL, o.WorkspaceCredentialFile = "https://connector.test", "/token"
		}},
		{name: "a workspace credential file without a connector", want: bot.ReasonFailed, setup: valid, edit: func(o *bot.Options) {
			o.WorkspaceCredentialFile = "/token"
		}},
		{name: "the control: a Bot with its mailbox starts", setup: valid, edit: func(o *bot.Options) {
			o.ConnectorURL, o.ConnectorTrustFile, o.CredentialFile = "https://connector.test", "/ca.crt", "/token"
			o.MailboxURL, o.MailboxOrigin, o.GaramRootFile, o.BotGRN = "https://garam.test", "https://garam.test", "/garam.crt", "grn:acme/iam/bot/d1"
		}},
		{name: "a mailbox without its root file", want: bot.ReasonFailed, setup: valid, edit: func(o *bot.Options) {
			o.ConnectorURL, o.ConnectorTrustFile, o.CredentialFile = "https://connector.test", "/ca.crt", "/token"
			o.MailboxURL, o.MailboxOrigin, o.GaramRootFile, o.BotGRN = "https://garam.test", "https://garam.test", "/garam.crt", "grn:acme/iam/bot/d1"
			o.GaramRootFile = ""
		}},
		{name: "a mailbox without the bot's garam identity", want: bot.ReasonFailed, setup: valid, edit: func(o *bot.Options) {
			o.ConnectorURL, o.ConnectorTrustFile, o.CredentialFile = "https://connector.test", "/ca.crt", "/token"
			o.MailboxURL, o.MailboxOrigin, o.GaramRootFile, o.BotGRN = "https://garam.test", "https://garam.test", "/garam.crt", "grn:acme/iam/bot/d1"
			o.CredentialFile, o.WorkspaceCredentialFile = "", "/token"
		}},
		{name: "a mailbox and an inbox", want: bot.ReasonFailed, setup: valid, edit: func(o *bot.Options) {
			o.ConnectorURL, o.ConnectorTrustFile, o.CredentialFile = "https://connector.test", "/ca.crt", "/token"
			o.MailboxURL, o.MailboxOrigin, o.GaramRootFile, o.BotGRN = "https://garam.test", "https://garam.test", "/garam.crt", "grn:acme/iam/bot/d1"
			o.InboxDir = "/inbox"
		}},
		{name: "a negative heartbeat interval", want: bot.ReasonFailed, setup: valid, edit: func(o *bot.Options) {
			o.HeartbeatInterval = -time.Second
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFiles(t)
			tc.setup(t, f)
			o := options(f)
			if tc.edit != nil {
				tc.edit(&o)
			}
			// Ended before Run starts, so a start that succeeds returns at once.
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := bot.NewService(o).Run(ctx)
			_, statErr := os.Stat(f.status)
			if tc.want == "" {
				require.NoError(t, err)
				assert.NoError(t, statErr, "a start writes the status")

				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.want, bot.ReasonOf(err))
			assert.ErrorIs(t, statErr, os.ErrNotExist, "a failed start writes no status")
		})
	}
}
