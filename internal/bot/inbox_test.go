package bot_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/internal/bot"
)

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	return got
}

func TestInbox_AnswersTheFirstMessageByNameSettingAsideOneTooLargeAndARejectedOne(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "001-large"), strings.Repeat("x", bot.MaxMessage+1))
	writeFile(t, filepath.Join(dir, "002-limit"), strings.Repeat("y", bot.MaxMessage))
	writeFile(t, filepath.Join(dir, "003-next"), "write the release notes")
	writeFile(t, filepath.Join(dir, ".000-partial"), "still being written")
	inbox := bot.NewInbox(dir, time.Millisecond, nil, nil)

	m, err := inbox.Next(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "002-limit", m.ID, "a message of MaxMessage is taken, and the larger one before it is not")
	assert.Len(t, m.Input[0].Text, bot.MaxMessage)
	assert.Equal(t, []string{".000-partial", ".001-large.rejected", "002-limit", "003-next"}, names(t, dir))

	require.NoError(t, inbox.Done(t.Context(), m, bot.OutcomeRejected))
	m, err = inbox.Next(t.Context())
	require.NoError(t, err)
	assert.Equal(t, bot.Message{ID: "003-next", Input: []agent.Input{{Text: "write the release notes"}}}, m)
	require.NoError(t, inbox.Done(t.Context(), m, agent.OutcomeFailed))
	assert.Equal(t, []string{".000-partial", ".001-large.rejected", ".002-limit.rejected"}, names(t, dir),
		"a rejected message is set aside, and one whose task ended is removed")

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = inbox.Next(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "an inbox of no message waits")
}
