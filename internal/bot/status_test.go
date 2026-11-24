package bot_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/sherlock/internal/bot"
)

func TestProbe_PassesOnlyARunningStatusWithAFreshHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	running := bot.Status{State: bot.StateRunning, Heartbeat: start, Bot: bot.BotRef{Name: "mira"}}
	require.NoError(t, bot.WriteStatus(path, running))

	// The control: the running, fresh status passes, up to the freshness bound.
	require.NoError(t, bot.Probe(path, start.Add(bot.Freshness)))

	require.ErrorIs(t, bot.Probe(path, start.Add(bot.Freshness+time.Second)), bot.ErrNotRunning, "a stale heartbeat")

	stopped := running
	stopped.State = bot.StateStopped
	require.NoError(t, bot.WriteStatus(path, stopped))
	require.ErrorIs(t, bot.Probe(path, start), bot.ErrNotRunning, "a stopped process")
}

func TestProbe_FailsWithoutAStatusFile(t *testing.T) {
	require.ErrorIs(t, bot.Probe(filepath.Join(t.TempDir(), "status.json"), start), bot.ErrNotRunning)
}
