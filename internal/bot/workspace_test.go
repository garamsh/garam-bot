package bot_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	"github.com/garamsh/garam-bot/api/workspace-access/v1alpha1/reference"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/internal/bot"
)

// The client of interface 7 is checked against the reference server, which stands in for the place's
// connector, its workloads and its workspaces, and whose fixture moves them (conformance.md §The
// fixture). Each workspace's adapter port echoes.

const (
	team       = "game-team"
	attachment = "main"
	tool       = "search"
)

// place is a reference server serving in a directory of its own until the test ends.
type place struct {
	t   *testing.T
	dir string
	url string
}

func startPlace(t *testing.T) *place {
	t.Helper()
	p := &place{t: t, dir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		reference.Main(ctx, []string{"--dir", p.dir, "serve"}, io.Discard, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	require.Eventually(t, func() bool {
		read, err := os.ReadFile(filepath.Join(p.dir, reference.URLFile))
		p.url = strings.TrimSpace(string(read))
		return err == nil && p.url != ""
	}, 10*time.Second, 10*time.Millisecond)
	return p
}

// fixture runs one of the reference's fixture subcommands, and decodes what it prints into answer.
func (p *place) fixture(answer any, args ...string) {
	p.t.Helper()
	var stdout, stderr bytes.Buffer
	status := reference.Main(context.Background(), append([]string{"--dir", p.dir}, args...), &stdout, &stderr)
	require.Zero(p.t, status, "%v: %s", args, stderr.String())
	if answer != nil {
		require.NoError(p.t, json.Unmarshal(stdout.Bytes(), answer))
	}
}

// bot places mira with the attachment and the tool, starts an instance of her, and answers her UID and
// a credential file naming that instance, with credential's flags.
func (p *place) bot(credential ...string) (string, string, string) {
	p.t.Helper()
	var placed reference.BotAnswer
	p.fixture(&placed, "bot", team, "mira", "--attachment", attachment, "--tool", tool)
	var started reference.InstanceAnswer
	p.fixture(&started, "instance", team, "mira")
	file := filepath.Join(p.t.TempDir(), "credential")
	p.credential(file, started.Instance, credential...)
	return placed.UID, started.Instance, file
}

// credential writes a credential naming instance, with the fixture's flags, to file.
func (p *place) credential(file, instance string, flags ...string) {
	p.t.Helper()
	var issued reference.CredentialAnswer
	p.fixture(&issued, append([]string{"credential", instance}, flags...)...)
	require.NoError(p.t, os.WriteFile(file, []byte(issued.Credential+"\n"), 0o600))
}

// held is what a workspace holds at a path: whether anything, and its content.
type held struct {
	Exists  bool
	Content []byte
}

// file answers what the workspace holds at path, read behind the server.
func (p *place) file(uid, path string) held {
	p.t.Helper()
	var answer reference.FileAnswer
	p.fixture(&answer, "file", uid, attachment, path)
	content, err := base64.StdEncoding.DecodeString(answer.Content)
	require.NoError(p.t, err)
	return held{Exists: answer.Exists, Content: content}
}

// sleeps records each wait the client asks for, in order. A wait of a minute or more, a renewal's,
// lasts until its context ends; a shorter one returns at once, after hook has seen it.
type sleeps struct {
	mu   sync.Mutex
	got  []time.Duration
	hook func(n int, d time.Duration)
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.got = append(s.got, d)
	n, hook := len(s.got), s.hook
	s.mu.Unlock()
	if hook != nil {
		hook(n, d)
	}
	if d > time.Minute {
		<-ctx.Done()
		return ctx.Err()
	}
	time.Sleep(time.Millisecond)
	return ctx.Err()
}

func (s *sleeps) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.got...)
}

// errors collects what Run reports.
type reported struct {
	mu   sync.Mutex
	errs []string
}

func (r *reported) add(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err.Error())
}

func (r *reported) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errs...)
}

// drive runs a Workspace of mira's attachment at p until the test ends.
func drive(t *testing.T, p *place, credentialFile string, waits *sleeps) (*bot.Workspace, *reported) {
	t.Helper()
	key, err := bot.NewKey()
	require.NoError(t, err)
	w, err := bot.NewWorkspace(bot.WorkspaceConfig{
		URL: p.url, TrustFile: filepath.Join(p.dir, reference.TrustFile), CredentialFile: credentialFile,
		Attachment: attachment, Key: key, Sleep: waits.sleep,
	})
	require.NoError(t, err)
	r := &reported{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx, r.add)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return w, r
}

// written waits until the session's tokens are written, and answers when.
// written answers the adapter's token the workspace last wrote.
func written(w *bot.Workspace) string {
	token, _ := w.AdapterTokens()
	return token
}

func writtenAt(t *testing.T, w *bot.Workspace) time.Time {
	t.Helper()
	require.Eventually(t, func() bool { return !w.Status().TokensWrittenAt.IsZero() }, 10*time.Second, 5*time.Millisecond)
	return w.Status().TokensWrittenAt
}

func TestWorkspace_AcquiresTheWorkspaceWritesItsTokensAndDialsTheAdapter(t *testing.T) {
	p := startPlace(t)
	uid, _, credential := p.bot()
	waits := &sleeps{}
	w, _ := drive(t, p, credential, waits)

	writtenAt(t, w)

	assert.Equal(t, workspace.PhaseRunning, w.Status().Phase)
	adapter := p.file(uid, agent.TokenPath)
	require.True(t, adapter.Exists, "the adapter's token is written")
	assert.Equal(t, written(w), string(adapter.Content), "the token the process holds is the one written")
	assert.GreaterOrEqual(t, len(adapter.Content), 32, "at least 128 random bits (AUTH-1)")
	model := p.file(uid, bot.ModelTokenPath)
	assert.True(t, model.Exists && strings.HasPrefix(string(model.Content), "reference_model_"), "the model token is written")
	search := p.file(uid, bot.ToolTokenPath(tool))
	assert.True(t, search.Exists && strings.HasPrefix(string(search.Content), "reference_tool_"), "the tool's token is written")
	space, ready := w.Session()
	assert.True(t, ready, "the session is ready once its tokens are written")
	assert.NotEmpty(t, space.Model.BaseURL, "the session's model gateway, as the connector answered it")
	require.Len(t, space.Tools, 1)
	assert.Equal(t, tool, space.Tools[0].Name)
	require.Eventually(t, func() bool { return len(waits.recorded()) > 0 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, 40*time.Minute, waits.recorded()[0], "the tokens are written again in 40 minutes (TOK-2)")

	conn, err := w.Dial(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	echoed := make([]byte, 4)
	_, err = io.ReadFull(conn, echoed)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(echoed), "the dial reaches the adapter's port")
}

func TestWorkspace_WaitsAfterEach401FromOneSecondDoublingTo60AndNotOnceAnswered(t *testing.T) {
	p := startPlace(t)
	_, instance, credential := p.bot("--expired")
	waits := &sleeps{}
	waits.hook = func(n int, _ time.Duration) {
		if n == 8 {
			p.credential(credential, instance) // the place replaces the credential
		}
	}
	w, r := drive(t, p, credential, waits)

	writtenAt(t, w)

	second := time.Second
	want := []time.Duration{second, 2 * second, 4 * second, 8 * second, 16 * second, 32 * second, time.Minute, time.Minute}
	got := waits.recorded()
	require.GreaterOrEqual(t, len(got), len(want)+1)
	assert.Equal(t, want, got[:len(want)], "AUTH-2: from 1 second, doubling to 60")
	assert.Equal(t, 40*time.Minute, got[len(want)], "an answer that is not 401 ends the back-off: no request after it waits")
	assert.Contains(t, r.all()[0], "401 invalid_credential")
}

func TestWorkspace_ReadsAPendingWorkspaceNoSoonerThanItsRetryAfter(t *testing.T) {
	p := startPlace(t)
	uid, _, credential := p.bot()
	waits := &sleeps{}
	w, _ := drive(t, p, credential, waits)
	first := writtenAt(t, w)
	p.fixture(nil, "workspace", uid, attachment, "--pending")
	from := len(waits.recorded())
	var readyWhilePending atomic.Bool
	waits.mu.Lock()
	waits.hook = func(n int, _ time.Duration) {
		if n == from+1 {
			_, ready := w.Session()
			readyWhilePending.Store(ready)
		}
		if n == from+4 {
			p.fixture(nil, "workspace", uid, attachment, "--running")
		}
	}
	waits.mu.Unlock()

	_, err := w.Dial(context.Background())
	require.ErrorContains(t, err, "409 not_running", "control: the workspace is not running")
	require.Eventually(t, func() bool { return w.Status().TokensWrittenAt.After(first) }, 10*time.Second, 5*time.Millisecond)

	got := waits.recorded()[from:]
	// The reference answers each pending read with Retry-After: 1. A back-off would double instead.
	want := []time.Duration{time.Second, time.Second, time.Second, time.Second}
	require.GreaterOrEqual(t, len(got), len(want)+1)
	assert.Equal(t, want, got[:len(want)], "WS-3: each pending read waits its Retry-After")
	assert.Equal(t, 40*time.Minute, got[len(want)], "once it runs, the tokens are written and stand")
	assert.False(t, readyWhilePending.Load(), "the session is not ready while the workspace is acquired again")
	_, ready := w.Session()
	assert.True(t, ready, "control: the session is ready again once the tokens are written")
}

func TestWorkspace_WritesItsTokensAgainAfterAnAcquireThatStartedTheWorkspace(t *testing.T) {
	p := startPlace(t)
	uid, _, credential := p.bot()
	w, _ := drive(t, p, credential, &sleeps{})
	first := writtenAt(t, w)
	before := written(w)
	p.fixture(nil, "workspace", uid, attachment, "--stopped")
	require.False(t, p.file(uid, agent.TokenPath).Exists, "control: a stop leaves no token in the workspace (WS-1)")

	_, err := w.Dial(context.Background())
	require.ErrorContains(t, err, "409 not_running")

	require.Eventually(t, func() bool { return w.Status().TokensWrittenAt.After(first) }, 10*time.Second, 5*time.Millisecond)
	adapter := p.file(uid, agent.TokenPath)
	assert.True(t, adapter.Exists, "the acquire that started the workspace is followed by a write")
	assert.Equal(t, written(w), string(adapter.Content))
	assert.NotEqual(t, before, written(w), "a fresh adapter token")
	assert.True(t, p.file(uid, bot.ModelTokenPath).Exists)
	assert.True(t, p.file(uid, bot.ToolTokenPath(tool)).Exists)
	assert.Equal(t, workspace.PhaseRunning, w.Status().Phase)
}

func TestWorkspace_WritesTheAdapterTokenAndWaitsForThePlacesGateway(t *testing.T) {
	p := startPlace(t)
	uid, _, credential := p.bot()
	p.fixture(nil, "issuer", "--off")
	w, _ := drive(t, p, credential, &sleeps{})

	require.Eventually(t, func() bool { return w.Status().WaitingForGateway }, 10*time.Second, 5*time.Millisecond)
	assert.True(t, p.file(uid, agent.TokenPath).Exists, "the adapter's token is written while the session waits")
	assert.False(t, p.file(uid, bot.ModelTokenPath).Exists, "no model token without an issuer")
	assert.Equal(t, string(access.CodeWaitingForGateway), w.Status().Refusal)
	_, ready := w.Session()
	assert.False(t, ready, "the session is not ready before its model and tool tokens are written")

	p.fixture(nil, "issuer", "--on")

	writtenAt(t, w)
	_, ready = w.Session()
	assert.True(t, ready, "control: the session is ready once they are")
	assert.False(t, w.Status().WaitingForGateway)
	assert.True(t, p.file(uid, bot.ModelTokenPath).Exists)
}

// miraWithAWorkspace is mira with one attachment.
const miraWithAWorkspace = mira + `  workspaces:
    - name: main
      template: {class: standard, lifetime: Bot}
`

func TestRun_DrivesTheBotsWorkspaceWithNoGaramIdentityAndReportsItWithNoToken(t *testing.T) {
	p := startPlace(t)
	uid, _, credential := p.bot()
	f := newFiles(t)
	writeFile(t, f.bot, miraWithAWorkspace)
	o := options(f)
	o.ConnectorURL, o.ConnectorTrustFile = p.url, filepath.Join(p.dir, reference.TrustFile)
	o.WorkspaceCredentialFile = credential
	stop := runInBackground(t, o)
	defer func() { require.NoError(t, stop()) }()

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(f.status)
		var status bot.Status
		return err == nil && json.Unmarshal(data, &status) == nil && status.Workspace != nil &&
			!status.Workspace.TokensWrittenAt.IsZero()
	}, 10*time.Second, 10*time.Millisecond)

	status := readStatus(t, f.status)
	assert.Nil(t, status.Token, "a Bot given no --credential-file has no garam identity")
	assert.Equal(t, attachment, status.Workspace.Attachment, "the Bot's first attachment")
	assert.Equal(t, workspace.PhaseRunning, status.Workspace.Phase)
	data, err := os.ReadFile(f.status)
	require.NoError(t, err)
	adapter := p.file(uid, agent.TokenPath)
	require.True(t, adapter.Exists)
	for _, secret := range []string{string(adapter.Content), "reference_model_", "reference_tool_"} {
		assert.NotContains(t, string(data), secret, "the status carries no token")
	}
	// The driver dials the adapter's port, which at the reference echoes and answers no interface 2.
	require.Eventually(t, func() bool {
		status := readStatus(t, f.status)
		return status.Session != nil && status.Session.Reason == bot.ReasonAdapterUnavailable
	}, 10*time.Second, 10*time.Millisecond, "the session is not reported")

	// Control: the same Bot with no attachment runs with no workspace.
	f2 := newFiles(t)
	writeFile(t, f2.bot, mira)
	o2 := options(f2)
	o2.ConnectorURL, o2.ConnectorTrustFile, o2.WorkspaceCredentialFile = o.ConnectorURL, o.ConnectorTrustFile, credential
	stop2 := runInBackground(t, o2)
	require.Eventually(t, func() bool { _, err := os.Stat(f2.status); return err == nil }, 5*time.Second, 10*time.Millisecond)
	assert.Nil(t, readStatus(t, f2.status).Workspace)
	assert.Nil(t, readStatus(t, f2.status).Session, "no session is driven with no workspace")
	require.NoError(t, stop2())
}

func TestWorkspace_WritesTheAdapterTokenAgainAtOnceWhenAskedWithOrWithoutTheSessionsTokens(t *testing.T) {
	p := startPlace(t)
	uid, _, credential := p.bot()
	p.fixture(nil, "issuer", "--off")
	waits := &sleeps{}
	w, _ := drive(t, p, credential, waits)
	require.Eventually(t, func() bool { return w.Status().WaitingForGateway }, 10*time.Second, 5*time.Millisecond)
	before := written(w)
	require.NotEmpty(t, before)
	_, writing := w.AdapterTokens()
	assert.Empty(t, writing, "control: no write is under way once one was answered")

	// No issuer: the session's tokens are refused, and the adapter's is written again regardless, as a
	// restarted adapter needs.
	w.Rewrite()
	require.Eventually(t, func() bool { return written(w) != before }, 10*time.Second, 5*time.Millisecond)
	assert.Equal(t, written(w), string(p.file(uid, agent.TokenPath).Content), "the new token is the one in the workspace")

	// With an issuer, a Rewrite writes every token again well before the 40 minutes they stand.
	p.fixture(nil, "issuer", "--on")
	first := writtenAt(t, w)
	require.Eventually(t, func() bool { return slices.Contains(waits.recorded(), 40*time.Minute) }, 10*time.Second, 5*time.Millisecond)
	token := written(w)
	w.Rewrite()
	require.Eventually(t, func() bool { return w.Status().TokensWrittenAt.After(first) }, 10*time.Second, 5*time.Millisecond)
	assert.NotEqual(t, token, written(w))
}

func TestWorkspace_AnswersTheTokenOfAWriteThatFailedBesideTheOneWritten(t *testing.T) {
	p := startPlace(t)
	uid, _, credential := p.bot()
	waits := &sleeps{}
	w, _ := drive(t, p, credential, waits)
	writtenAt(t, w)
	held := written(w)

	// A write whose answer is a failure may have written its adapter token: FILE-1 keeps the files
	// before a failure. Both tokens are answered until a write is answered.
	var seen atomic.Value
	waits.mu.Lock()
	waits.hook = func(_ int, d time.Duration) {
		if d <= time.Minute && seen.Load() == nil {
			done, writing := w.AdapterTokens()
			seen.Store([2]string{done, writing})
		}
	}
	waits.mu.Unlock()
	p.fixture(nil, "workspace", uid, attachment, "--stopped")
	w.Rewrite()
	require.Eventually(t, func() bool { return seen.Load() != nil }, 10*time.Second, 5*time.Millisecond)
	tokens := seen.Load().([2]string)
	assert.Equal(t, held, tokens[0], "the token last written stands while the write failed")
	assert.NotEmpty(t, tokens[1], "the failed write's token is not answered")
	assert.NotEqual(t, held, tokens[1])
	// Control: once a write is answered, no token is being written.
	require.Eventually(t, func() bool { _, writing := w.AdapterTokens(); return writing == "" && written(w) != held },
		10*time.Second, 5*time.Millisecond)
}
