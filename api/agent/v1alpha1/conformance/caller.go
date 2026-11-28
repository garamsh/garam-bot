package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// Conn is how a caller reaches an adapter: what interface 3's Dial and the token file give it.
type Conn struct {
	// BaseURL reaches the adapter's port, without PathPrefix.
	BaseURL string
	// Client carries the requests.
	Client *http.Client
	// Token is the content of the workspace's v1alpha1.TokenPath.
	Token string
}

// Files is the workspace's files as the caller writes them: interface 3's Provider.WriteFile bound
// to one workspace.
type Files interface {
	WriteFile(ctx context.Context, path string, content []byte) error
}

// Place is what the caller has at the execution place.
type Place struct {
	// BotToken is the bot's own token, which stays at the place (CRED-1).
	BotToken string
	// Now is the place's clock, which the caller reads for every token's age.
	Now func() time.Time
}

// TokenKeeper keeps a workspace's tokens.
type TokenKeeper interface {
	// Keep brings the workspace's tokens up to date at Place.Now: it writes each token that is
	// missing or has to be replaced before it expires. The caller of Keep calls it again as time
	// passes, at least every 10 minutes.
	Keep(ctx context.Context) error
}

// Caller is the code of a bot's process that drives an adapter, as far as interface 2 binds it: a
// caller suite runs it with RunCaller. A caller adapts to it, as an adapter adapts to Subject.
type Caller interface {
	// ReadEvents reads the events of the session after seq, as the caller acts on them. It
	// returns the events the caller took, and the number it reads after next.
	ReadEvents(ctx context.Context, c Conn, session string, after uint64) (events []v1alpha1.Event, next uint64, err error)
	// ReadState reads the state of the session as the caller reads it.
	ReadState(ctx context.Context, c Conn, session string) (v1alpha1.SessionState, error)
	// Tokens starts keeping the tokens of spec in the workspace files, and writes the first of
	// them before it returns.
	Tokens(ctx context.Context, files Files, spec v1alpha1.SessionSpec, place Place) (TokenKeeper, error)
}

// CallerCase is one test of the caller suite.
type CallerCase struct {
	// Requirement is the identifier of the requirement in ../README.md the case tests.
	Requirement string
	// Name says what the case holds.
	Name string
	run  func(t *testing.T, c Caller)
}

// CallerCases returns every case of the caller suite.
func CallerCases() []CallerCase {
	return []CallerCase{
		{Requirement: "VER-2", Name: "a caller skips an event type it does not know", run: skipsUnknownEvent},
		{Requirement: "VER-2", Name: "a caller reads a session state it does not know as unknown", run: readsUnknownState},
		{Requirement: "CRED-1", Name: "the caller writes no credential but short-lived tokens into a workspace", run: writesOnlyShortLivedTokens},
	}
}

// RunCaller runs every case of the caller suite against c, each as a subtest named
// <requirement>/<case>. The suite serves the caller a scripted adapter and a recording workspace,
// and needs no real one.
func RunCaller(t *testing.T, c Caller) {
	t.Helper()
	for _, cc := range CallerCases() {
		t.Run(cc.Requirement+"/"+cc.Name, func(t *testing.T) { cc.run(t, c) })
	}
}

// script serves what a scripted adapter answers: the events page and the session, whatever they
// hold, which the real adapters of this version never say.
func script(t *testing.T, state string, events string) Conn {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+v1alpha1.PathPrefix+"sessions/s1/events", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(events))
	})
	mux.HandleFunc("GET "+v1alpha1.PathPrefix+"sessions/s1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"s1","spec":{"bot":{"name":"b"},"model":{"baseUrl":"https://g.example","tokenFile":"/m"}},"state":"` + state + `"}`))
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+scriptToken {
			http.Error(w, `{"error":"unauthorized","message":"no token"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	return Conn{BaseURL: server.URL, Client: server.Client(), Token: scriptToken}
}

const scriptToken = "caller-suite-token-0123456789abcdef"

func skipsUnknownEvent(t *testing.T, c Caller) {
	// Event 2 is of a type a later release adds: it has members this version knows, so a caller
	// that does not look at its type takes it for a message.
	conn := script(t, "idle", `{"events":[
		{"seq":1,"task":"t1","type":"message","text":"one"},
		{"seq":2,"task":"t1","type":"reasoning","text":"a thought"},
		{"seq":3,"task":"t1","type":"taskDone","outcome":"completed"}],"next":3}`)
	events, next, err := c.ReadEvents(t.Context(), conn, "s1", 0)
	if err != nil {
		t.Fatalf("ReadEvents over an event of an unknown type: %v", err)
	}
	var seqs []uint64
	for _, e := range events {
		seqs = append(seqs, e.Seq)
	}
	if !slices.Equal(seqs, []uint64{1, 3}) {
		t.Fatalf("ReadEvents took the events %v, want 1 and 3: the unknown type is skipped, the known ones are not", seqs)
	}
	if next != 3 {
		t.Fatalf("ReadEvents next = %d, want 3: the caller reads on past the event it skipped", next)
	}
}

func readsUnknownState(t *testing.T, c Caller) {
	for _, tc := range []struct {
		answered string
		want     v1alpha1.SessionState
	}{
		{"paused", v1alpha1.SessionUnknown},
		{"idle", v1alpha1.SessionIdle}, // controls: a caller that reads every state as unknown is wrong
		{"working", v1alpha1.SessionWorking},
	} {
		got, err := c.ReadState(t.Context(), script(t, tc.answered, `{"events":[],"next":0}`), "s1")
		if err != nil {
			t.Fatalf("ReadState of the state %q: %v", tc.answered, err)
		}
		if got != tc.want {
			t.Fatalf("ReadState of the state %q = %q, want %q", tc.answered, got, tc.want)
		}
	}
}

// credClock steps 10 minutes at a time, and the case keeps the tokens for 3 hours: three times the
// life CRED-1 allows.
const (
	credStep = 10 * time.Minute
	credSpan = 3 * time.Hour
	credLife = time.Hour
)

// recorder is a workspace's files, with when each write came.
type recorder struct {
	now    func() time.Time
	writes []write
}

type write struct {
	path    string
	content string
	at      time.Time
}

func (r *recorder) WriteFile(_ context.Context, path string, content []byte) error {
	r.writes = append(r.writes, write{path: path, content: string(content), at: r.now()})

	return nil
}

func writesOnlyShortLivedTokens(t *testing.T, c Caller) {
	spec := v1alpha1.SessionSpec{
		Bot:   v1alpha1.Bot{Name: "b"},
		Model: v1alpha1.Model{BaseURL: "https://gateway.example", TokenFile: "/run/garam-bot/model/token"},
		Tools: []v1alpha1.Tool{
			{Name: "memory", URL: "https://tools.example/memory", TokenFile: "/run/garam-bot/tools/memory/token"},
			{Name: "mail", URL: "https://tools.example/mail", TokenFile: "/run/garam-bot/tools/mail/token"},
		},
	}
	named := []string{v1alpha1.TokenPath, spec.Model.TokenFile, spec.Tools[0].TokenFile, spec.Tools[1].TokenFile}
	start := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	now := start
	files := &recorder{now: func() time.Time { return now }}
	place := Place{BotToken: "bot-token-that-stays-at-the-place", Now: files.now}

	keeper, err := c.Tokens(t.Context(), files, spec, place)
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	for ; now.Sub(start) <= credSpan; now = now.Add(credStep) {
		if err := keeper.Keep(t.Context()); err != nil {
			t.Fatalf("Keep at +%s: %v", now.Sub(start), err)
		}
	}

	last := map[string]write{}
	for _, w := range files.writes {
		if !slices.Contains(named, w.path) {
			t.Fatalf("the caller wrote %s, which is no token of the protocol (%s)", w.path, strings.Join(named, ", "))
		}
		if strings.TrimSpace(w.content) == "" {
			t.Fatalf("the caller wrote an empty token to %s", w.path)
		}
		if strings.Contains(w.content, place.BotToken) {
			t.Fatalf("the caller wrote the bot's own token into %s: it stays at the place", w.path)
		}
		if p, ok := last[w.path]; ok {
			if w.at.Sub(p.at) > credLife {
				t.Fatalf("%s lived %s between two writes, more than the hour a token may", w.path, w.at.Sub(p.at))
			}
			if w.content == p.content {
				t.Fatalf("%s was rewritten with the token it held: a rewrite is a new token", w.path)
			}
		}
		last[w.path] = w
	}
	end := now.Add(-credStep) // the last Keep
	for _, path := range named {
		l, ok := last[path]
		if !ok {
			t.Fatalf("the caller never wrote %s", path)
		}
		if end.Sub(l.at) > credLife {
			t.Fatalf("%s was last written at +%s and had expired by +%s", path, l.at.Sub(start), end.Sub(start))
		}
		first := slices.IndexFunc(files.writes, func(w write) bool { return w.path == path })
		if files.writes[first].at != start {
			t.Fatalf("%s was first written at +%s, after the caller began", path, files.writes[first].at.Sub(start))
		}
	}
}
