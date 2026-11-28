// Package conformance is the conformance suite of interface 2, the agent: the cases an adapter must
// pass, each named for the requirement in ../README.md it tests. An adapter plugs in as a Subject,
// reached over HTTP, and runs the suite with Run.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
	"github.com/garamsh/garam-bot/api/model-gateway/v1alpha1/gatewayfake"
)

// Subject is an adapter under test.
type Subject struct {
	// BaseURL reaches the adapter's port, without PathPrefix.
	BaseURL string
	// Client carries the requests, http.DefaultClient when nil.
	Client *http.Client
	// Token is the content of the workspace's v1alpha1.TokenPath.
	Token string
	// Session is a spec the adapter can run: its model and tools reach what the adapter's harness
	// needs. The suite changes only Bot.Name in it.
	Session v1alpha1.SessionSpec
	// Timeout bounds each wait for an event. Zero is 3 minutes.
	Timeout time.Duration
	// Gateway, where set, is the model gateway Session's model.baseUrl reaches: interface 5's
	// gatewayfake, which records each model call. ModelBot is the bot whose token Session's
	// model.tokenFile holds. MODEL-1 runs only with them.
	Gateway  *gatewayfake.Gateway
	ModelBot gateway.TokenRequest
}

// Case is one test of the suite.
type Case struct {
	// Requirement is the identifier of the requirement in ../README.md the case tests.
	Requirement string
	// Name says what the case holds.
	Name string
	// Pending, when set, says why the case cannot run yet; the case is skipped.
	Pending string
	run     func(t *testing.T, s *Subject)
}

// HoldInput is the input of a task the suite stops: an adapter's harness keeps working on it
// until then.
const HoldInput = "conformance: hold"

// Run runs every case against s, each as a subtest named <requirement>/<case>. Each case deletes
// the session it made, so the cases run one after another against one adapter.
func Run(t *testing.T, s Subject) {
	t.Helper()
	if s.Client == nil {
		s.Client = http.DefaultClient
	}
	if s.Timeout == 0 {
		s.Timeout = 3 * time.Minute
	}
	for _, c := range Cases() {
		t.Run(c.Requirement+"/"+c.Name, func(t *testing.T) {
			if c.Pending != "" {
				t.Skip("pending: " + c.Pending)
			}
			c.run(t, &s)
		})
	}
}

// reply is an answer the adapter gave.
type reply struct {
	status int
	body   []byte
}

// call sends method to path under PathPrefix with body as JSON, nil for none, and the token.
func (s *Subject) call(t *testing.T, method, path string, body any) reply {
	t.Helper()
	return s.callAs(t, s.Token, method, path, body)
}

func (s *Subject) callAs(t *testing.T, token, method, path string, body any) reply {
	t.Helper()
	var raw []byte
	if body != nil {
		var ok bool
		if raw, ok = body.(json.RawMessage); !ok {
			var err error
			if raw, err = json.Marshal(body); err != nil {
				t.Fatalf("marshal the body of %s %s: %v", method, path, err)
			}
		}
	}
	r, err := s.send(t.Context(), token, method, path, raw)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}

	return r
}

// send sends method to path under PathPrefix with body, nil for none, carrying token unless it is
// empty, and reads the whole answer.
func (s *Subject) send(ctx context.Context, token, method, path string, body []byte) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	var in io.Reader
	if body != nil {
		in = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(s.BaseURL, "/")+v1alpha1.PathPrefix+path, in)
	if err != nil {
		return reply{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, fmt.Errorf("read the answer: %w", err)
	}

	return reply{status: resp.StatusCode, body: out}, nil
}

// want fails the case unless r has status and, for a refusal, code.
func (r reply) want(t *testing.T, call string, status int, code v1alpha1.Code) {
	t.Helper()
	if r.status != status {
		t.Fatalf("%s: status %d, want %d: %s", call, r.status, status, r.body)
	}
	if code == "" {
		return
	}
	var e v1alpha1.ErrorBody
	if err := json.Unmarshal(r.body, &e); err != nil || e.Error != code {
		t.Fatalf("%s: body %s, want the code %q", call, r.body, code)
	}
}

// decode reads r's body into v.
func (r reply) decode(t *testing.T, call string, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("%s: the answer is not its JSON: %v: %s", call, err, r.body)
	}
}

// session puts a session of the suite's spec under id and deletes it when the case ends.
func (s *Subject) session(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(func() { _, _ = s.send(context.Background(), s.Token, http.MethodDelete, "sessions/"+id, nil) })
	s.call(t, http.MethodPut, "sessions/"+id, s.spec(id)).want(t, "PUT the session", http.StatusCreated, "")
}

// spec is the suite's session spec for id.
func (s *Subject) spec(id string) v1alpha1.SessionSpec {
	spec := s.Session
	spec.Bot.Name = "conformance-" + id

	return spec
}

// task posts a task of input under id to session.
func (s *Subject) task(t *testing.T, session, id, input string) {
	t.Helper()
	s.call(t, http.MethodPost, "sessions/"+session+"/tasks", task(id, input)).
		want(t, "POST task "+id, http.StatusAccepted, "")
}

func task(id, input string) v1alpha1.TaskRequest {
	return v1alpha1.TaskRequest{ID: id, Input: []v1alpha1.Input{{Text: input, From: "conformance"}}}
}

// until reads the session's events from after until one satisfies done, and returns every event
// read up to it.
func (s *Subject) until(t *testing.T, session string, after uint64, done func(v1alpha1.Event) bool) []v1alpha1.Event {
	t.Helper()
	deadline := time.Now().Add(s.Timeout)
	var read []v1alpha1.Event
	for time.Now().Before(deadline) {
		var page v1alpha1.EventPage
		s.call(t, http.MethodGet, fmt.Sprintf("sessions/%s/events?after=%d&wait=5", session, after), nil).
			decode(t, "GET events", &page)
		for _, e := range page.Events {
			read = append(read, e)
			if done(e) {
				return read
			}
		}
		after = page.Next
	}
	t.Fatalf("no awaited event within %s; read %+v", s.Timeout, read)

	return nil
}

// doneOf reports whether e ends the task id.
func doneOf(id string) func(v1alpha1.Event) bool {
	return func(e v1alpha1.Event) bool { return e.Task == id && e.Type == v1alpha1.EventTaskDone }
}
