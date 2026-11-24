package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// Cases returns every case of the suite.
func Cases() []Case {
	return []Case{
		{Requirement: "AUTH-1", Name: "a request without the workspace's token is refused", run: unauthorized},
		{Requirement: "INFO-1", Name: "info names the harness and the release", run: info},
		{Requirement: "WIRE-1", Name: "a body member the adapter does not know is refused", run: unknownMember},
		{Requirement: "SES-1", Name: "a session is idempotent by identifier and one lives at a time", run: sessionIdempotent},
		{Requirement: "SES-1", Name: "a spec that breaks a rule is refused", run: sessionInvalid},
		{Requirement: "SES-2", Name: "a session answers its spec and state", run: sessionRead},
		{Requirement: "SES-3", Name: "delete is idempotent and frees the adapter", run: sessionDelete},
		{Requirement: "TASK-1", Name: "a task is idempotent by identifier and one runs at a time", run: taskIdempotent},
		{Requirement: "TASK-2", Name: "a stopped task ends stopped", run: taskStop},
		{Requirement: "EVT-1", Name: "events are numbered from 1 and read after a sequence number", run: eventsNumbered},
		{Requirement: "EVT-1", Name: "an events read holds until an event is added", run: eventsHold},
		{Requirement: "EVT-2", Name: "a task's last event is its one taskDone", run: taskDoneLast},
		{Requirement: "MEM-1", Name: "the harness's answer is a message event", run: answered},
		{
			Requirement: "MODEL-1", Name: "model requests go to the gateway with the token file's token",
			Pending: "needs a model gateway stub that records requests, which arrives with interface 5",
		},
		{
			Requirement: "TOOL-1", Name: "the harness reaches only the session's MCP servers, with their token files' tokens",
			Pending: "needs an MCP server stub that records calls, and a harness that a task can make call it",
		},
		{
			Requirement: "CRED-1", Name: "the caller writes no credential but short-lived tokens into a workspace",
			Pending: "a caller obligation; the caller suite arrives with the first caller",
		},
		{
			Requirement: "VER-2", Name: "a caller skips an event type it does not know",
			Pending: "a caller obligation; the caller suite arrives with the first caller",
		},
	}
}

func unauthorized(t *testing.T, s *Subject) {
	s.callAs(t, "", http.MethodGet, "info", nil).want(t, "GET info without a token", http.StatusUnauthorized, v1alpha1.CodeUnauthorized)
	s.callAs(t, s.Token+"x", http.MethodGet, "info", nil).want(t, "GET info with another token", http.StatusUnauthorized, v1alpha1.CodeUnauthorized)
	s.call(t, http.MethodGet, "info", nil).want(t, "GET info with the token", http.StatusOK, "") // control
}

func info(t *testing.T, s *Subject) {
	var i v1alpha1.Info
	r := s.call(t, http.MethodGet, "info", nil)
	r.want(t, "GET info", http.StatusOK, "")
	r.decode(t, "GET info", &i)
	if i.Harness == "" || i.SpecRelease == "" {
		t.Fatalf("info %+v names no harness or no release", i)
	}
}

func unknownMember(t *testing.T, s *Subject) {
	t.Cleanup(func() { _, _ = s.send(context.Background(), s.Token, http.MethodDelete, "sessions/wire", nil) })
	raw, err := json.Marshal(s.spec("wire"))
	if err != nil {
		t.Fatalf("marshal the spec: %v", err)
	}
	extra := json.RawMessage(strings.Replace(string(raw), "{", `{"conformanceUnknown":true,`, 1))
	s.call(t, http.MethodPut, "sessions/wire", extra).want(t, "PUT with an unknown member", http.StatusBadRequest, v1alpha1.CodeInvalid)
	s.call(t, http.MethodPut, "sessions/wire", json.RawMessage(raw)).want(t, "PUT without it", http.StatusCreated, "") // control
}

func sessionIdempotent(t *testing.T, s *Subject) {
	s.session(t, "ses")
	s.call(t, http.MethodPut, "sessions/ses", s.spec("ses")).want(t, "PUT again", http.StatusOK, "")
	other := s.spec("ses")
	other.Instructions += " Another instruction."
	s.call(t, http.MethodPut, "sessions/ses", other).want(t, "PUT another spec", http.StatusConflict, v1alpha1.CodeConflict)
	s.call(t, http.MethodPut, "sessions/second", s.spec("second")).want(t, "PUT a second session", http.StatusConflict, v1alpha1.CodeBusy)
}

func sessionInvalid(t *testing.T, s *Subject) {
	t.Cleanup(func() { _, _ = s.send(context.Background(), s.Token, http.MethodDelete, "sessions/invalid", nil) })
	spec := s.spec("invalid")
	spec.Model.TokenFile = "relative/token"
	s.call(t, http.MethodPut, "sessions/invalid", spec).want(t, "PUT a relative token file", http.StatusBadRequest, v1alpha1.CodeInvalid)
	s.call(t, http.MethodPut, "sessions/invalid", s.spec("invalid")).want(t, "PUT the spec as given", http.StatusCreated, "") // control
}

func sessionRead(t *testing.T, s *Subject) {
	s.call(t, http.MethodGet, "sessions/read", nil).want(t, "GET before PUT", http.StatusNotFound, v1alpha1.CodeNotFound)
	s.session(t, "read")
	var got v1alpha1.Session
	r := s.call(t, http.MethodGet, "sessions/read", nil)
	r.want(t, "GET", http.StatusOK, "")
	r.decode(t, "GET", &got)
	if got.ID != "read" || got.Spec.Bot.Name != s.spec("read").Bot.Name || got.State != v1alpha1.SessionIdle {
		t.Fatalf("session %+v, want read, its spec and idle", got)
	}
}

func sessionDelete(t *testing.T, s *Subject) {
	s.session(t, "gone")
	s.call(t, http.MethodDelete, "sessions/gone", nil).want(t, "DELETE", http.StatusNoContent, "")
	s.call(t, http.MethodDelete, "sessions/gone", nil).want(t, "DELETE again", http.StatusNoContent, "")
	s.call(t, http.MethodGet, "sessions/gone", nil).want(t, "GET after DELETE", http.StatusNotFound, v1alpha1.CodeNotFound)
	s.session(t, "after")
}

func taskIdempotent(t *testing.T, s *Subject) {
	s.session(t, "tasks")
	s.task(t, "tasks", "one", "Reply with the word ready.")
	s.task(t, "tasks", "one", "Reply with the word ready.")
	s.call(t, http.MethodPost, "sessions/tasks/tasks", task("one", "Another input.")).
		want(t, "POST task one with another body", http.StatusConflict, v1alpha1.CodeConflict)
	read := s.until(t, "tasks", 0, doneOf("one"))
	s.task(t, "tasks", "hold", HoldInput)
	s.call(t, http.MethodPost, "sessions/tasks/tasks", task("two", "Reply with the word ready.")).
		want(t, "POST task two while hold runs", http.StatusConflict, v1alpha1.CodeBusy)
	s.call(t, http.MethodPost, "sessions/tasks/tasks/hold/stop", nil).want(t, "stop hold", http.StatusAccepted, "")
	read = append(read, s.until(t, "tasks", read[len(read)-1].Seq, doneOf("hold"))...)
	if n := count(read, doneOf("one")); n != 1 {
		t.Fatalf("task one, posted twice, ended %d times, want once", n)
	}
}

func taskStop(t *testing.T, s *Subject) {
	s.session(t, "stop")
	s.call(t, http.MethodPost, "sessions/stop/tasks/absent/stop", nil).want(t, "stop an unknown task", http.StatusNotFound, v1alpha1.CodeNotFound)
	s.task(t, "stop", "hold", HoldInput)
	s.call(t, http.MethodPost, "sessions/stop/tasks/hold/stop", nil).want(t, "stop hold", http.StatusAccepted, "")
	read := s.until(t, "stop", 0, doneOf("hold"))
	if last := read[len(read)-1]; last.Outcome != v1alpha1.OutcomeStopped {
		t.Fatalf("hold ended %q, want %q", last.Outcome, v1alpha1.OutcomeStopped)
	}
	var got v1alpha1.Session
	s.call(t, http.MethodGet, "sessions/stop", nil).decode(t, "GET", &got)
	if got.State != v1alpha1.SessionIdle {
		t.Fatalf("the session after a stopped task is %q, want idle", got.State)
	}
}

func eventsNumbered(t *testing.T, s *Subject) {
	s.session(t, "seq")
	s.task(t, "seq", "one", "Reply with the word ready.")
	read := s.until(t, "seq", 0, doneOf("one"))
	for i, e := range read {
		if e.Seq != uint64(i)+1 {
			t.Fatalf("event %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}
	var page v1alpha1.EventPage
	s.call(t, http.MethodGet, "sessions/seq/events?after=1&wait=0", nil).decode(t, "GET events after 1", &page)
	if len(page.Events) != len(read)-1 || (len(page.Events) > 0 && page.Events[0].Seq != 2) {
		t.Fatalf("events after 1: %+v, want the %d events from seq 2", page.Events, len(read)-1)
	}
	s.call(t, http.MethodGet, "sessions/seq/events?wait=51", nil).want(t, "GET events waiting 51", http.StatusBadRequest, v1alpha1.CodeInvalid)
}

func eventsHold(t *testing.T, s *Subject) {
	s.session(t, "hold")
	var page v1alpha1.EventPage
	s.call(t, http.MethodGet, "sessions/hold/events?wait=0", nil).decode(t, "GET events at once", &page)
	if len(page.Events) != 0 || page.Next != 0 {
		t.Fatalf("a new session's events: %+v, want none and next 0", page)
	}
	type result struct {
		page v1alpha1.EventPage
		at   time.Time
	}
	got := make(chan result, 1)
	go func() {
		var p v1alpha1.EventPage
		r, err := s.send(t.Context(), s.Token, http.MethodGet, "sessions/hold/events?after=0&wait=30", nil)
		if err == nil {
			err = json.Unmarshal(r.body, &p)
		}
		if err != nil {
			t.Errorf("GET events waiting: %v", err)
		}
		got <- result{p, time.Now()}
	}()
	time.Sleep(500 * time.Millisecond)
	posted := time.Now()
	s.task(t, "hold", "one", "Reply with the word ready.")
	r := <-got
	if len(r.page.Events) == 0 {
		t.Fatalf("the held read answered no event")
	}
	if r.at.Before(posted) {
		t.Fatalf("the held read answered before the task was posted")
	}
	if r.at.Sub(posted) > 25*time.Second {
		t.Fatalf("the held read answered %s after the task, not when its event was added", r.at.Sub(posted))
	}
}

func taskDoneLast(t *testing.T, s *Subject) {
	s.session(t, "done")
	s.task(t, "done", "one", "Reply with the word ready.")
	read := s.until(t, "done", 0, doneOf("one"))
	s.task(t, "done", "two", "Reply with the word ready.")
	read = append(read, s.until(t, "done", read[len(read)-1].Seq, doneOf("two"))...)
	for _, id := range []string{"one", "two"} {
		ended := false
		for _, e := range read {
			if e.Task != id {
				continue
			}
			if ended {
				t.Fatalf("task %s has event %+v after its taskDone", id, e)
			}
			ended = e.Type == v1alpha1.EventTaskDone
		}
		if n := count(read, doneOf(id)); n != 1 {
			t.Fatalf("task %s ended %d times, want once", id, n)
		}
	}
}

func answered(t *testing.T, s *Subject) {
	s.session(t, "answer")
	s.task(t, "answer", "one", "Reply with the word ready.")
	read := s.until(t, "answer", 0, doneOf("one"))
	if count(read, func(e v1alpha1.Event) bool { return e.Task == "one" && e.Type == v1alpha1.EventMessage && e.Text != "" }) == 0 {
		t.Fatalf("task one's events %+v carry no message", read)
	}
}

func count(events []v1alpha1.Event, match func(v1alpha1.Event) bool) int {
	n := 0
	for _, e := range events {
		if match(e) {
			n++
		}
	}

	return n
}
