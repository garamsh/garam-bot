//go:build conformance

// Package server is conformance.md's server suite. It reaches the server under test only through the
// protocol and the fixture command CONFORMANCE_FIXTURE names.
package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

const (
	token = v1alpha1.TokenDir + "agent/token"
	// data is a file of the workspace's data volume, which a stop and a recovery keep.
	data = workspace.DataPath + "/kept"
)

func TestServer(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"T-1/the server serves TLS 1.3 and refuses an older handshake", transportTLS13},
		{"T-2/a request member the server does not know is refused", unknownMember},
		{"CRED-2/a missing, expired, foreign, long, pre-start or iat-less credential is refused", credentialRefused},
		{"CRED-2/a credential of an instance that ended is refused", credentialOfEndedInstance},
		{"CRED-2/a credential is reused while it lives", credentialReused},
		{"PROOF-1/a proof that is missing, repeated, unverified, of another typ, or for another method, URL, origin or credential is refused", proofRefused},
		{"PROOF-1/a proof within 60 seconds of the clock is taken and one past it is refused", proofWindow},
		{"PROOF-1/a seen jti is refused", proofReplayed},
		{"PROOF-1/a jti seen 90 seconds ago is refused while its proof is within the window", proofRemembered},
		{"PIN-1/a second key for one instance is refused with invalid_key", pinSecondKey},
		{"PIN-1/a restarted instance pins its new key", pinRestarted},
		{"PIN-1/a new instance of the bot pins its own key", pinNewInstance},
		{"BOT-1/a workload that runs no bot is unknown", botNoBot},
		{"BOT-1/an attachment of another bot is not found", botOtherAttachment},
		{"BOT-1/a bot made again under its name reaches a new workspace and not the old", botRemade},
		{"BOT-1/an instance of the bot's earlier incarnation reaches nothing", botEarlierIncarnation},
		{"WS-1/acquire is idempotent and answers the workspace's state", acquireIdempotent},
		{"WS-1/acquire starts a stopped workspace and recovers a failed one with its data", acquireRecovers},
		{"WS-1/a token file is gone after a stop or a recovery, and is written again", tokensGoneAfterStart},
		{"WS-1/an attachment the server cannot serve is unsupported", acquireUnsupported},
		{"WS-1/a workspace still deleting is answered deleting", acquireDeleting},
		{"WS-1/a workspace whose bot is gone is deleted, and one whose bot lives is not", deletedWithBot},
		{"WS-2/a read before acquire is not found", readBeforeAcquire},
		{"WS-2/a read takes nothing from the bot's limit", readTakesNothing},
		{"WS-3/a pending workspace is answered with Retry-After", pendingRetryAfter},
		{"FILE-1/files under the token directory are written whole and in order", filesWritten},
		{"FILE-1/the token directory itself, a sibling, a dot-dot and an unclean path are refused and nothing is written", filesPathRefused},
		{"FILE-1/a link at the final component is replaced, not followed", filesLinkReplaced},
		{"FILE-1/a link at an inner component is not followed, and the files before it stay written", filesInnerLink},
		{"FILE-1/a write of 17 files or of none is refused and nothing is written", filesCount},
		{"FILE-1/content above 64 KiB in all is refused", filesTooLarge},
		{"FILE-1/a workspace not running is answered not_running", filesNotRunning},
		{"DIAL-1/a dial reaches the adapter's port and passes bytes unchanged", dialEchoes},
		{"DIAL-1/another upgrade token is refused", dialOtherUpgrade},
		{"DIAL-1/a workspace not running is answered not_running", dialNotRunning},
		{"DIAL-3/a fifth open dial of one instance is answered 429", dialFifth},
		{"DIAL-3/the server's cap is answered 503", dialServerCap},
		{"DIAL-4/a dial closes when its instance ends", dialClosesWithInstance},
		{"DIAL-4/a dial closes when its bot is gone", dialClosesWithBot},
		{"DIAL-4/a dial closes when its workspace stops", dialClosesWithWorkspace},
		{"TRUST-1/a model's trust is PEM roots, as CheckTrust takes them", trustAnswered},
		{"TOK-1/session tokens name the model and every tool", tokensAnswered},
		{"TOK-1/a server with no issuer answers waiting_for_gateway", tokensNoIssuer},
		{"LIMIT-1/a bot past its limit is answered 429 alone, for each call that reaches a provider or an issuer", limitBot},
		{"LIMIT-1/a call refused before a provider or an issuer is asked takes nothing", limitRefusedTakesNothing},
		{"LIMIT-2/an instance past its request limit is answered 429, refused requests counted", limitInstance},
		{"SEC-1/a refusal carries no token and no credential", secretsNotInRefusal},
		// Last: it moves the server's clock past every open dial's idle time.
		{"DIAL-3/a dial idle for 120 seconds is closed, and one idle for 50 is not", dialIdle},
	} {
		t.Run(c.name, c.run)
	}
}

func ws(attachment string) string { return v1alpha1.WorkspacesPath + attachment }

func transportTLS13(t *testing.T) {
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12, //nolint:gosec // only the version is under test
	}}}
	_, err := old.Get(baseURL + ws("main"))
	assert.Error(t, err, "a TLS 1.2 handshake is refused (T-1)")
	// Control: the same call over TLS 1.3 is answered.
	b := newBot(t)
	assert.Equal(t, http.StatusNotFound, b.call(t, http.MethodGet, ws("main"), nil).status)
}

func unknownMember(t *testing.T) {
	b := newBot(t)
	unknown := map[string]any{"mode": "0600"}
	for label, path := range map[string]string{"acquire": ws("main"), "session tokens": ws("main") + v1alpha1.TokensSuffix} {
		method := http.MethodPut
		if label == "session tokens" {
			method = http.MethodPost
		}
		refused := b.call(t, method, path, unknown)
		assert.Equal(t, http.StatusBadRequest, refused.status, "%s: %s", label, refused.raw)
		assert.Equal(t, v1alpha1.CodeInvalid, refused.code, label)
	}
	// Control: an acquire with an empty object, and one with no body, are taken.
	assert.Equal(t, http.StatusOK, b.call(t, http.MethodPut, ws("main"), map[string]any{}).status)
	b.running(t)
	refused := b.call(t, http.MethodPut, ws("main")+v1alpha1.FilesSuffix,
		map[string]any{"files": []map[string]any{{"path": token, "content": []byte("t")}}, "mode": "0600"})
	assert.Equal(t, http.StatusBadRequest, refused.status, refused.raw)
	assert.Equal(t, v1alpha1.CodeInvalid, refused.code)
	// Control: the same body without the member is taken.
	assert.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: token, Content: []byte("t")}).status)
}

func credentialRefused(t *testing.T) {
	b := newBot(t)
	for label, c := range map[string]string{
		"not a credential":     "garbage",
		"for another audience": credential(t, b.instance, "--audience", "another-audience"),
		"expired":              credential(t, b.instance, "--expired"),
		"living over an hour":  credential(t, b.instance, "--lifetime", "61m"),
		"issued before start":  credential(t, b.instance, "--before-start"),
		"carrying no iat":      credential(t, b.instance, "--no-iat"),
	} {
		refused := b.process.call(t, c, http.MethodGet, ws("main"), nil, proofOptions{})
		assert.Equal(t, http.StatusUnauthorized, refused.status, label)
		assert.Equal(t, v1alpha1.CodeInvalidCredential, refused.code, label)
	}
	missing := b.process.call(t, "", http.MethodGet, ws("main"), nil, proofOptions{})
	assert.Equal(t, v1alpha1.CodeInvalidCredential, missing.code, "missing")
	// Control: the instance's credential as the place issues it is taken.
	assert.Equal(t, http.StatusNotFound, b.call(t, http.MethodGet, ws("main"), nil).status)
}

func credentialOfEndedInstance(t *testing.T) {
	b := newBot(t)
	require.Equal(t, http.StatusNotFound, b.call(t, http.MethodGet, ws("main"), nil).status) // control
	run(t, "end", b.instance)
	refused := b.call(t, http.MethodGet, ws("main"), nil)
	assert.Equal(t, http.StatusUnauthorized, refused.status)
	assert.Equal(t, v1alpha1.CodeInvalidCredential, refused.code)
}

func credentialReused(t *testing.T) {
	b := newBot(t)
	for range 3 {
		assert.Equal(t, http.StatusOK, b.acquire(t, "main").status, "one credential serves each request while it lives")
	}
}

func proofRefused(t *testing.T) {
	b := newBot(t)
	// Setup: the process's key is pinned to the instance.
	require.Equal(t, http.StatusNotFound, b.call(t, http.MethodGet, ws("main"), nil).status)
	other := credential(t, b.instance)
	thief := newProcess(t)
	for label, o := range map[string]proofOptions{
		"no proof":                             {none: true},
		"two proofs":                           {twice: true},
		"a signature that does not verify":     {corrupt: true},
		"another key's jwk over the signature": {jwk: &thief.key.PublicKey},
		"the pinned key's jwk over another key's signature": {signer: thief.key, jwk: &b.process.key.PublicKey},
		"another typ":        {typ: "JWT"},
		"a query in htu":     {url: baseURL + ws("main") + "?attachment=main"},
		"another method":     {method: http.MethodPost},
		"another path":       {url: baseURL + ws("other")},
		"another origin":     {url: "https://another.invalid" + ws("main")},
		"another credential": {credential: other},
	} {
		refused := b.process.call(t, b.credential, http.MethodGet, ws("main"), nil, o)
		assert.Equal(t, http.StatusUnauthorized, refused.status, label)
		assert.Equal(t, v1alpha1.CodeInvalidDPoPProof, refused.code, label)
	}
	// Control: the proof for this request is taken.
	assert.Equal(t, http.StatusNotFound, b.call(t, http.MethodGet, ws("main"), nil).status)
}

func proofWindow(t *testing.T) {
	b := newBot(t)
	for _, offset := range []time.Duration{-55 * time.Second, 55 * time.Second} {
		taken := b.process.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{iat: offset})
		assert.Equal(t, http.StatusNotFound, taken.status, "a proof %v from the clock is taken", offset)
	}
	for _, offset := range []time.Duration{-65 * time.Second, 65 * time.Second} {
		refused := b.process.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{iat: offset})
		assert.Equal(t, v1alpha1.CodeInvalidDPoPProof, refused.code, "a proof %v from the clock is refused", offset)
	}
}

func proofReplayed(t *testing.T) {
	b := newBot(t)
	proof := b.process.proof(t, http.MethodGet, baseURL+ws("main"), b.credential, proofOptions{})
	assert.Equal(t, http.StatusNotFound, b.process.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{reuse: proof}).status)
	refused := b.process.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{reuse: proof})
	assert.Equal(t, v1alpha1.CodeInvalidDPoPProof, refused.code, "a jti seen is refused")
}

func proofRemembered(t *testing.T) {
	b := newBot(t)
	proof := b.process.proof(t, http.MethodGet, baseURL+ws("main"), b.credential, proofOptions{iat: 55 * time.Second})
	require.Equal(t, http.StatusNotFound, b.process.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{reuse: proof}).status)
	// Its iat is 35 seconds behind the moved clock, so only the jti's memory refuses it.
	advanceClock(t, 90*time.Second)
	refused := b.process.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{reuse: proof})
	assert.Equal(t, v1alpha1.CodeInvalidDPoPProof, refused.code, "a jti seen 90 seconds ago is remembered")
	// Control: a fresh proof for the moved clock is taken.
	assert.Equal(t, http.StatusNotFound, b.call(t, http.MethodGet, ws("main"), nil).status)
}

func pinSecondKey(t *testing.T) {
	b := newBot(t)
	require.Equal(t, http.StatusOK, b.acquire(t, "main").status)
	thief := newProcess(t)
	refused := thief.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{})
	assert.Equal(t, http.StatusUnauthorized, refused.status)
	assert.Equal(t, v1alpha1.CodeInvalidKey, refused.code)
	// Control: the pinned key is still taken.
	assert.Equal(t, http.StatusOK, b.call(t, http.MethodGet, ws("main"), nil).status)
}

func pinRestarted(t *testing.T) {
	b := newBot(t)
	require.Equal(t, http.StatusOK, b.acquire(t, "main").status)
	run(t, "restart", b.instance)
	restarted := newProcess(t)
	assert.Equal(t, http.StatusOK, restarted.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{}).status,
		"a restarted instance pins its new process's key")
	refused := b.call(t, http.MethodGet, ws("main"), nil)
	assert.Equal(t, v1alpha1.CodeInvalidKey, refused.code, "the earlier process's key is no longer the instance's")
}

func pinNewInstance(t *testing.T) {
	b := newBot(t)
	require.Equal(t, http.StatusOK, b.acquire(t, "main").status)
	second := instance(t, b.namespace, b.name)
	other := newProcess(t)
	assert.Equal(t, http.StatusOK, other.call(t, credential(t, second), http.MethodGet, ws("main"), nil, proofOptions{}).status)
	// Control: the first instance keeps its own pin.
	assert.Equal(t, v1alpha1.CodeInvalidKey, other.call(t, b.credential, http.MethodGet, ws("main"), nil, proofOptions{}).code)
}

func botNoBot(t *testing.T) {
	stranger := credential(t, instance(t, "--no-bot"))
	refused := newProcess(t).call(t, stranger, http.MethodGet, ws("main"), nil, proofOptions{})
	assert.Equal(t, http.StatusForbidden, refused.status)
	assert.Equal(t, v1alpha1.CodeUnknownBot, refused.code)
	// Control: a bot's instance is known.
	assert.Equal(t, http.StatusNotFound, newBot(t).call(t, http.MethodGet, ws("main"), nil).status)
}

func botOtherAttachment(t *testing.T) {
	b := newBot(t)
	other := newBot(t, "--attachment", "secret")
	require.Equal(t, http.StatusOK, other.acquire(t, "secret").status)
	refused := b.acquire(t, "secret")
	assert.Equal(t, http.StatusNotFound, refused.status)
	assert.Equal(t, v1alpha1.CodeNotFound, refused.code)
	// Control: the bot's own attachment is served.
	assert.Equal(t, http.StatusOK, b.acquire(t, "main").status)
}

func botRemade(t *testing.T) {
	b := newBot(t)
	b.running(t)
	require.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: token, Content: []byte("old")}).status)
	require.Equal(t, "old", b.file(t, token).Content) // setup
	oldUID := b.uid
	var remade struct {
		UID string `json:"uid"`
	}
	fixtureJSON(t, &remade, "remake", b.namespace, b.name)
	require.NotEqual(t, oldUID, remade.UID)
	b.uid, b.instance, b.process = remade.UID, instance(t, b.namespace, b.name), newProcess(t)
	b.credential = credential(t, b.instance)
	b.running(t)
	assert.False(t, b.file(t, token).Exists, "the remade bot's workspace is new")
	var old struct {
		Exists bool `json:"exists"`
	}
	fixtureJSON(t, &old, "exists", oldUID, "main")
	assert.False(t, old.Exists, "the old bot's workspace is gone")
}

func botEarlierIncarnation(t *testing.T) {
	b := newBot(t)
	b.running(t)
	earlier := *b
	var remade struct {
		UID string `json:"uid"`
	}
	fixtureJSON(t, &remade, "remake", b.namespace, b.name)
	require.NotEqual(t, earlier.uid, remade.UID) // setup
	b.uid, b.instance, b.process = remade.UID, instance(t, b.namespace, b.name), newProcess(t)
	b.credential = credential(t, b.instance)
	// Control: the remade bot's own instance reaches its workspace.
	b.running(t)
	for label, a := range map[string]answered{
		"acquire": earlier.acquire(t, "main"),
		"write":   earlier.write(t, v1alpha1.File{Path: token, Content: []byte("earlier")}),
	} {
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, a.status, "%s: %s", label, a.raw)
		assert.Contains(t, []v1alpha1.Code{v1alpha1.CodeInvalidCredential, v1alpha1.CodeUnknownBot}, a.code, label)
	}
	assert.False(t, b.file(t, token).Exists, "the remade bot's workspace holds nothing of the earlier instance's")
}

func acquireIdempotent(t *testing.T) {
	b := newBot(t, "--tool", "github")
	first, second := b.acquire(t, "main"), b.acquire(t, "main")
	require.Equal(t, http.StatusOK, first.status, first.raw)
	assert.Equal(t, workspaceOf(t, first), workspaceOf(t, second))
	w := workspaceOf(t, first)
	assert.Equal(t, "main", w.Attachment)
	assert.EqualValues(t, "running", w.Phase)
	assert.NotEmpty(t, w.Model.BaseURL)
	require.Len(t, w.Tools, 1)
	assert.Equal(t, "github", w.Tools[0].Name)
	assert.NotEmpty(t, w.Tools[0].URL)
}

// trustAnswered holds an acquire's and a read's model.trust to what CheckTrust takes, and to the same
// roots in both. A server whose workspaces verify their gateway with their system roots answers none,
// which CheckTrust takes too.
func trustAnswered(t *testing.T) {
	b := newBot(t)
	acquired := b.acquire(t, "main")
	require.Equal(t, http.StatusOK, acquired.status, acquired.raw)
	read := b.call(t, http.MethodGet, ws("main"), nil)
	require.Equal(t, http.StatusOK, read.status, read.raw)
	trust := workspaceOf(t, acquired).Model.Trust
	assert.NoError(t, v1alpha1.CheckTrust(trust))
	assert.Equal(t, trust, workspaceOf(t, read).Model.Trust, "a read answers other roots than the acquire before it")
}

func acquireRecovers(t *testing.T) {
	b := newBot(t)
	b.running(t)
	run(t, "seed", b.uid, "main", data, "kept")
	for _, phase := range []string{"--stopped", "--failed"} {
		run(t, "workspace", b.uid, "main", phase)
		read := workspaceOf(t, b.call(t, http.MethodGet, ws("main"), nil))
		require.NotEqualValues(t, "running", read.Phase, phase) // setup
		b.running(t)
		assert.Equal(t, "kept", b.file(t, data).Content, "the data survives %s", phase)
	}
}

// tokensGoneAfterStart holds what a client is told: a token file lives outside the data volume, so an
// acquire that started or recovered the workspace leaves none, and the client writes its tokens again.
func tokensGoneAfterStart(t *testing.T) {
	b := newBot(t)
	b.running(t)
	for _, phase := range []string{"--stopped", "--failed"} {
		require.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: token, Content: []byte("before")}).status)
		require.True(t, b.file(t, token).Exists, phase) // setup
		run(t, "workspace", b.uid, "main", phase)
		b.running(t)
		assert.False(t, b.file(t, token).Exists, "a token outlives %s", phase)
		require.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: token, Content: []byte("after")}).status)
		assert.Equal(t, "after", b.file(t, token).Content, "the token is written again after %s", phase)
	}
}

func acquireUnsupported(t *testing.T) {
	b := newBot(t, "--unsupported", "shared")
	refused := b.acquire(t, "shared")
	assert.Equal(t, http.StatusUnprocessableEntity, refused.status)
	assert.Equal(t, v1alpha1.CodeUnsupported, refused.code)
	assert.Equal(t, http.StatusOK, b.acquire(t, "main").status) // control
}

func acquireDeleting(t *testing.T) {
	b := newBot(t)
	b.running(t)
	run(t, "workspace", b.uid, "main", "--deleting")
	refused := b.acquire(t, "main")
	assert.Equal(t, http.StatusConflict, refused.status)
	assert.Equal(t, v1alpha1.CodeDeleting, refused.code)
	assert.NotEmpty(t, refused.header.Get(v1alpha1.HeaderRetryAfter))
	run(t, "workspace", b.uid, "main", "--running")
	assert.Equal(t, http.StatusOK, b.acquire(t, "main").status) // control
}

func deletedWithBot(t *testing.T) {
	gone, kept := newBot(t), newBot(t)
	gone.running(t)
	kept.running(t)
	run(t, "remove", gone.namespace, gone.name)
	var exists struct {
		Exists bool `json:"exists"`
	}
	fixtureJSON(t, &exists, "exists", gone.uid, "main")
	assert.False(t, exists.Exists, "a gone bot's workspace is deleted")
	fixtureJSON(t, &exists, "exists", kept.uid, "main")
	assert.True(t, exists.Exists, "a living bot's workspace is kept")
}

func readBeforeAcquire(t *testing.T) {
	b := newBot(t)
	refused := b.call(t, http.MethodGet, ws("main"), nil)
	assert.Equal(t, http.StatusNotFound, refused.status)
	assert.Equal(t, v1alpha1.CodeNotFound, refused.code)
	b.running(t)
	assert.Equal(t, http.StatusOK, b.call(t, http.MethodGet, ws("main"), nil).status) // control
}

func readTakesNothing(t *testing.T) {
	l := serverLimits(t)
	b := newBot(t)
	b.running(t)
	reads := l.BotBurst + 4
	callers := b.spread(t, instancesFor(l, reads))
	for i := range reads {
		require.Equal(t, http.StatusOK, callers[i%len(callers)].call(t, http.MethodGet, ws("main"), nil).status)
	}
	assert.Equal(t, http.StatusOK, b.acquire(t, "main").status, "reads took nothing from the bot's limit")
}

func pendingRetryAfter(t *testing.T) {
	b := newBot(t)
	b.running(t)
	run(t, "workspace", b.uid, "main", "--pending")
	read := b.call(t, http.MethodGet, ws("main"), nil)
	require.EqualValues(t, "pending", workspaceOf(t, read).Phase)
	assert.NotEmpty(t, read.header.Get(v1alpha1.HeaderRetryAfter))
	run(t, "workspace", b.uid, "main", "--running")
	assert.Empty(t, b.call(t, http.MethodGet, ws("main"), nil).header.Get(v1alpha1.HeaderRetryAfter), "control: a running one carries none")
}

func filesWritten(t *testing.T) {
	b := newBot(t)
	b.running(t)
	second := v1alpha1.TokenDir + "model-token"
	require.Equal(t, http.StatusNoContent, b.write(t,
		v1alpha1.File{Path: token, Content: []byte("first")},
		v1alpha1.File{Path: second, Content: []byte("second")},
		v1alpha1.File{Path: token, Content: []byte("last")}).status)
	assert.Equal(t, "last", b.file(t, token).Content, "files are written in order")
	assert.Equal(t, "second", b.file(t, second).Content)
}

func filesPathRefused(t *testing.T) {
	b := newBot(t)
	b.running(t)
	for _, path := range []string{
		v1alpha1.TokenDir, strings.TrimSuffix(v1alpha1.TokenDir, "/"), "/run/garam-bot-x/token",
		v1alpha1.TokenDir + "../etc/passwd", v1alpha1.TokenDir + "a//b", "/etc/passwd",
	} {
		refused := b.write(t, v1alpha1.File{Path: token, Content: []byte("valid")}, v1alpha1.File{Path: path, Content: []byte("x")})
		assert.Equal(t, http.StatusBadRequest, refused.status, path)
		assert.Equal(t, v1alpha1.CodeInvalid, refused.code, path)
		assert.False(t, b.file(t, token).Exists, "nothing of a refused write is written: %s", path)
	}
	assert.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: token, Content: []byte("valid")}).status) // control
}

func filesLinkReplaced(t *testing.T) {
	b := newBot(t)
	b.running(t)
	target := "/workspace/planted"
	run(t, "link", b.uid, "main", token, target)
	require.Equal(t, target, b.file(t, token).Link) // setup
	before := b.file(t, target).Content
	require.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: token, Content: []byte("token")}).status)
	written := b.file(t, token)
	assert.Empty(t, written.Link, "the link is replaced")
	assert.Equal(t, "token", written.Content)
	assert.Equal(t, before, b.file(t, target).Content, "the link's target is not written")
}

func filesInnerLink(t *testing.T) {
	b := newBot(t)
	b.running(t)
	dir, target := strings.TrimSuffix(token, "/token"), "/workspace/planted"
	run(t, "link", b.uid, "main", dir, target)
	require.Equal(t, target, b.file(t, dir).Link) // setup
	first := v1alpha1.TokenDir + "model-token"
	refused := b.write(t, v1alpha1.File{Path: first, Content: []byte("first")}, v1alpha1.File{Path: token, Content: []byte("token")})
	assert.Equal(t, http.StatusBadRequest, refused.status, refused.raw)
	assert.Equal(t, v1alpha1.CodeInvalid, refused.code)
	var body v1alpha1.ErrorBody
	require.NoError(t, json.Unmarshal([]byte(refused.raw), &body))
	assert.Equal(t, token, body.Path, "the refusal names the path it reached")
	assert.False(t, b.file(t, target+"/token").Exists, "nothing is written through the link")
	assert.Equal(t, "first", b.file(t, first).Content, "the files before it stay written")
	// Control: a path through no link is written.
	assert.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: first, Content: []byte("again")}).status)
}

func filesCount(t *testing.T) {
	b := newBot(t)
	b.running(t)
	var files []v1alpha1.File
	for i := range v1alpha1.MaxFiles + 1 {
		files = append(files, v1alpha1.File{Path: fmt.Sprintf("%sfile-%02d", v1alpha1.TokenDir, i), Content: []byte("x")})
	}
	for label, refused := range map[string]answered{"17 files": b.write(t, files...), "no file": b.write(t)} {
		assert.Equal(t, http.StatusBadRequest, refused.status, "%s: %s", label, refused.raw)
		assert.Equal(t, v1alpha1.CodeInvalid, refused.code, label)
	}
	assert.False(t, b.file(t, files[0].Path).Exists, "nothing of a refused write is written")
	// Control: 16 files are written.
	assert.Equal(t, http.StatusNoContent, b.write(t, files[:v1alpha1.MaxFiles]...).status)
	assert.True(t, b.file(t, files[v1alpha1.MaxFiles-1].Path).Exists)
}

func filesTooLarge(t *testing.T) {
	b := newBot(t)
	b.running(t)
	second := v1alpha1.TokenDir + "model-token"
	of := func(n int) []byte { return bytes.Repeat([]byte("x"), n) }
	for label, refused := range map[string]answered{
		"two files of 40 KiB": b.write(t, v1alpha1.File{Path: token, Content: of(40 << 10)}, v1alpha1.File{Path: second, Content: of(40 << 10)}),
		"one file of 200 KiB": b.write(t, v1alpha1.File{Path: token, Content: of(200 << 10)}),
	} {
		assert.Equal(t, http.StatusRequestEntityTooLarge, refused.status, "%s: %s", label, refused.raw)
		assert.Equal(t, v1alpha1.CodeInvalid, refused.code, label)
	}
	assert.False(t, b.file(t, token).Exists, "nothing of a refused write is written")
	// Control: 64 KiB in all, in two files, is written.
	half := v1alpha1.MaxContentBytes / 2
	assert.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: token, Content: of(half)}, v1alpha1.File{Path: second, Content: of(half)}).status)
}

func filesNotRunning(t *testing.T) {
	b := newBot(t)
	b.running(t)
	run(t, "workspace", b.uid, "main", "--stopped")
	refused := b.write(t, v1alpha1.File{Path: token, Content: []byte("t")})
	assert.Equal(t, http.StatusConflict, refused.status)
	assert.Equal(t, v1alpha1.CodeNotRunning, refused.code)
	b.running(t)
	assert.Equal(t, http.StatusNoContent, b.write(t, v1alpha1.File{Path: token, Content: []byte("t")}).status) // control
}

func dialEchoes(t *testing.T) {
	b := newBot(t)
	b.running(t)
	d := b.dial(t, "main", v1alpha1.DialProtocol)
	require.NotNil(t, d.conn, d.answer.raw)
	assert.True(t, echoes(t, d.conn), "the bytes pass both ways unchanged")
}

func dialOtherUpgrade(t *testing.T) {
	b := newBot(t)
	b.running(t)
	refused := b.dial(t, "main", "websocket")
	require.Nil(t, refused.conn)
	assert.Equal(t, http.StatusBadRequest, refused.answer.status)
	assert.Equal(t, v1alpha1.CodeInvalid, refused.answer.code)
	assert.NotNil(t, b.dial(t, "main", v1alpha1.DialProtocol).conn) // control
}

func dialNotRunning(t *testing.T) {
	b := newBot(t)
	b.running(t)
	run(t, "workspace", b.uid, "main", "--stopped")
	refused := b.dial(t, "main", v1alpha1.DialProtocol)
	require.Nil(t, refused.conn)
	assert.Equal(t, v1alpha1.CodeNotRunning, refused.answer.code)
	b.running(t)
	assert.NotNil(t, b.dial(t, "main", v1alpha1.DialProtocol).conn) // control
}

func dialFifth(t *testing.T) {
	b := newBot(t)
	b.running(t)
	var open []dialed
	for range v1alpha1.MaxDialsPerInstance {
		d := b.dial(t, "main", v1alpha1.DialProtocol)
		require.NotNil(t, d.conn, d.answer.raw)
		open = append(open, d)
	}
	refused := b.dial(t, "main", v1alpha1.DialProtocol)
	require.Nil(t, refused.conn)
	assert.Equal(t, http.StatusTooManyRequests, refused.answer.status)
	assert.Equal(t, v1alpha1.CodeTemporarilyUnavailable, refused.answer.code)
	// Control: once one closes, a dial is taken again.
	require.NoError(t, open[0].conn.Close())
	assert.Eventually(t, func() bool {
		d := b.dial(t, "main", v1alpha1.DialProtocol)
		return d.conn != nil
	}, 5*time.Second, 200*time.Millisecond)
}

func dialServerCap(t *testing.T) {
	b, other := newBot(t), newBot(t)
	b.running(t)
	other.running(t)
	setMaxDials(t, 1)
	// Earlier cases' dials close at their client, and the server forgets them in its own time.
	require.Eventually(t, func() bool { return b.dial(t, "main", v1alpha1.DialProtocol).conn != nil }, 5*time.Second, 100*time.Millisecond)
	refused := other.dial(t, "main", v1alpha1.DialProtocol)
	require.Nil(t, refused.conn)
	assert.Equal(t, http.StatusServiceUnavailable, refused.answer.status)
	assert.Equal(t, v1alpha1.CodeTemporarilyUnavailable, refused.answer.code)
}

// setMaxDials sets the server's dial cap for the case, and puts the earlier one back after it.
func setMaxDials(t *testing.T, n int) {
	t.Helper()
	var answer struct {
		Previous int `json:"previous"`
	}
	fixtureJSON(t, &answer, "max-dials", fmt.Sprint(n))
	t.Cleanup(func() { run(t, "max-dials", fmt.Sprint(answer.Previous)) })
}

func dialClosesWithInstance(t *testing.T) {
	b := newBot(t)
	b.running(t)
	d := b.dial(t, "main", v1alpha1.DialProtocol)
	require.True(t, echoes(t, d.conn)) // setup
	run(t, "end", b.instance)
	assert.True(t, closes(t, d.conn))
}

func dialClosesWithBot(t *testing.T) {
	b, kept := newBot(t), newBot(t)
	b.running(t)
	kept.running(t)
	d, control := b.dial(t, "main", v1alpha1.DialProtocol), kept.dial(t, "main", v1alpha1.DialProtocol)
	require.True(t, echoes(t, d.conn)) // setup
	run(t, "remove", b.namespace, b.name)
	assert.True(t, closes(t, d.conn))
	assert.True(t, echoes(t, control.conn), "control: another bot's dial stays open")
}

func dialClosesWithWorkspace(t *testing.T) {
	b := newBot(t)
	b.running(t)
	d := b.dial(t, "main", v1alpha1.DialProtocol)
	require.True(t, echoes(t, d.conn)) // setup
	run(t, "workspace", b.uid, "main", "--stopped")
	assert.True(t, closes(t, d.conn))
}

func tokensAnswered(t *testing.T) {
	b := newBot(t, "--tool", "github", "--tool", "search")
	b.running(t)
	a := b.call(t, http.MethodPost, ws("main")+v1alpha1.TokensSuffix, nil)
	require.Equal(t, http.StatusOK, a.status, a.raw)
	var tokens v1alpha1.SessionTokens
	require.NoError(t, json.Unmarshal([]byte(a.raw), &tokens))
	assert.NotEmpty(t, tokens.Model.Token)
	assert.Positive(t, tokens.Model.ExpiresIn)
	assert.LessOrEqual(t, tokens.Model.ExpiresIn, int64(3600))
	require.Len(t, tokens.Tools, 2)
	for _, name := range []string{"github", "search"} {
		assert.NotEmpty(t, tokens.Tools[name].Token, name)
		assert.LessOrEqual(t, tokens.Tools[name].ExpiresIn, int64(3600), name)
	}
}

func tokensNoIssuer(t *testing.T) {
	b := newBot(t)
	b.running(t)
	run(t, "issuer", "--off")
	t.Cleanup(func() { run(t, "issuer", "--on") })
	refused := b.call(t, http.MethodPost, ws("main")+v1alpha1.TokensSuffix, nil)
	assert.Equal(t, http.StatusServiceUnavailable, refused.status)
	assert.Equal(t, v1alpha1.CodeWaitingForGateway, refused.code)
	run(t, "issuer", "--on")
	assert.Equal(t, http.StatusOK, b.call(t, http.MethodPost, ws("main")+v1alpha1.TokensSuffix, nil).status) // control
}

// limitSlack is how many calls past a burst a limit test makes, for what a limit gives back meanwhile.
const limitSlack = 10

func limitBot(t *testing.T) {
	l := serverLimits(t)
	b, other := newBot(t), newBot(t)
	b.running(t)
	// Several instances share the calls, so no instance's own request limit is what refuses.
	callers := b.spread(t, instancesFor(l, 3*(l.BotBurst+limitSlack)))
	next := 0
	caller := func() *bot {
		next++
		return callers[next%len(callers)]
	}
	spend := func() answered {
		for range l.BotBurst + limitSlack {
			if a := caller().acquire(t, "main"); a.status != http.StatusOK {
				return a
			}
		}
		return answered{}
	}
	for _, kind := range []struct {
		name string
		call func(*bot) answered
	}{
		{"an acquire", func(c *bot) answered { return c.acquire(t, "main") }},
		{"a write of files", func(c *bot) answered { return c.write(t, v1alpha1.File{Path: token, Content: []byte("t")}) }},
		{"a dial", func(c *bot) answered {
			d := c.dial(t, "main", v1alpha1.DialProtocol)
			if d.conn != nil {
				_ = d.conn.Close()
			}
			return d.answer
		}},
		{"a session tokens call", func(c *bot) answered { return c.call(t, http.MethodPost, ws("main")+v1alpha1.TokensSuffix, nil) }},
	} {
		// The limit gives a call back in its own time, so the call may land just after; a few
		// tries leave that to chance no more than once in thousands.
		var a answered
		for range 3 {
			limited := spend()
			require.Equal(t, http.StatusTooManyRequests, limited.status, "a bot's calls are limited (LIMIT-1)")
			assert.Equal(t, v1alpha1.CodeTemporarilyUnavailable, limited.code)
			assert.NotEmpty(t, limited.header.Get(v1alpha1.HeaderRetryAfter))
			if a = kind.call(caller()); a.status == http.StatusTooManyRequests {
				break
			}
		}
		assert.Equal(t, http.StatusTooManyRequests, a.status, "%s takes from the bot's limit: %s", kind.name, a.raw)
	}
	assert.Equal(t, http.StatusOK, other.acquire(t, "main").status, "control: another bot is not limited by the first")
}

func limitRefusedTakesNothing(t *testing.T) {
	l := serverLimits(t)
	b := newBot(t, "--unsupported", "shared")
	b.running(t)
	callers := b.spread(t, instancesFor(l, 4*(l.BotBurst+1)))
	run(t, "issuer", "--off")
	t.Cleanup(func() { run(t, "issuer", "--on") })
	setMaxDials(t, 0)
	large := bytes.Repeat([]byte("x"), v1alpha1.MaxContentBytes+1)
	for _, kind := range []struct {
		name string
		code v1alpha1.Code
		call func(*bot) answered
	}{
		{"an unsupported acquire", v1alpha1.CodeUnsupported, func(c *bot) answered { return c.acquire(t, "shared") }},
		{"session tokens with no issuer", v1alpha1.CodeWaitingForGateway, func(c *bot) answered {
			return c.call(t, http.MethodPost, ws("main")+v1alpha1.TokensSuffix, nil)
		}},
		{"a dial past the server's cap", v1alpha1.CodeTemporarilyUnavailable, func(c *bot) answered {
			return c.dial(t, "main", v1alpha1.DialProtocol).answer
		}},
		{"a write above 64 KiB", v1alpha1.CodeInvalid, func(c *bot) answered { return c.write(t, v1alpha1.File{Path: token, Content: large}) }},
	} {
		for i := range l.BotBurst + 1 {
			a := kind.call(callers[i%len(callers)])
			require.NotEqual(t, http.StatusTooManyRequests, a.status, "%s took from the bot's limit", kind.name)
			require.Equal(t, kind.code, a.code, "%s: %s", kind.name, a.raw)
		}
		assert.Equal(t, http.StatusOK, b.acquire(t, "main").status, "%s took nothing from the bot's limit", kind.name)
	}
}

func limitInstance(t *testing.T) {
	l := serverLimits(t)
	b := newBot(t)
	thief := newProcess(t)
	// Setup: the process's key is pinned to the instance.
	require.Equal(t, http.StatusNotFound, b.call(t, http.MethodGet, ws("absent"), nil).status)
	calls := []struct {
		code v1alpha1.Code
		call func() answered
	}{
		{v1alpha1.CodeNotFound, func() answered { return b.call(t, http.MethodGet, ws("absent"), nil) }},
		{v1alpha1.CodeInvalidDPoPProof, func() answered {
			return b.process.call(t, b.credential, http.MethodGet, ws("absent"), nil, proofOptions{method: http.MethodPost})
		}},
		{v1alpha1.CodeInvalidKey, func() answered { return thief.call(t, b.credential, http.MethodGet, ws("absent"), nil, proofOptions{}) }},
	}
	// A server that counted only the calls it took would refuse after three times its burst.
	var limited answered
	for i := range l.InstanceBurst + l.InstanceBurst/2 + limitSlack {
		c := calls[i%len(calls)]
		if a := c.call(); a.status == http.StatusTooManyRequests {
			limited = a
			break
		} else {
			require.Equal(t, c.code, a.code, a.raw)
		}
	}
	require.Equal(t, http.StatusTooManyRequests, limited.status, "an instance's requests are limited, refused ones counted (LIMIT-2)")
	assert.Equal(t, v1alpha1.CodeTemporarilyUnavailable, limited.code)
	assert.NotEmpty(t, limited.header.Get(v1alpha1.HeaderRetryAfter))
	assert.Equal(t, http.StatusNotFound, newBot(t).call(t, http.MethodGet, ws("absent"), nil).status, "control: another instance is not limited")
}

func secretsNotInRefusal(t *testing.T) {
	b := newBot(t, "--tool", "github")
	b.running(t)
	a := b.call(t, http.MethodPost, ws("main")+v1alpha1.TokensSuffix, nil)
	require.Equal(t, http.StatusOK, a.status)
	var tokens v1alpha1.SessionTokens
	require.NoError(t, json.Unmarshal([]byte(a.raw), &tokens))
	refused := newProcess(t).call(t, b.credential, http.MethodPost, ws("main")+v1alpha1.TokensSuffix, nil, proofOptions{})
	require.Equal(t, v1alpha1.CodeInvalidKey, refused.code)
	for _, secret := range []string{tokens.Model.Token, tokens.Tools["github"].Token, b.credential} {
		assert.NotContains(t, refused.raw, secret)
	}
}

func dialIdle(t *testing.T) {
	b := newBot(t)
	b.running(t)
	d := b.dial(t, "main", v1alpha1.DialProtocol)
	require.True(t, echoes(t, d.conn)) // setup
	advanceClock(t, 50*time.Second)
	time.Sleep(500 * time.Millisecond)
	assert.True(t, echoes(t, d.conn), "a dial idle for 50 seconds stays open")
	advanceClock(t, 121*time.Second)
	assert.True(t, closes(t, d.conn), "a dial idle for 120 seconds is closed")
}
