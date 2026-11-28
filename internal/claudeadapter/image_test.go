//go:build conformance

package claudeadapter_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/conformance"
	"github.com/garamsh/garam-bot/internal/claudeadapter/claudetest"
)

// The files of the container, where a workspace's caller would write them (CRED-1). The directory
// is the one tmpfs the container's user may write, as the volumes of a workspace are.
const (
	adapterTokenPath = v1alpha1.TokenPath
	modelTokenPath   = "/run/garam-bot/model/token"
	toolTokenPath    = "/run/garam-bot/tools/stub/token"
)

// TestImage_PassesTheConformanceSuite runs the suite against the built claude-code image, with the
// real Claude Code CLI in it, a stand-in for the model gateway and an MCP server of one tool. It
// is `make test-claude-code`'s, and needs docker and the image named by CLAUDE_ADAPTER_IMAGE.
func TestImage_PassesTheConformanceSuite(t *testing.T) {
	image := os.Getenv("CLAUDE_ADAPTER_IMAGE")
	if image == "" {
		t.Skip("CLAUDE_ADAPTER_IMAGE names the image to run; `make test-claude-code` builds and names it")
	}
	gateway, tool := &claudetest.Gateway{}, &claudetest.Server{}
	gatewayURL, toolURL := serveOnLoopback(t, gateway), serveOnLoopback(t, tool)
	c := startContainer(t, image)
	c.write(t, adapterTokenPath, "adapter-token")
	c.write(t, modelTokenPath, "model-token-1")
	c.write(t, toolTokenPath, "tool-token-1")
	spec := v1alpha1.SessionSpec{
		Bot:        v1alpha1.Bot{Name: "bot"},
		Model:      v1alpha1.Model{BaseURL: gatewayURL, TokenFile: modelTokenPath},
		Tools:      []v1alpha1.Tool{{Name: "stub", URL: toolURL + "/mcp", TokenFile: toolTokenPath}},
		WorkingDir: "/workspace",
	}
	c.waitReady(t, "adapter-token")

	t.Run("the suite", func(t *testing.T) {
		conformance.Run(t, conformance.Subject{BaseURL: c.url, Token: "adapter-token", Session: spec, Timeout: 3 * time.Minute})
	})

	r := &rig{t: t, url: c.url, token: "adapter-token", spec: spec}
	r.spec.Bot.Name = "extra"

	// MODEL-1: what the suite leaves pending, run here against the harness itself.
	t.Run("MODEL-1 the model token is read for each request", func(t *testing.T) {
		before := len(gateway.Seen())
		r.start("model")
		defer r.do(http.MethodDelete, "sessions/model", nil)
		assert.Equal(t, v1alpha1.OutcomeCompleted, done(t, r.run("model", "one", "Reply with the word ready.")).Outcome)
		c.write(t, modelTokenPath, "model-token-2")
		assert.Equal(t, v1alpha1.OutcomeCompleted, done(t, r.run("model", "two", "Reply with the word ready.")).Outcome)

		var tokens []string
		for _, s := range gateway.Seen()[before:] {
			if s.Path == "/v1/messages" {
				tokens = append(tokens, s.Authorization)
				assert.Empty(t, s.APIKey, "the harness sends no key of its own")
			}
		}
		require.NotEmpty(t, tokens)
		assert.Contains(t, tokens, "Bearer model-token-1")
		assert.Contains(t, tokens, "Bearer model-token-2", "the replaced token is the one sent")
		for _, token := range tokens {
			assert.Contains(t, []string{"Bearer model-token-1", "Bearer model-token-2"}, token, "no other credential reaches the gateway")
		}
	})

	// TOOL-1.
	t.Run("TOOL-1 the harness calls the session's tool with its token", func(t *testing.T) {
		before := len(tool.Seen())
		r.start("tool")
		defer r.do(http.MethodDelete, "sessions/tool", nil)
		events := r.run("tool", "one", claudetest.ToolText)

		var call, result bool
		for _, e := range events {
			call = call || e.Type == v1alpha1.EventToolCall && e.Call.Tool == "stub" && e.Call.Name == "echo"
			result = result || e.Type == v1alpha1.EventToolResult && !e.Result.IsError
		}
		assert.True(t, call, "events %+v", events)
		assert.True(t, result, "events %+v", events)
		seen := tool.Seen()[before:]
		require.NotEmpty(t, seen)
		for _, s := range seen {
			assert.Equal(t, "Bearer tool-token-1", s.Authorization)
			assert.Equal(t, "/mcp", s.Path)
		}
	})
}

// serveOnLoopback serves h where the container, which shares the host's network, reaches it, and
// returns its base URL. A host's firewall may drop what a container sends to the host from the
// bridge, so the container does not use the bridge.
func serveOnLoopback(t *testing.T, h http.Handler) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	return fmt.Sprintf("http://127.0.0.1:%d", l.Addr().(*net.TCPAddr).Port)
}

type container struct {
	name string
	url  string
}

// startContainer runs the image as a workspace's Pod would: as the image's own non-root user, with
// its token directory and its data volume writable by that user and nothing else.
func startContainer(t *testing.T, image string) *container {
	t.Helper()
	c := &container{name: fmt.Sprintf("claude-adapter-test-%d", time.Now().UnixNano())}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", c.name).Run() })
	out, err := exec.Command("docker", "run", "-d", "--name", c.name,
		"--network", "host",
		"--tmpfs", "/run/garam-bot:uid=65532,gid=65532,mode=0700",
		"--tmpfs", "/workspace:uid=65532,gid=65532,mode=0755",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		image).CombinedOutput()
	require.NoError(t, err, string(out))
	c.url = fmt.Sprintf("http://127.0.0.1:%d", v1alpha1.Port)

	return c
}

// write puts content in the file, as the container's user, which is how a caller's WriteFile
// reaches it.
func (c *container) write(t *testing.T, path, content string) {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", c.name, "sh", "-c", `umask 077 && mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", path)
	cmd.Stdin = strings.NewReader(content + "\n")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// waitReady waits for the adapter to answer info.
func (c *container) waitReady(t *testing.T, token string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+v1alpha1.PathPrefix+"info", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-ctx.Done():
			logs, _ := exec.Command("docker", "logs", c.name).CombinedOutput()
			t.Fatalf("the adapter did not answer; its log:\n%s", logs)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
