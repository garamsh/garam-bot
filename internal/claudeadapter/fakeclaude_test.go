package claudeadapter_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/garamsh/garam-bot/internal/claudeadapter/claudetest"
)

// fakeEnv makes the test binary act as the Claude Code executable: the adapter runs it as the
// harness and reads the stream-json it prints, which is the part of Claude Code the adapter
// depends on. What the real harness does is `make test-claude-code`'s to show.
const message = `{"messages":[{"role":"user","content":"hello"}]}`

const fakeEnv = "CLAUDEADAPTER_FAKE_CLAUDE"

// The prompts the fake harness acts on. Any other prompt it echoes.
const (
	askEnv       = "ask: environment"
	askGateway   = "ask: gateway"
	askTool      = "ask: tool"
	askToolPath  = "ask: tool path"
	askProbe     = "ask: probe " // followed by the address of a server that must never be reached
	askRedirect  = "ask: redirect"
	askToolCalls = "ask: tool calls"
	askFail      = "ask: fail"
	askCrash     = "ask: crash"
)

// harnessSeen is what the fake harness reports of its run for askEnv.
type harnessSeen struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
	MCP  string   `json:"mcp"`
}

func fakeClaude(args []string, in io.Reader, out, errOut io.Writer) int {
	raw, _ := io.ReadAll(in)
	prompt := string(raw)
	base := os.Getenv("ANTHROPIC_BASE_URL")
	say := func(text string) {
		emit(out, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
	}
	switch {
	case prompt == claudetest.HoldText:
		time.Sleep(time.Hour)
	case prompt == askEnv:
		mcp, _ := os.ReadFile(flagValue(args, "--mcp-config"))
		seen, _ := json.Marshal(harnessSeen{Args: args, Env: os.Environ(), MCP: string(mcp)})
		say(string(seen))
	case prompt == askGateway:
		say(post(base+"/v1/messages", message, http.Header{"X-Api-Key": {"the harness's own key"}, "Authorization": {"Bearer the harness's own token"}}))
	case prompt == askTool:
		var cfg struct {
			Servers map[string]struct {
				URL string `json:"url"`
			} `json:"mcpServers"`
		}
		mcp, _ := os.ReadFile(flagValue(args, "--mcp-config"))
		_ = json.Unmarshal(mcp, &cfg)
		say(post(cfg.Servers["stub"].URL, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil))
	case prompt == askToolPath:
		var cfg struct {
			Servers map[string]struct {
				URL string `json:"url"`
			} `json:"mcpServers"`
		}
		mcp, _ := os.ReadFile(flagValue(args, "--mcp-config"))
		_ = json.Unmarshal(mcp, &cfg)
		say(post(cfg.Servers["stub"].URL+"other", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil))
	case strings.HasPrefix(prompt, askProbe):
		say(probe(base, strings.TrimPrefix(prompt, askProbe)))
	case prompt == askRedirect:
		say(post(base+"/v1/messages", message, nil))
	case prompt == askToolCalls:
		emit(out, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "mcp__stub__echo", "input": map[string]any{"text": "hello"}},
			map[string]any{"type": "tool_use", "id": "toolu_2", "name": "Bash", "input": map[string]any{"command": "ls"}},
		}}})
		emit(out, map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "echo"},
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": "no", "is_error": true},
		}}})
		say("done")
	case prompt == askFail:
		emit(out, map[string]any{"type": "result", "subtype": "success", "is_error": true, "result": "the model gateway refused"})
		return 1
	case prompt == askCrash:
		fmt.Fprint(errOut, "the harness fell over")
		return 3
	default:
		say("echo: " + prompt)
	}
	emit(out, map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "ok"})

	return 0
}

func emit(w io.Writer, v any) { _ = json.NewEncoder(w).Encode(v) }

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}

	return ""
}

// post sends a POST the way a harness does, and says what came back. It does not follow a
// redirect.
func post(target, body string, header http.Header) string {
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return "error: " + err.Error()
	}
	for k, v := range header {
		req.Header[k] = v
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "error: " + err.Error()
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	return fmt.Sprintf("%d %s", resp.StatusCode, resp.Header.Get("Location"))
}

// probe asks the model proxy at base for things it must not do, each on its own connection, then
// for one thing it must. Each attempt differs from a proper request in one way only, so that the
// status says which check refused it. It answers the statuses in order, foreign being the address
// of a server none of them may reach.
func probe(base, foreign string) string {
	u, _ := url.Parse(base)
	own := u.Host
	body := message
	attempts := []string{
		// A target of its own, in the absolute form of a proxy request.
		"GET http://" + foreign + "/v1/messages HTTP/1.1\r\nHost: " + own + "\r\n\r\n",
		// A tunnel: the target of a CONNECT has no path.
		"CONNECT " + own + " HTTP/1.1\r\nHost: " + own + "\r\n\r\n",
		// A Host that is not the proxy's.
		"POST /v1/messages HTTP/1.1\r\nHost: " + foreign + "\r\nContent-Length: 2\r\n\r\n{}",
		// A path that climbs.
		"POST /v1/../v1/messages HTTP/1.1\r\nHost: " + own + "\r\nContent-Length: 2\r\n\r\n{}",
		// The control: a proper request, which is forwarded.
		"POST /v1/messages HTTP/1.1\r\nHost: " + own + "\r\nContent-Length: " + fmt.Sprint(len(body)) + "\r\n\r\n" + body,
	}
	var statuses []string
	for _, a := range attempts {
		conn, err := net.DialTimeout("tcp", own, 5*time.Second)
		if err != nil {
			return "error: " + err.Error()
		}
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
		_, _ = io.WriteString(conn, a)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil {
			statuses = append(statuses, "error")
			continue
		}
		resp.Body.Close()
		statuses = append(statuses, fmt.Sprint(resp.StatusCode))
	}

	return strings.Join(statuses, " ")
}
