// Package agentfake is the in-memory adapter of the agent interface, v1alpha1: the reference
// implementation the conformance suite runs against while the version is below v1. Its harness calls
// no model unless WithModel is given, when each task first makes one model call (MODEL-1). It answers
// a task with one message echoing its input and completes it, except a task whose input is HoldText,
// which it holds until the task is stopped.
package agentfake

import (
	"bytes"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// HoldText is the input that keeps a task running until it is stopped.
const HoldText = "conformance: hold"

// Harness is the name Info answers.
const Harness = "agentfake"

// Adapter is an http.Handler serving agent/v1alpha1 for one workspace.
type Adapter struct {
	token func() ([]byte, error)
	model *model

	mu      sync.Mutex
	session *session
	// changed is closed and replaced whenever an event is added, waking every events read.
	changed chan struct{}
}

type session struct {
	id      string
	spec    v1alpha1.SessionSpec
	tasks   map[string]v1alpha1.TaskRequest
	running string
	events  []v1alpha1.Event
}

// model is how the harness reaches the session's model: the workspace's files read by readFile, and
// the gateway reached by client.
type model struct {
	readFile func(path string) ([]byte, error)
	client   *http.Client
}

// WithModel has each task first call the session's model once, at model.baseUrl's Messages path, with
// the token of model.tokenFile, which readFile reads as the workspace holds it, read again for each
// call (MODEL-1). Where the session gives model.trustFile, readFile reads it for each call too, and
// the call verifies the gateway against its roots alone (MODEL-2); client is used otherwise. A call
// that fails fails its task.
func (a *Adapter) WithModel(readFile func(path string) ([]byte, error), client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	a.model = &model{readFile: readFile, client: client}
	return a
}

// call makes a task's one model call (MODEL-1).
func (m *model) call(spec v1alpha1.Model) error {
	token, err := m.readFile(spec.TokenFile)
	if err != nil {
		return err
	}
	body := []byte(`{"model":"agentfake","max_tokens":16,"messages":[{"role":"user","content":"Reply with the word ready."}]}`)
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(spec.BaseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Anthropic-Version", "2023-06-01")
	client, err := m.clientFor(spec)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the model answered %d", response.StatusCode)
	}
	return nil
}

// clientFor is the client of a call under spec: one that trusts model.trustFile's roots alone where it
// is given, read now (MODEL-2), and m.client otherwise.
func (m *model) clientFor(spec v1alpha1.Model) (*http.Client, error) {
	if spec.TrustFile == "" {
		return m.client, nil
	}
	trust, err := m.readFile(spec.TrustFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(trust) {
		return nil, errors.New("the trust file holds no certificate")
	}
	return &http.Client{
		Timeout:   m.client.Timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true},
	}, nil
}

// New returns an Adapter that takes the bearer token token returns, read at each request as an
// adapter reads v1alpha1.TokenPath.
func New(token func() ([]byte, error)) *Adapter {
	return &Adapter{token: token, changed: make(chan struct{})}
}

// ServeHTTP serves one request.
func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) {
		refuse(w, http.StatusUnauthorized, v1alpha1.CodeUnauthorized, "the request does not carry the workspace's token")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, v1alpha1.PathPrefix), "/")
	if !strings.HasPrefix(r.URL.Path, v1alpha1.PathPrefix) {
		parts = nil
	}
	switch {
	case len(parts) == 1 && parts[0] == "info" && r.Method == http.MethodGet:
		answer(w, http.StatusOK, v1alpha1.Info{Harness: Harness, Version: "0", SpecRelease: v1alpha1.SpecRelease})
	case len(parts) == 2 && parts[0] == "sessions":
		a.serveSession(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "tasks" && r.Method == http.MethodPost:
		a.postTask(w, r, parts[1])
	case len(parts) == 5 && parts[0] == "sessions" && parts[2] == "tasks" && parts[4] == "stop" && r.Method == http.MethodPost:
		a.stopTask(w, parts[1], parts[3])
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "events" && r.Method == http.MethodGet:
		a.events(w, r, parts[1])
	default:
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "no operation at "+r.Method+" "+r.URL.Path)
	}
}

func (a *Adapter) authorized(r *http.Request) bool {
	want, err := a.token()
	if err != nil || len(want) == 0 {
		return false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	return ok && subtle.ConstantTimeCompare([]byte(got), bytes.TrimSpace(want)) == 1
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
			a.session = &session{id: id, spec: spec, tasks: map[string]v1alpha1.TaskRequest{}}
			answer(w, http.StatusCreated, a.session.answer())
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
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.session != nil && a.session.id == id {
			a.session = nil
			a.wake()
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "no operation at "+r.Method+" "+r.URL.Path)
	}
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
	texts := make([]string, 0, len(task.Input))
	for _, in := range task.Input {
		texts = append(texts, in.Text)
	}
	text := strings.Join(texts, "\n")
	if a.model != nil && text != HoldText {
		// The task runs while its model call is made, outside the lock, as a harness's turn does.
		s.running = task.ID
		go a.callModel(s, task.ID, text)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	a.add(s, v1alpha1.Event{Task: task.ID, Type: v1alpha1.EventMessage, Text: text})
	if text == HoldText {
		s.running = task.ID
	} else {
		a.add(s, v1alpha1.Event{Task: task.ID, Type: v1alpha1.EventTaskDone, Outcome: v1alpha1.OutcomeCompleted})
	}
	w.WriteHeader(http.StatusAccepted)
}

// callModel makes task's model call, then ends the task, unless it was stopped or its session ended
// meanwhile.
func (a *Adapter) callModel(s *session, task, text string) {
	err := a.model.call(s.spec.Model)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != s || s.running != task {
		return
	}
	s.running = ""
	if err != nil {
		a.add(s, v1alpha1.Event{Task: task, Type: v1alpha1.EventTaskDone, Outcome: v1alpha1.OutcomeFailed})
		return
	}
	a.add(s, v1alpha1.Event{Task: task, Type: v1alpha1.EventMessage, Text: text})
	a.add(s, v1alpha1.Event{Task: task, Type: v1alpha1.EventTaskDone, Outcome: v1alpha1.OutcomeCompleted})
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
		a.add(s, v1alpha1.Event{Task: task, Type: v1alpha1.EventTaskDone, Outcome: v1alpha1.OutcomeStopped, Reason: "stopped by the caller"})
	}
	w.WriteHeader(http.StatusAccepted)
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
