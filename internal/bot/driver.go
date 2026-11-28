package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// sessionID is the one session the bot's process opens (ADR 0043 decision 7).
const sessionID = "bot"

// The session's reasons, as the status names why no task runs.
const (
	// ReasonWaitingForWorkspace: the workspace does not yet run with the adapter's token written, or
	// with the session's tokens written once the adapter's harness is checked.
	ReasonWaitingForWorkspace = "waitingForWorkspace"
	// ReasonHarnessMismatch: the adapter runs another harness than spec.agent.harness names.
	ReasonHarnessMismatch = "harnessMismatch"
	// ReasonAdapterUnavailable: the adapter is not reached, or refuses.
	ReasonAdapterUnavailable = "adapterUnavailable"
	// ReasonSourceUnavailable: the message source fails, or asks to be polled later.
	ReasonSourceUnavailable = "messageSourceUnavailable"
)

// readyPoll is how often the driver looks again at a workspace that is not ready, so a session opens
// soon after it is.
const readyPoll = 500 * time.Millisecond

// maxAttempts is how many times the adapter may refuse a message's task as invalid or as conflicting
// with another task of its identifier before the message is rejected.
const maxAttempts = 3

// DriverConfig is what a Driver runs on.
type DriverConfig struct {
	Bot   *v1alpha1.Bot
	Place AdapterPlace
	// Messages is where the tasks come from. Nil, the session is opened and given no task.
	Messages MessageSource
	Sleep    func(ctx context.Context, d time.Duration) error
	Logger   *slog.Logger
}

// Driver is interface 2's caller for the Bot (ADR 0043 decision 7): it checks the adapter's harness,
// opens the session bot, gives a task per message, one at a time, and long-polls the events to each
// task's end. No input, event content, adapter message or token reaches a log line or the status.
type Driver struct {
	cfg    DriverConfig
	client *adapterClient
	source backoff

	// next is the events cursor of the session, current the message taken and not yet done, and given
	// whether the adapter has its task on the connection this serve opened. ended holds the end of each
	// task the session's events showed, so a task that ended before it was given, or while another
	// was current, is not waited on. refused counts the refusals of the current message's task.
	next    uint64
	current *Message
	given   bool
	ended   map[string]agent.Outcome
	refused int

	mu     sync.Mutex
	status SessionStatus
}

// SessionStatus is what the status file says of the session: its state, why no task runs where none
// can, the last task's end, and how many messages ended each way.
type SessionStatus struct {
	State     agent.SessionState `json:"state,omitempty"`
	Reason    string             `json:"reason,omitempty"`
	LastTask  *TaskStatus        `json:"lastTask,omitempty"`
	Completed int                `json:"completed"`
	Failed    int                `json:"failed"`
	Stopped   int                `json:"stopped"`
	Rejected  int                `json:"rejected"`
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
	if cfg.Messages == nil {
		cfg.Messages = noMessages{}
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Driver{
		cfg:    cfg,
		client: newAdapterClient(&redialer{dial: cfg.Place.Dial, sleep: cfg.Sleep}, cfg.Place.AdapterTokens),
		ended:  map[string]agent.Outcome{},
	}, nil
}

// Status answers the session's status.
func (d *Driver) Status() SessionStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status
}

// errNotReady is a workspace not yet ready for the next step.
var errNotReady = errors.New("the workspace is not ready")

// Run drives the adapter until ctx ends, reading on from the last event it read. A failure leaves it
// running, and the session is opened again:
//   - every readyPoll while the workspace is not ready;
//   - at once where the adapter was not reached, the dial itself waiting DIAL-2's back-off;
//   - otherwise after a back-off from 1 second doubling to 60, reset once the session moves. An
//     adapter that refuses both its tokens, as one restarted with an empty token directory does, has
//     the workspace's tokens written again first.
func (d *Driver) Run(ctx context.Context) {
	retry := backoff{}
	for ctx.Err() == nil {
		err := d.serve(ctx, retry.reset)
		if ctx.Err() != nil {
			return
		}
		wait := readyPoll
		switch {
		case errors.Is(err, errNotReady):
		case errors.Is(err, errUnreached):
			d.cfg.Logger.Warn("adapter not reached; dialling again", "error", err)
			continue
		default:
			d.cfg.Logger.Warn("session not driven; retrying", "reason", d.Status().Reason, "error", err)
			var refused *adapterRefusal
			if errors.As(err, &refused) && refused.Status == http.StatusUnauthorized {
				d.cfg.Place.Rewrite()
			}
			wait = retry.next(err)
		}
		if d.cfg.Sleep(ctx, wait) != nil {
			return
		}
	}
}

func (d *Driver) setReason(reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.Reason = reason
}

func (d *Driver) setState(state agent.SessionState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.State = state
}

// serve checks the adapter's harness once its token is written, opens the session once its tokens are,
// and drives its tasks until a call fails. progressed is told each time the session moves, a task
// given or an event read, so the back-off starts again; opening the session alone is not a move, so a
// task refused on every connection is retried less and less often.
func (d *Driver) serve(ctx context.Context, progressed func()) error {
	d.given = false
	if written, writing := d.cfg.Place.AdapterTokens(); written == "" && writing == "" {
		d.setReason(ReasonWaitingForWorkspace)
		return errNotReady
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
	space, ready := d.cfg.Place.Session()
	if !ready {
		d.setReason(ReasonWaitingForWorkspace)
		return errNotReady
	}
	if err := d.open(ctx, d.spec(space)); err != nil {
		d.setReason(ReasonAdapterUnavailable)
		return err
	}
	d.setReason("")
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
// process or generation, is deleted and opened again. A session opened anew numbers its events from 1
// and holds no task, so the current task is given to it again: after a workspace restart, a task runs
// a second time.
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
		d.next, d.ended = 0, map[string]agent.Outcome{}
		d.cfg.Logger.Info("session opened", "session", sessionID)
	}
	return nil
}

// step takes the next message where none is current, gives its task where the adapter does not have
// it, and reads the session's events once, holding up to MaxWait for one.
func (d *Driver) step(ctx context.Context, progressed func()) error {
	if d.current == nil {
		m, err := d.take(ctx)
		if err != nil {
			return err
		}
		d.current, d.given, d.refused = &m, false, 0
		if !taskID.MatchString(m.ID) || len(m.Input) == 0 {
			d.cfg.Logger.Warn("message is no task; rejected", "task", m.ID)
			d.finish(ctx, OutcomeRejected)
			return nil
		}
	}
	if outcome, ok := d.ended[d.current.ID]; ok {
		// The session ended the task before: given by an earlier process, or ended while another
		// task was current.
		d.finish(ctx, outcome)
		return nil
	}
	if !d.given {
		if err := d.give(ctx); err != nil {
			var refused *adapterRefusal
			if !errors.As(err, &refused) || refused.Code != agent.CodeBusy {
				return err
			}
			// Another task of the session runs, given by an earlier process: its end is read below,
			// and the current one is given after it.
		} else {
			progressed()
		}
		if d.current == nil {
			return nil
		}
	}
	d.setState(agent.SessionWorking)
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
		if event.Type == agent.EventTaskDone {
			d.ended[event.Task] = event.Outcome
		}
	}
	if outcome, ok := d.ended[d.current.ID]; ok && d.given {
		d.finish(ctx, outcome)
	}
	return nil
}

// give gives the current message's task. A task given before, on an earlier connection or by an
// earlier process, is given again idempotently, and runs nothing a second time (TASK-1). A task the
// adapter refuses as invalid, or as conflicting with another of its identifier, maxAttempts times
// rejects the message.
func (d *Driver) give(ctx context.Context) error {
	body, err := json.Marshal(agent.TaskRequest{ID: d.current.ID, Input: d.current.Input})
	if err != nil {
		return err
	}
	_, err = d.client.call(ctx, http.MethodPost, "sessions/"+sessionID+"/tasks", body, nil)
	var refused *adapterRefusal
	if errors.As(err, &refused) && (refused.Code == agent.CodeInvalid || refused.Code == agent.CodeConflict) {
		d.refused++
		if d.refused >= maxAttempts {
			d.cfg.Logger.Warn("task refused; message rejected", "task", d.current.ID, "code", refused.Code, "attempts", d.refused)
			d.finish(ctx, OutcomeRejected)
			return nil
		}
	}
	if err != nil {
		return err
	}
	d.given = true
	d.cfg.Logger.Info("task given", "task", d.current.ID)
	return nil
}

// take answers the source's next message. While the source fails it is polled again after a back-off
// from 1 second doubling to 60, and while it asks to be polled later, after the time it asks, at most
// 60 seconds (REN-1).
func (d *Driver) take(ctx context.Context) (Message, error) {
	d.setState(agent.SessionIdle)
	for {
		m, err := d.cfg.Messages.Next(ctx)
		if err == nil {
			d.source.reset()
			d.setReason("")
			return m, nil
		}
		if ctx.Err() != nil {
			return Message{}, err
		}
		d.setReason(ReasonSourceUnavailable)
		wait := d.source.next(err)
		var later *RetryLater
		if errors.As(err, &later) && later.After > 0 {
			wait = min(later.After, lastWait)
		} else {
			d.cfg.Logger.Warn("message source failed; polling again", "error", err)
		}
		if err := d.cfg.Sleep(ctx, wait); err != nil {
			return Message{}, err
		}
	}
}

// finish records the current message's end and acknowledges it to the source.
func (d *Driver) finish(ctx context.Context, outcome agent.Outcome) {
	m := *d.current
	d.cfg.Logger.Info("task done", "task", m.ID, "outcome", outcome)
	d.mu.Lock()
	d.status.LastTask = &TaskStatus{ID: m.ID, Outcome: outcome}
	switch outcome {
	case agent.OutcomeCompleted:
		d.status.Completed++
	case agent.OutcomeStopped:
		d.status.Stopped++
	case OutcomeRejected:
		d.status.Rejected++
	default:
		d.status.Failed++
	}
	d.status.State = agent.SessionIdle
	d.mu.Unlock()
	if err := d.cfg.Messages.Done(ctx, m, outcome); err != nil {
		d.cfg.Logger.Error("message not acknowledged", "task", m.ID, "error", err)
	}
	d.current, d.given, d.refused = nil, false, 0
}
