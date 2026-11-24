package bot_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/sherlock/internal/bot"
)

var start = time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestReadTokenFile_TrimsTheToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeFile(t, path, "  first-token\n")

	token, err := bot.ReadTokenFile(path, start)
	require.NoError(t, err)
	assert.Equal(t, "first-token", token.Token())
}

func TestReadTokenFile_RefusesAnAbsentOrEmptyFile(t *testing.T) {
	directory := t.TempDir()
	empty := filepath.Join(directory, "empty")
	writeFile(t, empty, " \n")

	for name, path := range map[string]string{"absent": filepath.Join(directory, "absent"), "empty": empty} {
		t.Run(name, func(t *testing.T) {
			_, err := bot.ReadTokenFile(path, start)
			require.ErrorIs(t, err, bot.ErrTokenUnreadable)
		})
	}
}

func TestTokenFile_RefreshReadsAReplacedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeFile(t, path, "first-token")
	token, err := bot.ReadTokenFile(path, start)
	require.NoError(t, err)

	// The control: an unchanged file is read again and reported unchanged.
	changed, err := token.Refresh(start.Add(time.Second))
	require.NoError(t, err)
	assert.False(t, changed)

	writeFile(t, path, "second-token")
	changed, err = token.Refresh(start.Add(2 * time.Second))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "second-token", token.Token())
	assert.Equal(t, bot.TokenStatus{ReadAt: start.Add(2 * time.Second)}, token.Status())
}

func TestTokenFile_AFailedRefreshKeepsTheLastTokenAndMarksItStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeFile(t, path, "first-token")
	token, err := bot.ReadTokenFile(path, start)
	require.NoError(t, err)

	require.NoError(t, os.Remove(path))
	_, err = token.Refresh(start.Add(time.Second))
	require.ErrorIs(t, err, bot.ErrTokenUnreadable)
	assert.Equal(t, "first-token", token.Token())
	assert.Equal(t, bot.TokenStatus{ReadAt: start, Stale: true}, token.Status())

	writeFile(t, path, "second-token")
	_, err = token.Refresh(start.Add(2 * time.Second))
	require.NoError(t, err)
	assert.False(t, token.Status().Stale)
}

func TestTokenFile_ErrorsNeverCarryTheToken(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	writeFile(t, path, "secret-value")
	token, err := bot.ReadTokenFile(path, start)
	require.NoError(t, err)

	// A directory in the token's place fails the read after the file held a value.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))
	_, err = token.Refresh(start)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-value")
}
