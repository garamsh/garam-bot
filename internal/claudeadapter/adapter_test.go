package claudeadapter_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/conformance"
	"github.com/garamsh/garam-bot/internal/claudeadapter"
	"github.com/garamsh/garam-bot/internal/claudeadapter/claudetest"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		os.Exit(fakeClaude(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// rig is an adapter running the fake harness, with stand-ins for its gateway and tool server.
type rig struct {
	t       *testing.T
	url     string
	token   string
	files   string
	state   string
	gateway *claudetest.Gateway
	tool    *claudetest.Server
	spec    v1alpha1.SessionSpec
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, token: "adapter-token", files: t.TempDir(), state: t.TempDir(), gateway: &claudetest.Gateway{}, tool: &claudetest.Server{}}
	gateway := httptest.NewServer(r.gateway)
	tool := httptest.NewServer(r.tool)
	t.Cleanup(gateway.Close)
	t.Cleanup(tool.Close)
	r.write("adapter", r.token)
	r.write("model", "model-token-1")
	r.write("tool", "tool-token-1")
	r.spec = v1alpha1.SessionSpec{
		Bot:        v1alpha1.Bot{Name: "bot"},
		Model:      v1alpha1.Model{BaseURL: gateway.URL, TokenFile: filepath.Join(r.files, "model")},
		Tools:      []v1alpha1.Tool{{Name: "stub", URL: tool.URL, TokenFile: filepath.Join(r.files, "tool")}},
		WorkingDir: t.TempDir(),
	}
	adapter := claudeadapter.New(claudeadapter.Config{
		Command:  os.Args[0],
		Token:    func() ([]byte, error) { return os.ReadFile(filepath.Join(r.files, "adapter")) },
		Version:  "9.9.9",
		StateDir: r.state,
		Env:      []string{fakeEnv + "=1"},
	})
	srv := httptest.NewServer(adapter)
	t.Cleanup(srv.Close)
	r.url = srv.URL

	return r
}

// write replaces the token file name.
func (r *rig) write(name, content string) {
	r.t.Helper()
	require.NoError(r.t, os.WriteFile(filepath.Join(r.files, name), []byte(content+"\n"), 0o600))
}

// do sends a request with the adapter's token and returns the status and body.
func (r *rig) do(method, path string, body any) (int, []byte) {
	r.t.Helper()

	return r.doAs(r.token, method, path, body)
}

func (r *rig) doAs(token, method, path string, body any) (int, []byte) {
	r.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(r.t, err)
	}
	req, err := http.NewRequest(method, r.url+v1alpha1.PathPrefix+path, bytes.NewReader(raw))
	require.NoError(r.t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	defer resp.Body.Close()
	var out bytes.Buffer
	_, err = out.ReadFrom(resp.Body)
	require.NoError(r.t, err)

	return resp.StatusCode, out.Bytes()
}

func (r *rig) start(id string) {
	r.t.Helper()
	status, body := r.do(http.MethodPut, "sessions/"+id, r.spec)
	require.Equal(r.t, http.StatusCreated, status, string(body))
}

// run gives the session a task of one input and returns the task's events, after it ended.
func (r *rig) run(session, task, input string) []v1alpha1.Event {
	r.t.Helper()
	status, body := r.do(http.MethodPost, "sessions/"+session+"/tasks", v1alpha1.TaskRequest{ID: task, Input: []v1alpha1.Input{{Text: input}}})
	require.Equal(r.t, http.StatusAccepted, status, string(body))
	var events []v1alpha1.Event
	var after uint64
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		status, body := r.do(http.MethodGet, "sessions/"+session+"/events?wait=5&after="+itoa(after), nil)
		require.Equal(r.t, http.StatusOK, status, string(body))
		var page v1alpha1.EventPage
		require.NoError(r.t, json.Unmarshal(body, &page))
		after = page.Next
		for _, e := range page.Events {
			if e.Task == task {
				events = append(events, e)
			}
			if e.Task == task && e.Type == v1alpha1.EventTaskDone {
				return events
			}
		}
	}
	r.t.Fatalf("task %s did not end; events %+v", task, events)

	return nil
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)

	return string(b)
}

// said returns the text of the task's only message.
func said(t *testing.T, events []v1alpha1.Event) string {
	t.Helper()
	var texts []string
	for _, e := range events {
		if e.Type == v1alpha1.EventMessage {
			texts = append(texts, e.Text)
		}
	}
	require.Len(t, texts, 1, "events %+v", events)

	return texts[0]
}

func done(t *testing.T, events []v1alpha1.Event) v1alpha1.Event {
	t.Helper()
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, v1alpha1.EventTaskDone, last.Type)

	return last
}

func TestAdapter_PassesTheConformanceSuite(t *testing.T) {
	r := newRig(t)
	conformance.Run(t, conformance.Subject{BaseURL: r.url, Token: r.token, Session: r.spec, Timeout: time.Minute})
}

func TestAdapter_AnswersInfo(t *testing.T) {
	r := newRig(t)
	status, body := r.do(http.MethodGet, "info", nil)
	require.Equal(t, http.StatusOK, status)
	var info v1alpha1.Info
	require.NoError(t, json.Unmarshal(body, &info))
	assert.Equal(t, v1alpha1.Info{Harness: "claude-code", Version: "9.9.9", SpecRelease: v1alpha1.SpecRelease}, info)
}

func TestAdapter_ReadsItsTokenAtEachRequest(t *testing.T) {
	r := newRig(t)
	status, _ := r.do(http.MethodGet, "info", nil)
	require.Equal(t, http.StatusOK, status)

	r.write("adapter", "replaced-token")
	status, _ = r.doAs("adapter-token", http.MethodGet, "info", nil)
	assert.Equal(t, http.StatusUnauthorized, status, "the token before the replacement")
	status, _ = r.doAs("replaced-token", http.MethodGet, "info", nil)
	assert.Equal(t, http.StatusOK, status, "the token after the replacement")

	r.write("adapter", "")
	status, _ = r.doAs("", http.MethodGet, "info", nil)
	assert.Equal(t, http.StatusUnauthorized, status, "an empty token file takes no request")
	require.NoError(t, os.Remove(filepath.Join(r.files, "adapter")))
	status, _ = r.doAs("replaced-token", http.MethodGet, "info", nil)
	assert.Equal(t, http.StatusUnauthorized, status, "a missing token file takes no request")
}

func TestAdapter_SendsTheModelTokenReadAtEachRequest(t *testing.T) {
	r := newRig(t)
	r.start("s")
	r.write("model", "model-token-1")
	assert.Equal(t, "200 ", said(t, r.run("s", "one", askGateway)))
	r.write("model", "model-token-2")
	assert.Equal(t, "200 ", said(t, r.run("s", "two", askGateway)))

	seen := r.gateway.Seen()
	require.Len(t, seen, 2)
	assert.Equal(t, "Bearer model-token-1", seen[0].Authorization)
	assert.Equal(t, "Bearer model-token-2", seen[1].Authorization)
	assert.Empty(t, seen[0].APIKey, "the key the harness sent is not forwarded")
}

func TestAdapter_SendsEachToolTheTokenReadAtEachRequest(t *testing.T) {
	r := newRig(t)
	r.start("s")
	assert.Equal(t, "200 ", said(t, r.run("s", "one", askTool)))
	r.write("tool", "tool-token-2")
	assert.Equal(t, "200 ", said(t, r.run("s", "two", askTool)))

	seen := r.tool.Seen()
	require.Len(t, seen, 2)
	assert.Equal(t, "Bearer tool-token-1", seen[0].Authorization)
	assert.Equal(t, "Bearer tool-token-2", seen[1].Authorization)
	assert.Empty(t, r.gateway.Seen(), "a tool's token goes to the tool only")
}

func TestAdapter_ToolProxyServesOnlyTheToolsURL(t *testing.T) {
	r := newRig(t)
	r.start("s")

	assert.Equal(t, "403 ", said(t, r.run("s", "one", askToolPath)))
	assert.Empty(t, r.tool.Seen(), "no other path of the server is reached")
	assert.Equal(t, "200 ", said(t, r.run("s", "two", askTool)), "the control: its URL is")
}

func TestAdapter_AnswersNothingWhenAnUpstreamTokenCannotBeRead(t *testing.T) {
	r := newRig(t)
	r.start("s")
	require.NoError(t, os.Remove(filepath.Join(r.files, "model")))
	assert.Equal(t, "502 ", said(t, r.run("s", "one", askGateway)))
	assert.Empty(t, r.gateway.Seen(), "a request without a token is not sent on")
}

func TestAdapter_ProxiesForwardOnlyToTheirUpstream(t *testing.T) {
	r := newRig(t)
	foreign := &claudetest.Recorder{}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { foreign.Record(req) }))
	t.Cleanup(other.Close)
	r.start("s")

	got := said(t, r.run("s", "one", askProbe+strings.TrimPrefix(other.URL, "http://")))

	// Each of the first four is refused for its own reason, and the last, a proper request, is
	// forwarded: so the refusals are not a proxy that refuses everything.
	assert.Equal(t, "403 403 403 403 200", got)
	assert.Empty(t, foreign.Seen(), "nothing reached the other server")
	require.Len(t, r.gateway.Seen(), 1, "only the proper request reached the upstream")
	assert.Equal(t, "/v1/messages", r.gateway.Seen()[0].Path)
}

func TestAdapter_ProxiesDoNotFollowARedirect(t *testing.T) {
	r := newRig(t)
	foreign := &claudetest.Recorder{}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { foreign.Record(req) }))
	t.Cleanup(other.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL+"/elsewhere", http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	r.spec.Model.BaseURL = redirecting.URL
	r.start("s")

	got := said(t, r.run("s", "one", askRedirect))

	assert.Equal(t, "302 "+other.URL+"/elsewhere", got, "the harness is handed the redirect")
	assert.Empty(t, foreign.Seen(), "the proxy did not follow it")
}

func TestAdapter_GivesTheHarnessNoCredentialOfItsOwn(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "a-key-of-the-workspace")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "a-token-of-the-workspace")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "a-secret-of-the-workspace")
	r := newRig(t)
	r.start("s")

	var seen harnessSeen
	require.NoError(t, json.Unmarshal([]byte(said(t, r.run("s", "one", askEnv))), &seen))

	env := strings.Join(seen.Env, "\n")
	assert.NotContains(t, env, "of-the-workspace")
	assert.NotContains(t, env, "model-token", "the model token is the proxy's, and not the harness's")
	assert.NotContains(t, env, "tool-token")
	assert.Contains(t, env, "ANTHROPIC_AUTH_TOKEN=garam-bot-proxy", "the control: the harness has its placeholder")
	var base string
	for _, kv := range seen.Env {
		if v, ok := strings.CutPrefix(kv, "ANTHROPIC_BASE_URL="); ok {
			base = v
		}
	}
	assert.NotEqual(t, r.spec.Model.BaseURL, base, "the harness is pointed at the proxy and not at the gateway")
	host, _, err := net.SplitHostPort(strings.TrimPrefix(base, "http://"))
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
}

func TestAdapter_ConfiguresOnlyTheSessionsToolServers(t *testing.T) {
	r := newRig(t)
	second := httptest.NewServer(&claudetest.Server{})
	t.Cleanup(second.Close)
	r.spec.Tools = append(r.spec.Tools, v1alpha1.Tool{Name: "second", URL: second.URL, TokenFile: filepath.Join(r.files, "tool")})
	r.start("s")

	var seen harnessSeen
	require.NoError(t, json.Unmarshal([]byte(said(t, r.run("s", "one", askEnv))), &seen))

	var cfg struct {
		Servers map[string]struct{ Type, URL string } `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(seen.MCP), &cfg))
	assert.Len(t, cfg.Servers, 2)
	for name, s := range cfg.Servers {
		assert.Contains(t, []string{"stub", "second"}, name)
		assert.Equal(t, "http", s.Type)
		assert.True(t, strings.HasPrefix(s.URL, "http://127.0.0.1:"), "%s is reached through a loopback proxy, got %s", name, s.URL)
	}
	assert.Contains(t, seen.Args, "--strict-mcp-config", "the harness takes no server of its own")
}

func TestAdapter_ReportsToolCallsAndTheirResults(t *testing.T) {
	r := newRig(t)
	r.start("s")

	events := r.run("s", "one", askToolCalls)

	var types []v1alpha1.EventType
	for _, e := range events {
		types = append(types, e.Type)
	}
	assert.Equal(t, []v1alpha1.EventType{v1alpha1.EventToolCall, v1alpha1.EventToolCall, v1alpha1.EventToolResult, v1alpha1.EventToolResult, v1alpha1.EventMessage, v1alpha1.EventTaskDone}, types)
	assert.Equal(t, &v1alpha1.ToolCall{ID: "toolu_1", Tool: "stub", Name: "echo", Input: json.RawMessage(`{"text":"hello"}`)}, events[0].Call)
	assert.Equal(t, &v1alpha1.ToolCall{ID: "toolu_2", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}, events[1].Call)
	assert.Equal(t, &v1alpha1.ToolResult{ID: "toolu_1"}, events[2].Result)
	assert.Equal(t, &v1alpha1.ToolResult{ID: "toolu_2", IsError: true}, events[3].Result)
	assert.Equal(t, v1alpha1.OutcomeCompleted, done(t, events).Outcome)
}

func TestAdapter_FailsATaskTheHarnessEndsWithAnError(t *testing.T) {
	r := newRig(t)
	r.start("s")

	end := done(t, r.run("s", "one", askFail))

	assert.Equal(t, v1alpha1.OutcomeFailed, end.Outcome)
	assert.Equal(t, "the model gateway refused", end.Reason)
}

func TestAdapter_FailsATaskTheHarnessDiesOn(t *testing.T) {
	r := newRig(t)
	r.start("s")

	end := done(t, r.run("s", "one", askCrash))

	assert.Equal(t, v1alpha1.OutcomeFailed, end.Outcome)
	assert.Contains(t, end.Reason, "the harness fell over", "the harness's last words")
}

func TestAdapter_ContinuesTheHarnessConversationOfACompletedTask(t *testing.T) {
	r := newRig(t)
	r.start("s")
	args := func(task string) harnessSeen {
		var seen harnessSeen
		require.NoError(t, json.Unmarshal([]byte(said(t, r.run("s", task, askEnv))), &seen))

		return seen
	}

	first, second := args("one"), args("two")

	id := flagValue(first.Args, "--session-id")
	assert.NotEmpty(t, id)
	assert.Empty(t, flagValue(first.Args, "--resume"))
	assert.Equal(t, id, flagValue(second.Args, "--resume"), "the second task continues the first's conversation")
}

func TestAdapter_StartsAFreshConversationAfterATaskThatNeverFinished(t *testing.T) {
	r := newRig(t)
	r.start("s")
	crashed := done(t, r.run("s", "one", askCrash))
	require.Equal(t, v1alpha1.OutcomeFailed, crashed.Outcome)

	var seen harnessSeen
	require.NoError(t, json.Unmarshal([]byte(said(t, r.run("s", "two", askEnv))), &seen))

	assert.NotEmpty(t, flagValue(seen.Args, "--session-id"), "there is no conversation to resume")
	assert.Empty(t, flagValue(seen.Args, "--resume"))
}

func TestAdapter_StopEndsAHeldTaskAndTheNextRuns(t *testing.T) {
	r := newRig(t)
	r.start("s")
	status, _ := r.do(http.MethodPost, "sessions/s/tasks", v1alpha1.TaskRequest{ID: "held", Input: []v1alpha1.Input{{Text: claudetest.HoldText}}})
	require.Equal(t, http.StatusAccepted, status)
	status, _ = r.do(http.MethodPost, "sessions/s/tasks/held/stop", nil)
	require.Equal(t, http.StatusAccepted, status)

	assert.Equal(t, "echo: after", said(t, r.run("s", "next", "after")))
}

func TestAdapter_DeleteEndsTheSessionsHarnessProxiesAndFiles(t *testing.T) {
	r := newRig(t)
	r.start("s")
	var seen harnessSeen
	require.NoError(t, json.Unmarshal([]byte(said(t, r.run("s", "one", askEnv))), &seen))
	var proxy string
	for _, kv := range seen.Env {
		if v, ok := strings.CutPrefix(kv, "ANTHROPIC_BASE_URL=http://"); ok {
			proxy = v
		}
	}
	require.NotEmpty(t, proxy)
	conn, err := net.Dial("tcp", proxy)
	require.NoError(t, err, "the control: the proxy listens while the session lives")
	conn.Close()
	entries, err := os.ReadDir(r.state)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "the control: the session keeps files while it lives")

	status, _ := r.do(http.MethodDelete, "sessions/s", nil)
	require.Equal(t, http.StatusNoContent, status)

	_, err = net.Dial("tcp", proxy)
	assert.Error(t, err, "the proxy is closed")
	entries, err = os.ReadDir(r.state)
	require.NoError(t, err)
	assert.Empty(t, entries, "the session's files are removed")
}
