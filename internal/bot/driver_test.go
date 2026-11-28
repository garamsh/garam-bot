package bot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/agentfake"
	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	"github.com/garamsh/garam-bot/internal/bot"
)

// The driver is checked against agentfake, interface 2's reference adapter, served behind a place
// that stands in for the workspace: its dial reaches the adapter, and its adapter token is the one
// agentfake takes.

const adapterToken = "adapter-token-0123456789abcdef0123456789"

// adapterPlace serves agentfake and dials it, counting the dials.
type adapterPlace struct {
	t      *testing.T
	server *httptest.Server
	dials  atomic.Int32
	ready  atomic.Bool
	refuse atomic.Bool
	// answered counts the dials answered, as a 101 answers one.
	answered atomic.Int32
	// noHTTP makes each dial answer bytes that are no HTTP, and hold the connection open.
	noHTTP atomic.Bool
	// holding is the number of events reads the adapter holds open, held how many it has held, and
	// broken how many it had held at the test's last break.
	holding, held atomic.Int32
	broken        int32
	space         access.Workspace
}

func startAdapter(t *testing.T) *adapterPlace {
	t.Helper()
	p := &adapterPlace{t: t, space: access.Workspace{
		Attachment: attachment,
		Model:      access.Model{BaseURL: "https://gateway.place.test/v1"},
		Tools:      []access.Tool{{Name: tool, URL: "https://tools.place.test/" + tool}},
	}}
	fake := agentfake.New(func() ([]byte, error) { return []byte(adapterToken), nil })
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events") && r.URL.Query().Get("wait") != "0" {
			p.held.Add(1)
			p.holding.Add(1)
			defer p.holding.Add(-1)
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(p.server.Close)
	p.ready.Store(true)
	return p
}

func (p *adapterPlace) Dial(ctx context.Context) (net.Conn, error) {
	p.dials.Add(1)
	if p.refuse.Load() {
		return nil, errors.New("the workspace's adapter port is closed")
	}
	if p.noHTTP.Load() {
		ours, theirs := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, theirs) }()
		go func() { _, _ = theirs.Write([]byte("not an HTTP answer\r\n\r\n")) }()
		p.t.Cleanup(func() { _ = theirs.Close() })
		return ours, nil
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", p.server.Listener.Addr().String())
	if err == nil {
		p.answered.Add(1)
	}
	return conn, err
}

func (p *adapterPlace) AdapterToken() string { return adapterToken }

func (p *adapterPlace) Session() (access.Workspace, bool) { return p.space, p.ready.Load() }

// call makes one request of the adapter as the test, with the adapter's token, and answers the status
// and the body.
func (p *adapterPlace) call(method, path string, body any) (int, []byte) {
	p.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(p.t, err)
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(p.t.Context(), method, p.server.URL+agent.PathPrefix+path, reader)
	require.NoError(p.t, err)
	request.Header.Set("Authorization", "Bearer "+adapterToken)
	response, err := p.server.Client().Do(request)
	require.NoError(p.t, err)
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	require.NoError(p.t, err)
	return response.StatusCode, data
}

// session reads the session bot, and answers false where the adapter holds none.
func (p *adapterPlace) session() (agent.Session, bool) {
	p.t.Helper()
	status, data := p.call(http.MethodGet, "sessions/bot", nil)
	if status == http.StatusNotFound {
		return agent.Session{}, false
	}
	require.Equal(p.t, http.StatusOK, status, string(data))
	var session agent.Session
	require.NoError(p.t, json.Unmarshal(data, &session))
	return session, true
}

// events reads every event of the session bot.
func (p *adapterPlace) events() []agent.Event {
	p.t.Helper()
	status, data := p.call(http.MethodGet, "sessions/bot/events?after=0&wait=0", nil)
	require.Equal(p.t, http.StatusOK, status, string(data))
	var page struct {
		Events []agent.Event `json:"events"`
	}
	require.NoError(p.t, json.Unmarshal(data, &page))
	return page.Events
}

// awaitHeldRead waits for an events read of the driver's held open, one begun since the last break.
func (p *adapterPlace) awaitHeldRead() {
	p.t.Helper()
	require.Eventually(p.t, func() bool { return p.held.Load() > p.broken && p.holding.Load() > 0 }, 5*time.Second, time.Millisecond,
		"no events read held")
}

// breakHeldRead waits for a held events read, then breaks every connection, as a restart would.
func (p *adapterPlace) breakHeldRead() {
	p.t.Helper()
	p.awaitHeldRead()
	p.broken = p.held.Load()
	p.server.CloseClientConnections()
}

// polls counts the inbox's reads that found nothing, each followed by the 10 milliseconds' wait.
func polls(waits *sleeps) int {
	var n int
	for _, d := range waits.recorded() {
		if d == 10*time.Millisecond {
			n++
		}
	}
	return n
}

func driverBot(harness string) *v1alpha1.Bot {
	return &v1alpha1.Bot{
		ObjectMeta: metav1.ObjectMeta{Name: "mira", Namespace: team},
		Spec: v1alpha1.BotSpec{
			Persona: v1alpha1.Persona{Role: "Game Lead", Instructions: "Keep the release notes short."},
			Agent:   v1alpha1.Agent{Harness: harness},
		},
	}
}

// logs is what the driver logs, kept to be read.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// runDriver runs a Driver of bot at p until the test ends.
func runDriver(t *testing.T, p *adapterPlace, b *v1alpha1.Bot, inbox string, waits *sleeps, out *logs) *bot.Driver {
	t.Helper()
	driver, err := bot.NewDriver(bot.DriverConfig{
		Bot: b, Place: p, InboxDir: inbox, InboxPoll: 10 * time.Millisecond, Sleep: waits.sleep,
		Logger: slog.New(slog.NewTextHandler(out, nil)),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		driver.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return driver
}

// backoffs are the waits of a second or more: the retries', not the inbox's polls.
func backoffs(waits *sleeps) []time.Duration {
	var kept []time.Duration
	for _, d := range waits.recorded() {
		if d >= time.Second {
			kept = append(kept, d)
		}
	}
	return kept
}

func TestDriver_OpensTheSessionAndGivesEachMessageAsATaskInTurnRemovingItOnceDone(t *testing.T) {
	p := startAdapter(t)
	p.ready.Store(false)
	inbox := t.TempDir()
	writeFile(t, filepath.Join(inbox, "002-second"), "and the patch notes")
	writeFile(t, filepath.Join(inbox, "001-first"), "write the release notes")
	writeFile(t, filepath.Join(inbox, ".003-partial"), "still being written")
	writeFile(t, filepath.Join(inbox, "004 not a task id"), "skipped")
	out := &logs{}
	waits := &sleeps{}
	driver := runDriver(t, p, driverBot(agentfake.Harness), inbox, waits, out)

	// Control: no session is opened before the workspace runs with its tokens written.
	require.Eventually(t, func() bool { return driver.Status().Reason == bot.ReasonWaitingForWorkspace }, 5*time.Second, time.Millisecond)
	_, opened := p.session()
	assert.False(t, opened, "a session opened before the workspace's tokens are written")
	p.ready.Store(true)

	require.Eventually(t, func() bool { return driver.Status().Completed == 2 }, 5*time.Second, time.Millisecond)
	after := polls(waits)
	require.Eventually(t, func() bool { return polls(waits) > after+1 }, 5*time.Second, time.Millisecond,
		"the inbox is not read again and found empty of tasks")
	status := driver.Status()
	assert.Equal(t, bot.SessionStatus{
		State: agent.SessionIdle, Completed: 2,
		LastTask: &bot.TaskStatus{ID: "002-second", Outcome: agent.OutcomeCompleted},
	}, status)

	session, opened := p.session()
	require.True(t, opened)
	assert.Equal(t, agent.SessionSpec{
		Bot:          agent.Bot{Name: "mira"},
		Instructions: "Keep the release notes short.",
		Model:        agent.Model{BaseURL: "https://gateway.place.test/v1", TokenFile: bot.ModelTokenPath},
		Tools:        []agent.Tool{{Name: tool, URL: "https://tools.place.test/" + tool, TokenFile: bot.ToolTokenPath(tool)}},
	}, session.Spec)

	var order []string
	for _, event := range p.events() {
		order = append(order, event.Task+" "+string(event.Type)+" "+event.Text)
	}
	assert.Equal(t, []string{
		"001-first message write the release notes", "001-first taskDone ",
		"002-second message and the patch notes", "002-second taskDone ",
	}, order, "the messages are given by name, each after the last is done, its content the input")

	entries, err := os.ReadDir(inbox)
	require.NoError(t, err)
	var left []string
	for _, entry := range entries {
		left = append(left, entry.Name())
	}
	assert.Equal(t, []string{".003-partial", "004 not a task id"}, left, "a done task's message is removed, and no other")

	for _, secret := range []string{adapterToken, "release notes", "patch notes"} {
		assert.NotContains(t, out.String(), secret, "the log carries no token and no input or event content")
	}
}

func TestDriver_OpensNoSessionOnAnAdapterOfAnotherHarness(t *testing.T) {
	p := startAdapter(t)
	inbox := t.TempDir()
	writeFile(t, filepath.Join(inbox, "001"), "write the release notes")
	driver := runDriver(t, p, driverBot("claude-code"), inbox, &sleeps{}, &logs{})

	require.Eventually(t, func() bool { return driver.Status().Reason == bot.ReasonHarnessMismatch }, 5*time.Second, time.Millisecond)
	_, opened := p.session()
	assert.False(t, opened, "a session opened on an adapter of another harness")
	assert.FileExists(t, filepath.Join(inbox, "001"), "no task is given")
	// The control is the case above: the adapter of the Bot's harness is given the session.
}

func TestDriver_DeletesASessionHeldUnderAnotherSpecAndOpensItAgain(t *testing.T) {
	p := startAdapter(t)
	earlier := agent.SessionSpec{
		Bot:   agent.Bot{Name: "mira"},
		Model: agent.Model{BaseURL: "https://gateway.place.test/old", TokenFile: bot.ModelTokenPath},
	}
	status, data := p.call(http.MethodPut, "sessions/bot", earlier)
	require.Equal(t, http.StatusCreated, status, string(data))
	status, data = p.call(http.MethodPost, "sessions/bot/tasks", agent.TaskRequest{ID: "old", Input: []agent.Input{{Text: "an earlier task"}}})
	require.Equal(t, http.StatusAccepted, status, string(data))
	inbox := t.TempDir()
	writeFile(t, filepath.Join(inbox, "001"), "write the release notes")
	driver := runDriver(t, p, driverBot(agentfake.Harness), inbox, &sleeps{}, &logs{})

	require.Eventually(t, func() bool { return driver.Status().Completed == 1 }, 5*time.Second, time.Millisecond)
	session, opened := p.session()
	require.True(t, opened)
	assert.Equal(t, "https://gateway.place.test/v1", session.Spec.Model.BaseURL, "the session holds the Bot's spec")
	var tasks []string
	for _, event := range p.events() {
		tasks = append(tasks, event.Task)
	}
	assert.Equal(t, []string{"001", "001"}, tasks, "the session opened again numbers its events from 1 and holds none of the earlier one's")
}

func TestDriver_DialsAgainAfterABrokenConnectionFromOneSecondDoublingTo60ResetByA101(t *testing.T) {
	p := startAdapter(t)
	inbox := t.TempDir()
	writeFile(t, filepath.Join(inbox, "001"), agentfake.HoldText)
	writeFile(t, filepath.Join(inbox, "002"), "write the release notes")
	waits := &sleeps{}
	driver := runDriver(t, p, driverBot(agentfake.Harness), inbox, waits, &logs{})

	working := func() bool { s, ok := p.session(); return ok && s.State == agent.SessionWorking }
	require.Eventually(t, working, 5*time.Second, time.Millisecond)
	assert.Empty(t, backoffs(waits), "control: no wait before a connection breaks")

	// Two breaks of the held events read: each is dialled again after a second, the dial between
	// answered 101.
	p.breakHeldRead()
	p.breakHeldRead()
	p.awaitHeldRead()
	require.Len(t, backoffs(waits), 2)

	// A break whose dials then fail: the waits double while they fail, and a dial answered starts
	// them again from a second.
	p.refuse.Store(true)
	p.breakHeldRead()
	require.Eventually(t, func() bool { return len(backoffs(waits)) >= 9 }, 5*time.Second, time.Millisecond)
	answered := p.answered.Load()
	p.refuse.Store(false)
	require.Eventually(t, func() bool { return p.answered.Load() > answered }, 5*time.Second, time.Millisecond)
	p.awaitHeldRead()
	recovered := len(backoffs(waits))
	p.breakHeldRead()
	require.Eventually(t, func() bool { return len(backoffs(waits)) > recovered }, 5*time.Second, time.Millisecond)
	got := backoffs(waits)
	s := time.Second
	assert.Equal(t, []time.Duration{s, s, s, 2 * s, 4 * s, 8 * s, 16 * s, 32 * s, 60 * s}, got[:9])
	assert.Equal(t, s, got[recovered], "the wait after a 101 starts again from a second")
	assert.Equal(t, 0, driver.Status().Stopped, "control: the task has not ended")
	for _, event := range p.events() {
		assert.Equal(t, "001", event.Task, "a task given while another runs")
	}

	status, data := p.call(http.MethodPost, "sessions/bot/tasks/001/stop", nil)
	require.Equal(t, http.StatusAccepted, status, string(data))
	require.Eventually(t, func() bool { return driver.Status().Completed == 1 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, 1, driver.Status().Stopped, "the events are read on to the held task's end")
	assert.NoFileExists(t, filepath.Join(inbox, "001"))
	var done []string
	for _, event := range p.events() {
		if event.Type == agent.EventTaskDone {
			done = append(done, event.Task+" "+string(event.Outcome))
		}
	}
	assert.Equal(t, []string{"001 stopped", "002 completed"}, done,
		"the task given again on each connection ran once, and the next after it")
	assert.Empty(t, driver.Status().Reason, "the session is driven again")
}

func TestDriver_GivesADoneTaskWhoseMessageItCannotRemoveNoMore(t *testing.T) {
	p := startAdapter(t)
	inbox := t.TempDir()
	writeFile(t, filepath.Join(inbox, "001"), "write the release notes")
	require.NoError(t, os.Chmod(inbox, 0o555))
	t.Cleanup(func() { _ = os.Chmod(inbox, 0o755) })
	waits := &sleeps{}
	driver := runDriver(t, p, driverBot(agentfake.Harness), inbox, waits, &logs{})

	require.Eventually(t, func() bool { return driver.Status().Completed == 1 }, 5*time.Second, time.Millisecond)
	assert.FileExists(t, filepath.Join(inbox, "001"), "control: the message could not be removed")
	after := polls(waits)
	require.Eventually(t, func() bool { return polls(waits) > after+1 }, 5*time.Second, time.Millisecond,
		"the inbox is not found empty of tasks: the done task is given again")
	assert.Equal(t, agent.SessionIdle, driver.Status().State)
}

func TestDriver_OpensTheSessionAgainWhereTheAdapterLostItAndReadsItsEventsFromTheStart(t *testing.T) {
	p := startAdapter(t)
	inbox := t.TempDir()
	writeFile(t, filepath.Join(inbox, "001"), "write the release notes")
	driver := runDriver(t, p, driverBot(agentfake.Harness), inbox, &sleeps{}, &logs{})
	require.Eventually(t, func() bool { return driver.Status().Completed == 1 }, 5*time.Second, time.Millisecond)

	// The adapter loses the session, as a restarted one does, and the next message finds none.
	status, data := p.call(http.MethodDelete, "sessions/bot", nil)
	require.Equal(t, http.StatusNoContent, status, string(data))
	writeFile(t, filepath.Join(inbox, "002"), "and the patch notes")

	require.Eventually(t, func() bool { return driver.Status().Completed == 2 }, 5*time.Second, time.Millisecond,
		"the task of the session opened again is not read to its end")
	var tasks []string
	for _, event := range p.events() {
		tasks = append(tasks, event.Task)
	}
	assert.Equal(t, []string{"002", "002"}, tasks, "control: the session was opened again")
}

func TestDriver_WaitsBeforeDiallingAgainAnAdapterThatAnswersNoHTTP(t *testing.T) {
	p := startAdapter(t)
	p.noHTTP.Store(true)
	waits := &sleeps{}
	driver := runDriver(t, p, driverBot(agentfake.Harness), t.TempDir(), waits, &logs{})

	require.Eventually(t, func() bool { return len(backoffs(waits)) >= 3 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, bot.ReasonAdapterUnavailable, driver.Status().Reason)
	assert.LessOrEqual(t, int(p.dials.Load()), len(backoffs(waits))+1, "a dial again with no wait before it")
	assert.Equal(t, []time.Duration{time.Second, time.Second, time.Second}, backoffs(waits)[:3],
		"each dial is answered, so each wait starts from a second")
}
