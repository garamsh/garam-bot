package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// sessionID is the one session the bot's process opens (ADR 0043 decision 7).
const sessionID = "bot"

// taskID is the form TASK-1 gives a task's identifier, which an inbox file's name is.
var taskID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// The session's reasons, as the status names why no task runs.
const (
	// ReasonWaitingForWorkspace: the workspace is not running with its tokens written yet.
	ReasonWaitingForWorkspace = "waitingForWorkspace"
	// ReasonHarnessMismatch: the adapter runs another harness than spec.agent.harness names.
	ReasonHarnessMismatch = "harnessMismatch"
	// ReasonAdapterUnavailable: the adapter is not reached, or refuses.
	ReasonAdapterUnavailable = "adapterUnavailable"
)

// AdapterPlace is what the driver reaches its adapter through: the workspace's dial and adapter token
// (interface 7's DIAL-1, interface 2's AUTH-1), and the session's model and tools once the workspace
// runs with its tokens written. Workspace is one.
type AdapterPlace interface {
	Dial(ctx context.Context) (net.Conn, error)
	AdapterToken() string
	Session() (access.Workspace, bool)
}

// DriverConfig is what a Driver runs on.
type DriverConfig struct {
	Bot   *v1alpha1.Bot
	Place AdapterPlace
	// InboxDir is the directory of message files the bot's tasks come from, until G4's long poll: each
	// file is one task, its name the task's identifier and its content the input. A file is deleted
	// once its task is done. A name starting with "." is skipped, so a writer can rename into place.
	// Empty, the session is opened and given no task.
	InboxDir string
	// InboxPoll is how often an empty inbox is read again, a second where zero.
	InboxPoll time.Duration
	Sleep     func(ctx context.Context, d time.Duration) error
	Logger    *slog.Logger
}

// Driver is interface 2's caller for the Bot (ADR 0043 decision 7): it checks the adapter's harness,
// opens the session bot, starts a task per message, one at a time, and long-polls the events to each
// task's end. No input, event content or token reaches a log line or the status.
type Driver struct {
	cfg    DriverConfig
	client *adapterClient

	// next is the events cursor of the session, current the task taken and not yet done, and given
	// whether the adapter has it on the connection this serve opened. finished holds the tasks done,
	// whose message is skipped where it could not be removed.
	next     uint64
	current  *agent.TaskRequest
	file     string
	given    bool
	finished map[string]bool

	mu     sync.Mutex
	status SessionStatus
}

// SessionStatus is what the status file says of the session: its state, why no task runs where none
// can, the last task's end, and how many tasks ended each way.
type SessionStatus struct {
	State     agent.SessionState `json:"state,omitempty"`
	Reason    string             `json:"reason,omitempty"`
	LastTask  *TaskStatus        `json:"lastTask,omitempty"`
	Completed int                `json:"completed"`
	Failed    int                `json:"failed"`
	Stopped   int                `json:"stopped"`
}

// TaskStatus is a task's end: its identifier and outcome. The adapter's reason is its own text, and is
// kept out with the rest of what the harness says.
type TaskStatus struct {
	ID      string        `json:"id"`
	Outcome agent.Outcome `json:"outcome"`
}

// NewDriver returns a Driver under cfg.
func NewDriver(cfg DriverConfig) (*Driver, error) {
	if cfg.Bot == nil || cfg.Place == nil {
		return nil, errors.New("a driver needs the Bot and its workspace")
	}
	if cfg.InboxPoll <= 0 {
		cfg.InboxPoll = time.Second
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Driver{
		cfg:      cfg,
		client:   newAdapterClient(&redialer{dial: cfg.Place.Dial, sleep: cfg.Sleep}, cfg.Place.AdapterToken),
		finished: map[string]bool{},
	}, nil
}

// Status answers the session's status.
func (d *Driver) Status() SessionStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status
}

// Run drives the adapter until ctx ends, reading on from the last event it read. A failure leaves it
// running, and the session is opened again: at once where the adapter was not reached, the dial
// itself waiting DIAL-2's back-off, and otherwise after a back-off from 1 second doubling to 60, reset
// once the session moves.
func (d *Driver) Run(ctx context.Context) {
	retry := backoff{}
	for ctx.Err() == nil {
		err := d.serve(ctx, retry.reset)
		if ctx.Err() != nil {
			return
		}
		d.cfg.Logger.Warn("session not driven; retrying", "reason", d.Status().Reason, "error", err)
		if errors.Is(err, errUnreached) {
			continue
		}
		if d.cfg.Sleep(ctx, retry.next(err)) != nil {
			return
		}
	}
}

func (d *Driver) setReason(reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.Reason = reason
}

// serve opens the session and drives its tasks until a call fails. progressed is told each time the
// session moves, so the back-off starts again.
func (d *Driver) serve(ctx context.Context, progressed func()) error {
	d.given = false
	space, ready := d.cfg.Place.Session()
	if !ready {
		d.setReason(ReasonWaitingForWorkspace)
		return errors.New("the workspace is not running with its tokens written")
	}
	var info agent.Info
	if _, err := d.client.call(ctx, http.MethodGet, "info", nil, &info); err != nil {
		d.setReason(ReasonAdapterUnavailable)
		return err
	}
	if info.Harness != d.cfg.Bot.Spec.Agent.Harness {
		d.setReason(ReasonHarnessMismatch)
		return fmt.Errorf("the adapter runs the harness %q, and the Bot names %q", info.Harness, d.cfg.Bot.Spec.Agent.Harness)
	}
	if err := d.open(ctx, d.spec(space)); err != nil {
		d.setReason(ReasonAdapterUnavailable)
		return err
	}
	d.setReason("")
	progressed()
	for {
		if err := d.step(ctx, progressed); err != nil {
			if ctx.Err() == nil {
				d.setReason(ReasonAdapterUnavailable)
			}
			return err
		}
	}
}

// spec is the session's spec: the Bot, its instructions, and the model and tools the workspace reaches,
// each with the token file the workspace's tokens are written to.
func (d *Driver) spec(space access.Workspace) agent.SessionSpec {
	bot := d.cfg.Bot
	spec := agent.SessionSpec{
		Bot:          agent.Bot{Name: bot.Name},
		Instructions: bot.Spec.Persona.Instructions,
		Model:        agent.Model{BaseURL: space.Model.BaseURL, TokenFile: ModelTokenPath},
	}
	for _, tool := range space.Tools {
		spec.Tools = append(spec.Tools, agent.Tool{Name: tool.Name, URL: tool.URL, TokenFile: ToolTokenPath(tool.Name)})
	}
	return spec
}

// open opens the session bot under spec. One the adapter holds under another spec, from an earlier
// process or generation, is deleted and opened again. A session opened anew numbers its events from 1,
// and the task given and not done is given again.
func (d *Driver) open(ctx context.Context, spec agent.SessionSpec) error {
	body, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	status, err := d.client.call(ctx, http.MethodPut, "sessions/"+sessionID, body, nil)
	var refused *adapterRefusal
	if errors.As(err, &refused) && refused.Code == agent.CodeConflict {
		if _, err := d.client.call(ctx, http.MethodDelete, "sessions/"+sessionID, nil, nil); err != nil {
			return err
		}
		status, err = d.client.call(ctx, http.MethodPut, "sessions/"+sessionID, body, nil)
	}
	if err != nil {
		return err
	}
	if status == http.StatusCreated {
		d.next = 0
		d.cfg.Logger.Info("session opened", "session", sessionID)
	}
	return nil
}

// step gives the next task where none is given, or reads the session's events once, holding up to
// MaxWait for one.
func (d *Driver) step(ctx context.Context, progressed func()) error {
	if d.current == nil {
		task, file, err := d.nextMessage()
		if err != nil {
			return err
		}
		if task == nil {
			d.setState(agent.SessionIdle)
			return d.cfg.Sleep(ctx, d.cfg.InboxPoll)
		}
		d.current, d.file, d.given = task, file, false
	}
	if !d.given {
		body, err := json.Marshal(d.current)
		if err != nil {
			return err
		}
		// A task given before, on an earlier connection, is given again idempotently, and runs nothing
		// a second time (TASK-1).
		if _, err := d.client.call(ctx, http.MethodPost, "sessions/"+sessionID+"/tasks", body, nil); err != nil {
			return err
		}
		d.given = true
		d.setState(agent.SessionWorking)
		d.cfg.Logger.Info("task given", "task", d.current.ID)
		progressed()
	}
	events, next, err := d.client.events(ctx, sessionID, d.next, agent.MaxWait)
	if err != nil {
		return err
	}
	d.next = next
	if len(events) > 0 {
		progressed()
	}
	for _, event := range events {
		d.cfg.Logger.Info("event", "task", event.Task, "type", event.Type)
		if event.Type == agent.EventTaskDone && event.Task == d.current.ID {
			d.done(event)
		}
	}
	return nil
}

// done records the current task's end and removes its message.
func (d *Driver) done(event agent.Event) {
	d.cfg.Logger.Info("task done", "task", event.Task, "outcome", event.Outcome)
	d.mu.Lock()
	d.status.LastTask = &TaskStatus{ID: event.Task, Outcome: event.Outcome}
	switch event.Outcome {
	case agent.OutcomeCompleted:
		d.status.Completed++
	case agent.OutcomeStopped:
		d.status.Stopped++
	default:
		d.status.Failed++
	}
	d.status.State = agent.SessionIdle
	d.mu.Unlock()
	d.finished[event.Task] = true
	if err := os.Remove(d.file); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.cfg.Logger.Error("message not removed; it is skipped while the process runs", "task", event.Task, "error", err)
	}
	d.current, d.file, d.given = nil, "", false
}

func (d *Driver) setState(state agent.SessionState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.State = state
}

// nextMessage reads the inbox's first message by name, as os.ReadDir sorts them, and answers no task
// where it holds none.
func (d *Driver) nextMessage() (*agent.TaskRequest, string, error) {
	if d.cfg.InboxDir == "" {
		return nil, "", nil
	}
	entries, err := os.ReadDir(d.cfg.InboxDir)
	if err != nil {
		return nil, "", fmt.Errorf("read the inbox: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || name[0] == '.' || !taskID.MatchString(name) || d.finished[name] {
			continue
		}
		path := filepath.Join(d.cfg.InboxDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read the message %s: %w", name, err)
		}
		return &agent.TaskRequest{ID: name, Input: []agent.Input{{Text: string(content)}}}, path, nil
	}
	return nil, "", nil
}

// redialer dials the adapter, waiting before a dial that follows a connection the server broke or a
// dial that failed: from 1 second, doubling to 60, reset by a dial that is answered 101 (DIAL-2). The
// wait is the dial's, not the caller's, because the HTTP transport may dial again of its own accord
// for a request a broken connection did not answer.
type redialer struct {
	dial  func(ctx context.Context) (net.Conn, error)
	sleep func(ctx context.Context, d time.Duration) error

	mu     sync.Mutex
	wait   backoff
	broken bool
	failed error
}

// DialContext dials the adapter, whatever network and address the transport names.
func (r *redialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	r.mu.Lock()
	var wait time.Duration
	if r.broken {
		wait = r.wait.next(r.failed)
	}
	r.mu.Unlock()
	if wait > 0 {
		if err := r.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	conn, err := r.dial(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broken, r.failed = err != nil, err
	if err != nil {
		return nil, err
	}
	r.wait.reset()
	return &watchedConn{Conn: conn, broke: r.broke}, nil
}

func (r *redialer) broke() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broken = true
}

// watchedConn is one dialled connection, which tells broke when a read fails on a connection not
// closed by the process: one the server broke.
type watchedConn struct {
	net.Conn
	broke  func()
	closed atomic.Bool
}

func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil && !c.closed.Load() {
		c.broke()
	}
	return n, err
}

func (c *watchedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// errUnreached is a call that reached no adapter: the dial failed, or the connection broke.
var errUnreached = errors.New("the adapter was not reached")

// adapterClient speaks interface 2 to the adapter over the workspace's dial, with the adapter token
// read again for each request (AUTH-1). Each connection is one dial.
type adapterClient struct {
	http  *http.Client
	dial  *redialer
	token func() string
}

func newAdapterClient(dial *redialer, token func() string) *adapterClient {
	return &adapterClient{
		dial:  dial,
		token: token,
		http: &http.Client{
			// An events read holds up to MaxWait, so the timeout leaves it room.
			Timeout: (agent.MaxWait + 20) * time.Second,
			Transport: &http.Transport{
				DialContext: dial.DialContext,
				// Below DIAL-3's four dials of an instance, and idle less than its 120 seconds.
				MaxConnsPerHost: 2, IdleConnTimeout: time.Minute,
			},
		},
	}
}

// adapterRefusal is the adapter's refusal of one call. Its message names the call, the status and
// the code, and nothing of the request.
type adapterRefusal struct {
	call   string
	Status int
	Code   agent.Code
}

func (r *adapterRefusal) Error() string {
	return fmt.Sprintf("the adapter refused %s: %d %s", r.call, r.Status, r.Code)
}

// call makes one request of path, under PathPrefix, decodes a 2xx answer into answer where it is given,
// and answers the status.
func (c *adapterClient) call(ctx context.Context, method, path string, body []byte, answer any) (int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://adapter"+agent.PathPrefix+path, reader)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		// The next dial waits, whatever broke the call: an answer that is no HTTP is not dialled for
		// again at once either.
		c.dial.broke()
		return 0, fmt.Errorf("%w: %w", errUnreached, err)
	}
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	if err != nil {
		return 0, fmt.Errorf("read the adapter's answer: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		refused := &adapterRefusal{call: method + " " + path, Status: response.StatusCode}
		var parsed agent.ErrorBody
		if json.Unmarshal(read, &parsed) == nil {
			refused.Code = parsed.Error
		}
		return response.StatusCode, refused
	}
	if answer != nil {
		if err := json.Unmarshal(read, answer); err != nil {
			return 0, fmt.Errorf("decode the adapter's answer to %s %s: %w", method, path, err)
		}
	}
	return response.StatusCode, nil
}

// events reads the session's events after after, holding up to wait seconds for one, and keeps the
// types it knows (VER-2). It answers them and the number to read after next.
func (c *adapterClient) events(ctx context.Context, session string, after uint64, wait int) ([]agent.Event, uint64, error) {
	var page struct {
		Events []json.RawMessage `json:"events"`
		Next   uint64            `json:"next"`
	}
	path := "sessions/" + session + "/events?after=" + strconv.FormatUint(after, 10) + "&wait=" + strconv.Itoa(wait)
	if _, err := c.call(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, after, err
	}
	kept := make([]agent.Event, 0, len(page.Events))
	for _, raw := range page.Events {
		var event agent.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, after, fmt.Errorf("decode an event: %w", err)
		}
		switch event.Type {
		case agent.EventMessage, agent.EventToolCall, agent.EventToolResult, agent.EventTaskDone:
			kept = append(kept, event)
		}
	}
	return kept, page.Next, nil
}
