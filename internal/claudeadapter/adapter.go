// Package claudeadapter is the claude-code adapter of the agent interface, v1alpha1
// (api/agent/v1alpha1): an HTTP server that runs the Claude Code CLI in a workspace, one `claude -p`
// per task, and reports what it did as events.
//
// The harness is given a model gateway and tool servers by the session, and a token file for each,
// which the interface wants read again for every request. Claude Code reads a credential once or
// on a timer, so each of them is a loopback proxy of this package's (proxy.go): the harness is
// pointed at the proxy, which reads the file for each request and sets the Authorization header.
// The harness is given no credential of its own.
package claudeadapter

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// Harness is the name Info answers: what a Bot's spec.agent.harness names.
const Harness = "claude-code"

// placeholderToken is what the harness is given as its API credential. The proxy replaces it with
// the model token, so it is not a secret, and a harness that sent it anywhere else would find it
// worth nothing.
const placeholderToken = "garam-bot-proxy"

// Config is what an Adapter is made of.
type Config struct {
	// Command is the Claude Code executable, "claude" when empty.
	Command string
	// Token returns the content of the file holding the adapter's bearer token. It is called for
	// each request (AUTH-1). Nil reads v1alpha1.TokenPath.
	Token func() ([]byte, error)
	// Version is the harness's version, which Info answers.
	Version string
	// DefaultDir is where the harness works when the session names no directory, "/workspace"
	// when empty.
	DefaultDir string
	// StateDir is where the adapter keeps a session's files, the harness's configuration
	// directory among them, the system's temporary directory when empty. Nothing in it is a
	// credential.
	StateDir string
	// Env is added to the harness's environment, after the adapter's own. It is for tests; an
	// adapter that runs for real sets nothing here.
	Env []string
	// Log receives what the adapter reports of itself, never a token or a header. Nil discards.
	Log *slog.Logger
}

// Adapter is an http.Handler serving agent/v1alpha1 for one workspace.
type Adapter struct {
	cfg Config

	mu      sync.Mutex
	session *session
	// changed is closed and replaced whenever an event is added, waking every events read.
	changed chan struct{}
}

type session struct {
	id    string
	spec  v1alpha1.SessionSpec
	dir   string
	tasks map[string]v1alpha1.TaskRequest
	// running is the running task's identifier, and stop ends its run.
	running string
	stop    context.CancelFunc
	// last is closed when the most recent run of the harness has ended, so the next waits for it.
	last   chan struct{}
	events []v1alpha1.Event

	model  *proxy
	tools  map[string]*proxy
	claude string
	// resumable is whether the harness's own session of claude has a turn to continue.
	resumable bool
}

// New returns an Adapter.
func New(cfg Config) *Adapter {
	if cfg.Command == "" {
		cfg.Command = "claude"
	}
	if cfg.Token == nil {
		cfg.Token = func() ([]byte, error) { return os.ReadFile(v1alpha1.TokenPath) }
	}
	if cfg.DefaultDir == "" {
		cfg.DefaultDir = "/workspace"
	}
	if cfg.StateDir == "" {
		cfg.StateDir = os.TempDir()
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}

	return &Adapter{cfg: cfg, changed: make(chan struct{})}
}

// ServeHTTP serves one request.
func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) {
		refuse(w, http.StatusUnauthorized, v1alpha1.CodeUnauthorized, "the request does not carry the workspace's token")
		return
	}
	parts, ok := strings.CutPrefix(r.URL.Path, v1alpha1.PathPrefix)
	p := strings.Split(parts, "/")
	switch {
	case !ok:
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "no operation at "+r.Method+" "+r.URL.Path)
	case len(p) == 1 && p[0] == "info" && r.Method == http.MethodGet:
		answer(w, http.StatusOK, v1alpha1.Info{Harness: Harness, Version: a.cfg.Version, SpecRelease: v1alpha1.SpecRelease})
	case len(p) == 2 && p[0] == "sessions":
		a.serveSession(w, r, p[1])
	case len(p) == 3 && p[0] == "sessions" && p[2] == "tasks" && r.Method == http.MethodPost:
		a.postTask(w, r, p[1])
	case len(p) == 5 && p[0] == "sessions" && p[2] == "tasks" && p[4] == "stop" && r.Method == http.MethodPost:
		a.stopTask(w, p[1], p[3])
	case len(p) == 3 && p[0] == "sessions" && p[2] == "events" && r.Method == http.MethodGet:
		a.events(w, r, p[1])
	default:
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "no operation at "+r.Method+" "+r.URL.Path)
	}
}

// authorized reads the token file anew, so a token the caller has replaced is the one taken.
func (a *Adapter) authorized(r *http.Request) bool {
	want, err := a.cfg.Token()
	want = bytes.TrimSpace(want)
	if err != nil || len(want) == 0 {
		return false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	return ok && subtle.ConstantTimeCompare([]byte(got), want) == 1
}

func (a *Adapter) serveSession(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut:
		var spec v1alpha1.SessionSpec
		if !decode(w, r, &spec) {
			return
		}
		if err := v1alpha1.CheckSession(id, spec); err != nil {
			refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, err.Error())
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case a.session == nil:
			s, err := a.newSession(id, spec)
			if err != nil {
				a.cfg.Log.Error("a session cannot start", "session", id, "error", err)
				refuse(w, http.StatusInternalServerError, v1alpha1.CodeInternal, "the session cannot start")
				return
			}
			a.session = s
			answer(w, http.StatusCreated, s.answer())
		case a.session.id != id:
			refuse(w, http.StatusConflict, v1alpha1.CodeBusy, "session "+a.session.id+" lives")
		case !reflect.DeepEqual(a.session.spec, spec):
			refuse(w, http.StatusConflict, v1alpha1.CodeConflict, "session "+id+" holds another spec")
		default:
			answer(w, http.StatusOK, a.session.answer())
		}
	case http.MethodGet:
		a.mu.Lock()
		defer a.mu.Unlock()
		if s := a.find(w, id); s != nil {
			answer(w, http.StatusOK, s.answer())
		}
	case http.MethodDelete:
		a.deleteSession(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "no operation at "+r.Method+" "+r.URL.Path)
	}
}

// newSession makes the session's directory and proxies. The caller holds a.mu.
func (a *Adapter) newSession(id string, spec v1alpha1.SessionSpec) (s *session, err error) {
	dir, err := os.MkdirTemp(a.cfg.StateDir, "claude-adapter-")
	if err != nil {
		return nil, err
	}
	s = &session{id: id, spec: spec, dir: dir, tasks: map[string]v1alpha1.TaskRequest{}, tools: map[string]*proxy{}, claude: uuid()}
	defer func() {
		if err != nil {
			s.release()
		}
	}()
	for _, d := range []string{"home", "config"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			return nil, err
		}
	}
	if s.model, err = startProxy(spec.Model.BaseURL, spec.Model.TokenFile, false, a.cfg.Log); err != nil {
		return nil, err
	}
	servers := map[string]any{}
	for _, t := range spec.Tools {
		p, err := startProxy(t.URL, t.TokenFile, true, a.cfg.Log)
		if err != nil {
			return nil, err
		}
		s.tools[t.Name] = p
		servers[t.Name] = map[string]string{"type": "http", "url": p.URL() + "/"}
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return nil, err
	}
	// The file names loopback addresses only, so it holds no credential.
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), raw, 0o600); err != nil {
		return nil, err
	}

	return s, nil
}

// release closes the session's proxies and removes its files.
func (s *session) release() {
	if s.model != nil {
		s.model.close()
	}
	for _, p := range s.tools {
		p.close()
	}
	_ = os.RemoveAll(s.dir)
}

func (a *Adapter) deleteSession(id string) {
	a.mu.Lock()
	s := a.session
	if s == nil || s.id != id {
		a.mu.Unlock()
		return
	}
	a.session = nil
	if s.stop != nil {
		s.stop()
	}
	last := s.last
	a.wake()
	a.mu.Unlock()
	if last != nil {
		<-last
	}
	s.release()
}

func (a *Adapter) postTask(w http.ResponseWriter, r *http.Request, id string) {
	var task v1alpha1.TaskRequest
	if !decode(w, r, &task) {
		return
	}
	if err := task.Check(); err != nil {
		refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, err.Error())
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.find(w, id)
	if s == nil {
		return
	}
	if earlier, ok := s.tasks[task.ID]; ok {
		if !reflect.DeepEqual(earlier, task) {
			refuse(w, http.StatusConflict, v1alpha1.CodeConflict, "task "+task.ID+" holds another body")
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if s.running != "" {
		refuse(w, http.StatusConflict, v1alpha1.CodeBusy, "task "+s.running+" runs")
		return
	}
	s.tasks[task.ID] = task
	s.running = task.ID
	ctx, stop := context.WithCancel(context.Background())
	s.stop = stop
	previous := s.last
	s.last = make(chan struct{})
	go a.run(ctx, s, task, previous, s.last)
	w.WriteHeader(http.StatusAccepted)
}

func (a *Adapter) stopTask(w http.ResponseWriter, id, task string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.find(w, id)
	if s == nil {
		return
	}
	if _, ok := s.tasks[task]; !ok {
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "no task "+task)
		return
	}
	if s.running == task {
		s.running = ""
		s.stop()
		a.add(s, v1alpha1.Event{Task: task, Type: v1alpha1.EventTaskDone, Outcome: v1alpha1.OutcomeStopped, Reason: "stopped by the caller"})
	}
	w.WriteHeader(http.StatusAccepted)
}

// run gives the harness the task and reports what it does. previous is the run before, which has
// to have ended: two harnesses on one conversation would corrupt it. done is closed at the end.
func (a *Adapter) run(ctx context.Context, s *session, task v1alpha1.TaskRequest, previous, done chan struct{}) {
	defer close(done)
	if previous != nil {
		<-previous
	}
	// A stop or a delete before the harness started leaves nothing to run.
	if ctx.Err() != nil {
		return
	}
	emit := func(e v1alpha1.Event) {
		a.mu.Lock()
		defer a.mu.Unlock()
		// An event of a task that was stopped, or of a session that was deleted, is not reported.
		if s.running == task.ID && a.session == s {
			e.Task = task.ID
			a.add(s, e)
		}
	}
	end := a.runClaude(ctx, s, task, emit)

	a.mu.Lock()
	defer a.mu.Unlock()
	if s.running != task.ID || a.session != s {
		return
	}
	s.running = ""
	s.resumable = s.resumable || end.Seen
	a.add(s, v1alpha1.Event{Task: task.ID, Type: v1alpha1.EventTaskDone, Outcome: end.Outcome, Reason: end.Reason})
}

// runClaude runs one `claude -p` on the task and returns how it ended.
func (a *Adapter) runClaude(ctx context.Context, s *session, task v1alpha1.TaskRequest, emit func(v1alpha1.Event)) outcome {
	a.mu.Lock()
	resume, claudeID := s.resumable, s.claude
	if !resume {
		// A conversation that never finished a turn has nothing to continue, and the harness
		// refuses an identifier it has begun.
		s.claude = uuid()
		claudeID = s.claude
	}
	a.mu.Unlock()

	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions", "--strict-mcp-config", "--mcp-config", filepath.Join(s.dir, "mcp.json"), "--setting-sources", "user"}
	if resume {
		args = append(args, "--resume", claudeID)
	} else {
		args = append(args, "--session-id", claudeID)
	}
	if s.spec.Model.Name != "" {
		args = append(args, "--model", s.spec.Model.Name)
	}
	if s.spec.Instructions != "" {
		args = append(args, "--append-system-prompt", s.spec.Instructions)
	}
	dir := s.spec.WorkingDir
	if dir == "" {
		dir = a.cfg.DefaultDir
	}
	cmd := exec.CommandContext(ctx, a.cfg.Command, args...)
	cmd.Dir = dir
	cmd.Env = a.harnessEnv(s)
	cmd.Stdin = strings.NewReader(prompt(task))
	var stderr tail
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return failed("the harness cannot start: " + err.Error())
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return failed("the harness cannot start: " + err.Error())
	}
	end := readStream(out, emit)
	werr := cmd.Wait()
	if end.Seen {
		return end
	}
	reason := "the harness ended without a result"
	if werr != nil {
		reason += ": " + werr.Error()
	}
	if t := stderr.String(); t != "" {
		reason += ": " + t
	}

	return failed(reason)
}

// harnessEnv is the harness's whole environment: the adapter's own is not passed on, so no
// credential of the workspace's reaches it but the placeholder (MODEL-1).
func (a *Adapter) harnessEnv(s *session) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(s.dir, "home"),
		"CLAUDE_CONFIG_DIR=" + filepath.Join(s.dir, "config"),
		"ANTHROPIC_BASE_URL=" + s.model.URL(),
		"ANTHROPIC_AUTH_TOKEN=" + placeholderToken,
		"DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}

	return append(env, a.cfg.Env...)
}

func prompt(task v1alpha1.TaskRequest) string {
	parts := make([]string, 0, len(task.Input))
	for _, in := range task.Input {
		if in.From != "" {
			parts = append(parts, "From "+in.From+":\n"+in.Text)
		} else {
			parts = append(parts, in.Text)
		}
	}

	return strings.Join(parts, "\n\n")
}

func failed(reason string) outcome {
	return outcome{Outcome: v1alpha1.OutcomeFailed, Reason: reason}
}

// tail keeps the last bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailSize = 1024

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailSize {
		t.buf = t.buf[len(t.buf)-tailSize:]
	}

	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return strings.TrimSpace(string(t.buf))
}

func (a *Adapter) events(w http.ResponseWriter, r *http.Request, id string) {
	after, wait, err := eventsQuery(r)
	if err != nil {
		refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, err.Error())
		return
	}
	deadline := time.After(time.Duration(wait) * time.Second)
	for {
		a.mu.Lock()
		s := a.find(w, id)
		if s == nil {
			a.mu.Unlock()
			return
		}
		page := v1alpha1.EventPage{Events: []v1alpha1.Event{}, Next: after}
		if after < uint64(len(s.events)) {
			page.Events = append(page.Events, s.events[after:]...)
			page.Next = uint64(len(s.events))
		}
		changed := a.changed
		a.mu.Unlock()
		if len(page.Events) > 0 || wait == 0 {
			answer(w, http.StatusOK, page)
			return
		}
		select {
		case <-changed:
		case <-deadline:
			wait = 0
		case <-r.Context().Done():
			return
		}
	}
}

func eventsQuery(r *http.Request) (after uint64, wait int, err error) {
	q := r.URL.Query()
	wait = v1alpha1.MaxWait
	if v := q.Get("after"); v != "" {
		if after, err = strconv.ParseUint(v, 10, 64); err != nil {
			return 0, 0, fmt.Errorf("after %q is not a sequence number", v)
		}
	}
	if v := q.Get("wait"); v != "" {
		if wait, err = strconv.Atoi(v); err != nil || wait < 0 || wait > v1alpha1.MaxWait {
			return 0, 0, fmt.Errorf("wait %q is not 0 to %d", v, v1alpha1.MaxWait)
		}
	}

	return after, wait, nil
}

// find returns the session id, or answers 404 and returns nil. The caller holds a.mu.
func (a *Adapter) find(w http.ResponseWriter, id string) *session {
	if a.session == nil || a.session.id != id {
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "no session "+id)
		return nil
	}

	return a.session
}

// add numbers e and appends it to s's events. The caller holds a.mu.
func (a *Adapter) add(s *session, e v1alpha1.Event) {
	e.Seq = uint64(len(s.events)) + 1
	s.events = append(s.events, e)
	a.wake()
}

func (a *Adapter) wake() {
	close(a.changed)
	a.changed = make(chan struct{})
}

func (s *session) answer() v1alpha1.Session {
	state := v1alpha1.SessionIdle
	if s.running != "" {
		state = v1alpha1.SessionWorking
	}

	return v1alpha1.Session{ID: s.id, Spec: s.spec, State: state, Task: s.running}
}

// decode reads the body into v, refusing a member v does not name, or answers 400 and reports
// false.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	err := d.Decode(v)
	if err == nil && d.More() {
		err = errors.New("more than one JSON value")
	}
	if err != nil {
		refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, "the body: "+err.Error())
		return false
	}

	return true
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func refuse(w http.ResponseWriter, status int, code v1alpha1.Code, message string) {
	answer(w, status, v1alpha1.ErrorBody{Error: code, Message: message})
}

// uuid returns a random version 4 UUID, which the harness takes as a session identifier.
func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
