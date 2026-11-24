package tests

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const garamBotMira = `apiVersion: bot.garam.sh/v1alpha1
kind: Bot
metadata: {name: mira, namespace: game-team, generation: 2}
spec:
  persona: {role: Game Lead}
  agent: {harness: claude-code}
`

func buildGaramBotBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "garam-bot")
	build := exec.Command("go", "build", "-o", path, "./cmd/garam-bot")
	build.Dir = ".." // the module root; the test binary runs in tests/
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)

	return path
}

func probe(binary, statusFile string) (string, error) {
	out, err := exec.Command(binary, "probe", "--status-file", statusFile).CombinedOutput()

	return string(out), err
}

// TestGaramBot_RunsABotThatProbePasses runs the built binary as a container runs it: run in the
// background, probe beside it, then SIGTERM.
func TestGaramBot_RunsABotThatProbePasses(t *testing.T) {
	binary := buildGaramBotBinary(t)
	directory := t.TempDir()
	botFile, tokenFile := filepath.Join(directory, "bot.yaml"), filepath.Join(directory, "token")
	statusFile := filepath.Join(directory, "status.json")
	require.NoError(t, os.WriteFile(botFile, []byte(garamBotMira), 0o600))
	require.NoError(t, os.WriteFile(tokenFile, []byte("a-garam-token\n"), 0o600))

	// The control: probe fails before run has started.
	_, err := probe(binary, statusFile)
	require.Error(t, err)

	var stderr bytes.Buffer
	run := exec.Command(binary, "run", "--bot-file", botFile, "--token-file", tokenFile,
		"--status-file", statusFile, "--termination-log", filepath.Join(directory, "termination-log"))
	run.Stderr = &stderr
	require.NoError(t, run.Start())
	t.Cleanup(func() { _ = run.Process.Kill() })

	require.Eventually(t, func() bool { _, err := probe(binary, statusFile); return err == nil },
		10*time.Second, 50*time.Millisecond, "probe never passed; run's stderr: %s", &stderr)

	require.NoError(t, run.Process.Signal(syscall.SIGTERM))
	require.NoError(t, run.Wait(), "SIGTERM exits 0; stderr: %s", &stderr)
	out, err := probe(binary, statusFile)
	require.Error(t, err)
	assert.Contains(t, out, `state is "Stopped"`)
	assert.NotContains(t, stderr.String(), "a-garam-token", "the token reaches no log line")
	assert.Contains(t, stderr.String(), `"msg":"bot running"`)
}

func TestGaramBot_AFailedStartWritesItsReasonAsTheTerminationMessage(t *testing.T) {
	binary := buildGaramBotBinary(t)
	directory := t.TempDir()
	botFile, terminationLog := filepath.Join(directory, "bot.yaml"), filepath.Join(directory, "termination-log")
	require.NoError(t, os.WriteFile(botFile, []byte(strings.Replace(garamBotMira, "Bot\n", "Agent\n", 1)), 0o600))

	run := exec.Command(binary, "run", "--bot-file", botFile,
		"--status-file", filepath.Join(directory, "status.json"), "--termination-log", terminationLog)
	err := run.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 1, exit.ExitCode())
	message, err := os.ReadFile(terminationLog)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(message), "BotInvalid: "), "termination message %q", message)
}
