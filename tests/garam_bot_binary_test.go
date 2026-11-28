package tests

import (
	"bytes"
	"encoding/json"
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

// referenceConnector serves interface 6 from its reference server, built from api/ and run in the
// background until the test ends, and answers its directory, which its fixture subcommands share.
func referenceConnector(t *testing.T) (reference string, directory string) {
	t.Helper()
	reference = filepath.Join(t.TempDir(), "bot-token-reference")
	build := exec.Command("go", "build", "-o", reference, "./bot-token/v1alpha1/reference/cmd/bot-token-reference")
	build.Dir = filepath.Join("..", "api")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)
	directory = t.TempDir()
	serve := exec.Command(reference, "--dir", directory, "serve")
	require.NoError(t, serve.Start())
	t.Cleanup(func() {
		_ = serve.Process.Signal(syscall.SIGTERM)
		_ = serve.Wait()
	})
	require.Eventually(t, func() bool {
		read, err := os.ReadFile(filepath.Join(directory, "url"))
		return err == nil && len(read) > 0
	}, 10*time.Second, 50*time.Millisecond, "the reference server never wrote its URL")
	return reference, directory
}

func fixture(t *testing.T, reference, directory string, args ...string) []byte {
	t.Helper()
	out, err := exec.Command(reference, append([]string{"--dir", directory}, args...)...).Output()
	require.NoError(t, err, "fixture %v", args)
	return out
}

// TestGaramBot_RunsABotThatProbePasses runs the built binary as a container runs it, with its garam
// token from the reference connector: run in the background, probe beside it, then SIGTERM.
func TestGaramBot_RunsABotThatProbePasses(t *testing.T) {
	binary := buildGaramBotBinary(t)
	reference, connector := referenceConnector(t)
	fixture(t, reference, connector, "bot", "game-team", "mira", "--owner", "garam")
	var issued struct {
		Credential string `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(fixture(t, reference, connector, "credential", "game-team", "mira"), &issued))
	url, err := os.ReadFile(filepath.Join(connector, "url"))
	require.NoError(t, err)

	directory := t.TempDir()
	botFile, credentialFile := filepath.Join(directory, "bot.yaml"), filepath.Join(directory, "token")
	statusFile := filepath.Join(directory, "status.json")
	require.NoError(t, os.WriteFile(botFile, []byte(garamBotMira), 0o600))
	require.NoError(t, os.WriteFile(credentialFile, []byte(issued.Credential), 0o600))

	// The control: probe fails before run has started.
	_, err = probe(binary, statusFile)
	require.Error(t, err)

	var stderr bytes.Buffer
	run := exec.Command(binary, "run", "--bot-file", botFile, "--connector-url", strings.TrimSpace(string(url)),
		"--connector-trust-file", filepath.Join(connector, "ca.pem"), "--credential-file", credentialFile,
		"--status-file", statusFile, "--termination-log", filepath.Join(directory, "termination-log"))
	run.Stderr = &stderr
	require.NoError(t, run.Start())
	t.Cleanup(func() { _ = run.Process.Kill() })

	require.Eventually(t, func() bool { _, err := probe(binary, statusFile); return err == nil },
		10*time.Second, 50*time.Millisecond, "probe never passed; run's stderr: %s", &stderr)
	require.Eventually(t, func() bool {
		read, err := os.ReadFile(statusFile)
		var status struct {
			Token *struct {
				ObtainedAt time.Time `json:"obtainedAt"`
				Stale      bool      `json:"stale"`
			} `json:"token"`
		}
		return err == nil && json.Unmarshal(read, &status) == nil && status.Token != nil &&
			!status.Token.ObtainedAt.IsZero() && !status.Token.Stale
	}, 10*time.Second, 50*time.Millisecond, "the token was never obtained; run's stderr: %s", &stderr)
	bindings, err := os.ReadDir(filepath.Join(connector, "bindings"))
	require.NoError(t, err)
	assert.Len(t, bindings, 1, "the connector answered one token, bound to the process's key")

	require.NoError(t, run.Process.Signal(syscall.SIGTERM))
	require.NoError(t, run.Wait(), "SIGTERM exits 0; stderr: %s", &stderr)
	out, err := probe(binary, statusFile)
	require.Error(t, err)
	assert.Contains(t, out, `state is "Stopped"`)
	assert.NotContains(t, stderr.String(), issued.Credential, "the credential reaches no log line")
	assert.NotContains(t, stderr.String(), "reference_bt_", "the token reaches no log line")
	status, err := os.ReadFile(statusFile)
	require.NoError(t, err)
	assert.NotContains(t, string(status), "reference_bt_", "the token reaches no status")
	assert.Contains(t, stderr.String(), `"msg":"bot running"`)
}

func TestGaramBot_AFailedStartWritesItsReasonAsTheTerminationMessage(t *testing.T) {
	binary := buildGaramBotBinary(t)
	for _, tc := range []struct {
		name string
		// bot is the Bot file's content, and botFlag false leaves --bot-file out.
		bot     string
		botFlag bool
		// want is the termination message's prefix, and empty for the control, which starts.
		want string
	}{
		{name: "the control: a Bot starts and writes no message", bot: garamBotMira, botFlag: true},
		{name: "a file that is not a Bot", bot: strings.Replace(garamBotMira, "Bot\n", "Agent\n", 1), botFlag: true,
			want: "BotInvalid: "},
		{name: "no --bot-file", want: "BotInvalid: bot file invalid: --bot-file not given"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			botFile, terminationLog := filepath.Join(directory, "bot.yaml"), filepath.Join(directory, "termination-log")
			statusFile := filepath.Join(directory, "status.json")
			require.NoError(t, os.WriteFile(botFile, []byte(tc.bot), 0o600))
			args := []string{"run", "--status-file", statusFile, "--termination-log", terminationLog}
			if tc.botFlag {
				args = append(args, "--bot-file", botFile)
			}
			run := exec.Command(binary, args...)

			if tc.want == "" {
				require.NoError(t, run.Start())
				t.Cleanup(func() { _ = run.Process.Kill() })
				require.Eventually(t, func() bool { _, err := probe(binary, statusFile); return err == nil },
					10*time.Second, 50*time.Millisecond)
				require.NoError(t, run.Process.Signal(syscall.SIGTERM))
				require.NoError(t, run.Wait())
				_, err := os.Stat(terminationLog)
				assert.ErrorIs(t, err, os.ErrNotExist, "a start that ran writes no termination message")

				return
			}
			err := run.Run()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			assert.Equal(t, 1, exit.ExitCode())
			message, err := os.ReadFile(terminationLog)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(string(message), tc.want), "termination message %q", message)
		})
	}
}
