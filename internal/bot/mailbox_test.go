package bot_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/agentfake"
	"github.com/garamsh/garam-bot/internal/bot"
)

// The mailbox is checked against a fake of garam's machine listener: pollBotMailbox as its contract
// states it, each hand-out signed as garam ADR-0101 states, and its keys served at
// /message-signing-keys. Its origin is not the address dialled, as behind a proxy.

const (
	botGRN      = "grn:acme/iam/bot/decl-1"
	garamOrigin = "https://machine.garam.test"
	botToken    = "bot-token-0123456789abcdef0123456789"
	sender      = "grn:acme/iam/agent/sender-1"
)

// clock is a test's clock, moved by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// signingKey is one of garam's message signing keys.
type signingKey struct {
	private *ecdsa.PrivateKey
	kid     string
}

func newSigningKey(t *testing.T) *signingKey {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	thumbprint, err := (&jose.JSONWebKey{Key: private.Public()}).Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	return &signingKey{private: private, kid: base64.RawURLEncoding.EncodeToString(thumbprint)}
}

// posted is one message posted to the bot, and how its hand-outs are tampered with.
type posted struct {
	id, body string
	// claims and header change the signature's claims and header before it is signed; key signs it in
	// place of garam's current key.
	claims func(map[string]any)
	typ    string
	key    *signingKey
	// contract is the content's contract in place of garam-message.v1.
	contract string
}

// garam is the fake machine listener.
type garam struct {
	t        *testing.T
	server   *httptest.Server
	rootFile string
	clock    func() time.Time
	identity *identity

	mu        sync.Mutex
	current   *signingKey
	published []*signingKey
	queue     []posted
	out       *posted
	outCursor string
	attempt   int
	acked     []string
	cursors   []string
	statuses  []int
	// redirected are the Authorization headers of the requests a redirect brought to /elsewhere.
	redirected []string
	keyReads   int
	keysDown   bool
	arrived    chan struct{}
}

func startGaram(t *testing.T, now func() time.Time) *garam {
	t.Helper()
	g := &garam{t: t, clock: now, arrived: make(chan struct{}, 1)}
	g.current = newSigningKey(t)
	g.published = []*signingKey{g.current}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /message-signing-keys", g.keys)
	mux.HandleFunc("POST /bots/{bot}/mailbox", g.poll)
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.redirected = append(g.redirected, r.Header.Get("Authorization"))
		g.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	g.server = httptest.NewTLSServer(mux)
	t.Cleanup(g.server.Close)
	g.rootFile = filepath.Join(t.TempDir(), "garam.crt")
	require.NoError(t, os.WriteFile(g.rootFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.server.Certificate().Raw}), 0o600))
	return g
}

func (g *garam) post(messages ...posted) {
	g.mu.Lock()
	g.queue = append(g.queue, messages...)
	g.mu.Unlock()
	select {
	case g.arrived <- struct{}{}:
	default:
	}
}

func (g *garam) keys(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.keyReads++
	if g.keysDown {
		http.Error(w, `{"kind":"unavailable","message":"down"}`, http.StatusServiceUnavailable)
		return
	}
	set := jose.JSONWebKeySet{}
	for _, key := range g.published {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: key.private.Public(), KeyID: key.kid, Algorithm: string(jose.ES256), Use: "sig"})
	}
	w.Header().Set("Content-Type", "application/jwk-set+json")
	w.Header().Set("Cache-Control", "max-age=3600")
	_ = json.NewEncoder(w).Encode(set)
}

// poll answers pollBotMailbox: the token and its proof checked, then a scripted status where one is
// queued, then the cursor's acknowledgement, the message handed out and not acknowledged again, or the
// next, or 204 after a short wait.
func (g *garam) poll(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r) {
		w.Header().Set("WWW-Authenticate", `DPoP algs="ES256"`)
		http.Error(w, `{"kind":"unauthenticated","message":"refused"}`, http.StatusUnauthorized)
		return
	}
	g.mu.Lock()
	g.cursors = append(g.cursors, r.URL.Query().Get("cursor"))
	if len(g.statuses) > 0 {
		status := g.statuses[0]
		g.statuses = g.statuses[1:]
		g.mu.Unlock()
		switch status {
		case http.StatusTooManyRequests:
			w.Header().Set("Retry-After", "7")
		case http.StatusTemporaryRedirect:
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"kind":"scripted","message":"a scripted refusal"}`))
		return
	}
	if cursor := r.URL.Query().Get("cursor"); g.out != nil && cursor == g.outCursor {
		g.acked = append(g.acked, g.out.id)
		g.out = nil
	}
	if g.out == nil && len(g.queue) > 0 {
		next := g.queue[0]
		g.queue, g.out, g.attempt = g.queue[1:], &next, 0
	}
	if g.out == nil {
		g.mu.Unlock()
		select {
		case <-g.arrived:
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	g.attempt++
	g.outCursor = fmt.Sprintf("cursor-%s-%d", g.out.id, g.attempt)
	delivery := g.handOut(*g.out, g.outCursor, g.attempt)
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(delivery)
}

// handOut signs one hand-out of m. g.mu is held.
func (g *garam) handOut(m posted, cursor string, attempt int) map[string]any {
	contract := "garam-message.v1"
	if m.contract != "" {
		contract = m.contract
	}
	content, err := json.Marshal(map[string]string{"contract": contract, "sender": sender, "body": m.body})
	require.NoError(g.t, err)
	digest := sha256.Sum256(content)
	now := g.clock()
	claims := map[string]any{
		"iss": garamOrigin, "sub": sender, "aud": botGRN, "org": "acme", "message_id": m.id,
		"content_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
		"iat":            now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
	if m.claims != nil {
		m.claims(claims)
	}
	key, typ := g.current, "agent-message+jwt"
	if m.key != nil {
		key = m.key
	}
	if m.typ != "" {
		typ = m.typ
	}
	options := (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", key.kid)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key.private}, options)
	require.NoError(g.t, err)
	payload, err := json.Marshal(claims)
	require.NoError(g.t, err)
	signed, err := signer.Sign(payload)
	require.NoError(g.t, err)
	signature, err := signed.CompactSerialize()
	require.NoError(g.t, err)
	return map[string]any{
		"cursor": cursor, "attempt": attempt, "content": string(content), "signature": signature,
		"message": map[string]any{"id": m.id, "recipient": botGRN, "sender": sender, "idempotencyKey": m.id, "body": m.body},
	}
}

// authorized checks the request as pollBotMailbox does: the token under DPoP, and its proof signed by
// the key it is bound to, for POST at this route's URL at garam's origin, with the token's hash.
func (g *garam) authorized(r *http.Request) bool {
	if r.Header.Get("Authorization") != "DPoP "+botToken {
		return false
	}
	proof, err := jose.ParseSignedCompact(r.Header.Get("DPoP"), []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(proof.Signatures) != 1 {
		return false
	}
	header := proof.Signatures[0].Protected
	if header.ExtraHeaders[jose.HeaderType] != "dpop+jwt" || header.JSONWebKey == nil {
		return false
	}
	thumbprint, err := header.JSONWebKey.Thumbprint(crypto.SHA256)
	if err != nil || base64.RawURLEncoding.EncodeToString(thumbprint) != g.identity.thumbprint {
		return false
	}
	payload, err := proof.Verify(header.JSONWebKey)
	if err != nil {
		return false
	}
	var claims struct {
		HTM, HTU, ATH, JTI string
		IAT                int64
	}
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	digest := sha256.Sum256([]byte(botToken))
	return claims.HTM == http.MethodPost && claims.HTU == garamOrigin+r.URL.EscapedPath() && claims.JTI != "" &&
		claims.ATH == base64.RawURLEncoding.EncodeToString(digest[:]) && claims.IAT > 0
}

func (g *garam) record() (acked, cursors []string, keyReads int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.acked...), append([]string(nil), g.cursors...), g.keyReads
}

// identity is the bot's garam identity: the token garam takes, and proofs signed by the process's key
// through TokenSource.Proof.
type identity struct {
	source     *bot.TokenSource
	thumbprint string

	mu      sync.Mutex
	value   string
	refused []string
}

func newIdentity(t *testing.T) *identity {
	t.Helper()
	key, err := bot.NewKey()
	require.NoError(t, err)
	source, err := bot.NewTokenSource("https://connector.test", "/ca.crt", "/credential", key, time.Now)
	require.NoError(t, err)
	thumbprint, err := (&jose.JSONWebKey{Key: key.Public()}).Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	return &identity{source: source, thumbprint: base64.RawURLEncoding.EncodeToString(thumbprint), value: botToken}
}

func (i *identity) token() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.value
}

func (i *identity) Token() string { return i.token() }

func (i *identity) Refused(token string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.refused = append(i.refused, token)
}

func (i *identity) Proof(method, url, token string) (string, error) {
	return i.source.Proof(method, url, token)
}

func (i *identity) refusals() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.refused...)
}

// mailboxOf is the Mailbox of botGRN at g, dialled at g's address with garamOrigin as its origin.
func mailboxOf(t *testing.T, g *garam, now func() time.Time, out *logs) *bot.Mailbox {
	t.Helper()
	g.identity = newIdentity(t)
	mailbox, err := bot.NewMailbox(bot.MailboxConfig{
		URL: g.server.URL, Origin: garamOrigin, RootFile: g.rootFile, GRN: botGRN, Identity: g.identity,
		Now: now, Logger: slog.New(slog.NewTextHandler(out, nil)),
	})
	require.NoError(t, err)
	return mailbox
}

func TestMailbox_GivesEachMessageAsATaskAndAcknowledgesItOnTheNextPoll(t *testing.T) {
	g := startGaram(t, time.Now)
	out := &logs{}
	mailbox := mailboxOf(t, g, time.Now, out)
	p := startAdapter(t)
	driver := runDriverOn(t, p, driverBot(agentfake.Harness), mailbox, &sleeps{}, out)

	g.post(posted{id: "msg-1", body: "write the release notes"}, posted{id: "msg:2/opaque", body: "and the patch notes"})
	require.Eventually(t, func() bool { return driver.Status().Completed == 2 }, 10*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { acked, _, _ := g.record(); return len(acked) == 2 }, 10*time.Second, time.Millisecond,
		"the last message is not acknowledged by the next poll")
	acked, cursors, keyReads := g.record()
	assert.Equal(t, []string{"msg-1", "msg:2/opaque"}, acked)
	assert.Equal(t, []string{"", "cursor-msg-1-1", "cursor-msg:2/opaque-1"}, cursors[:3],
		"each poll names the cursor of the message done before it")
	assert.Equal(t, 1, keyReads, "garam's keys are read once for both messages")

	var given []string
	for _, event := range p.events() {
		if event.Type == agent.EventMessage {
			given = append(given, event.Task+" "+event.Text)
		}
	}
	digest := sha256.Sum256([]byte("msg:2/opaque"))
	opaque := "m-" + fmt.Sprintf("%x", digest[:16])
	assert.Equal(t, []string{"msg-1 write the release notes", opaque + " and the patch notes"}, given,
		"each task's identifier is its message's, or a digest of one that is no task identifier")
	assert.NotContains(t, out.String(), "release notes", "a message's content is logged")
	assert.NotContains(t, out.String(), botToken)
}

func TestMailbox_TakesTheSenderGaramVouchesFor(t *testing.T) {
	g := startGaram(t, time.Now)
	mailbox := mailboxOf(t, g, time.Now, &logs{})
	g.post(posted{id: "msg-1", body: "write the release notes"})

	m, err := mailbox.Next(t.Context())
	require.NoError(t, err)
	assert.Equal(t, bot.Message{ID: "msg-1", Input: []agent.Input{{Text: "write the release notes", From: sender}}}, m)
}

func TestMailbox_RejectsAndAcknowledgesEachDeliveryWhoseSignatureFailsAndLogsNoneOfIt(t *testing.T) {
	now := &clock{now: time.Now()}
	g := startGaram(t, now.Now)
	out := &logs{}
	mailbox := mailboxOf(t, g, now.Now, out)
	p := startAdapter(t)
	driver := runDriverOn(t, p, driverBot(agentfake.Harness), mailbox, &sleeps{}, out)

	stranger := newSigningKey(t)
	set := func(claim string, value any) func(map[string]any) {
		return func(claims map[string]any) { claims[claim] = value }
	}
	refused := []posted{
		{id: "aud", body: "secret-aud", claims: set("aud", "grn:acme/iam/bot/another")},
		{id: "aud-list", body: "secret-aud-list", claims: set("aud", []string{botGRN})},
		{id: "iss", body: "secret-iss", claims: set("iss", "https://another.garam.test")},
		{id: "digest", body: "secret-digest", claims: set("content_sha256", "AAAA")},
		{id: "message-id", body: "secret-message-id", claims: set("message_id", "another")},
		{id: "expired", body: "secret-expired", claims: set("exp", now.Now().Add(-2*time.Minute).Unix())},
		{id: "ahead", body: "secret-ahead", claims: set("iat", now.Now().Add(2*time.Minute).Unix())},
		{id: "typ", body: "secret-typ", typ: "JWT"},
		{id: "stranger", body: "secret-stranger", key: stranger},
		{id: "contract", body: "secret-contract", contract: "garam-message.v2"},
	}
	g.post(refused...)
	// Expired and issued ahead within the 60 seconds of skew, it is taken.
	g.post(posted{id: "good", body: "write the release notes", claims: func(claims map[string]any) {
		claims["exp"], claims["iat"] = now.Now().Add(-30*time.Second).Unix(), now.Now().Add(30*time.Second).Unix()
	}})

	require.Eventually(t, func() bool { return driver.Status().Completed == 1 }, 10*time.Second, time.Millisecond)
	assert.Equal(t, len(refused), driver.Status().Rejected)
	acked, _, _ := g.record()
	var want []string
	for _, m := range refused {
		want = append(want, m.id)
	}
	require.GreaterOrEqual(t, len(acked), len(want))
	assert.Equal(t, want, acked[:len(want)], "each refused delivery is acknowledged, so it is not handed out again")
	var tasks []string
	for _, event := range p.events() {
		if event.Type == agent.EventTaskDone {
			tasks = append(tasks, event.Task)
		}
	}
	assert.Equal(t, []string{"good"}, tasks, "control: the delivery that verifies, alone, is given")
	for _, m := range refused {
		assert.NotContains(t, out.String(), m.body, "a refused delivery's content is logged")
	}
	assert.Contains(t, out.String(), "message refused", "control: the refusal is logged")
}

func TestMailbox_ReadsGaramsKeysOnceAnHourAndAgainForAKidItDoesNotHoldAtMostOnceAMinute(t *testing.T) {
	now := &clock{now: time.Now()}
	g := startGaram(t, now.Now)
	mailbox := mailboxOf(t, g, now.Now, &logs{})
	next := func(id string) bot.Message {
		t.Helper()
		g.post(posted{id: id, body: "a message"})
		m, err := mailbox.Next(t.Context())
		require.NoError(t, err)
		require.NoError(t, mailbox.Done(t.Context(), m, agent.OutcomeCompleted))
		return m
	}
	reads := func() int { _, _, n := g.record(); return n }

	assert.NotEmpty(t, next("one").Input)
	assert.NotEmpty(t, next("two").Input)
	assert.Equal(t, 1, reads(), "the keys are read again within the hour")
	now.advance(time.Hour)
	assert.NotEmpty(t, next("three").Input)
	assert.Equal(t, 2, reads(), "the keys are not read again after an hour")

	// garam rotates within the hour: a key it signs with now is read for once.
	g.mu.Lock()
	g.current = newSigningKey(t)
	g.published = append(g.published, g.current)
	g.mu.Unlock()
	now.advance(2 * time.Minute)
	assert.NotEmpty(t, next("rotated").Input, "a message signed by a key published since is refused")
	assert.Equal(t, 3, reads())

	// A kid garam does not publish reads the keys again once a minute, and is refused.
	stranger := newSigningKey(t)
	now.advance(2 * time.Minute)
	for _, id := range []string{"stranger-1", "stranger-2"} {
		g.post(posted{id: id, body: "a message", key: stranger})
		m, err := mailbox.Next(t.Context())
		require.NoError(t, err)
		assert.Empty(t, m.Input, "a message signed by a key garam does not publish is given")
		require.NoError(t, mailbox.Done(t.Context(), m, bot.OutcomeRejected))
	}
	assert.Equal(t, 4, reads(), "a kid not held reads the keys more than once a minute")
}

func TestMailbox_WaitsAsGaramAsksAndPollsWithoutACursorHandedOutAgain(t *testing.T) {
	g := startGaram(t, time.Now)
	mailbox := mailboxOf(t, g, time.Now, &logs{})
	g.post(posted{id: "one", body: "a message"})
	m, err := mailbox.Next(t.Context())
	require.NoError(t, err)
	require.NoError(t, mailbox.Done(t.Context(), m, agent.OutcomeCompleted))

	g.mu.Lock()
	g.statuses = []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusConflict}
	g.mu.Unlock()
	_, err = mailbox.Next(t.Context())
	var later *bot.RetryLater
	require.ErrorAs(t, err, &later)
	assert.Equal(t, 7*time.Second, later.After, "429's Retry-After")
	_, err = mailbox.Next(t.Context())
	require.Error(t, err, "503 is a failure the driver waits on")
	assert.NotErrorAs(t, err, &later)
	assert.NotContains(t, err.Error(), "a scripted refusal", "garam's message reaches the error")
	// The poll with no cursor is handed what was handed and not acknowledged, again.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	m, err = mailbox.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, "one", m.ID)

	_, cursors, _ := g.record()
	assert.Equal(t, []string{"", "cursor-one-1", "cursor-one-1", "cursor-one-1", ""}, cursors[:5],
		"429 and 503 poll again with the same cursor, and 409 with none")
	acked, _, _ := g.record()
	assert.Empty(t, acked, "control: the scripted answers acknowledged nothing")
}

func TestMailbox_FollowsNoRedirectWithItsToken(t *testing.T) {
	g := startGaram(t, time.Now)
	mailbox := mailboxOf(t, g, time.Now, &logs{})
	g.mu.Lock()
	g.statuses = []int{http.StatusTemporaryRedirect}
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := mailbox.Next(ctx)
	require.Error(t, err, "a redirect is a refusal")
	assert.Contains(t, err.Error(), "307")
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.redirected, "the poll and its token were sent where the redirect pointed")
}

func TestMailbox_TellsItsIdentityOfARefusedTokenAndPollsWithNone(t *testing.T) {
	g := startGaram(t, time.Now)
	mailbox := mailboxOf(t, g, time.Now, &logs{})
	setToken := func(value string) {
		g.identity.mu.Lock()
		defer g.identity.mu.Unlock()
		g.identity.value = value
	}

	setToken("a-token-garam-refuses")
	_, err := mailbox.Next(t.Context())
	require.Error(t, err)
	assert.Equal(t, []string{"a-token-garam-refuses"}, g.identity.refusals(), "the refused token is not reported")

	// Control: a token garam takes is not reported.
	setToken(botToken)
	g.post(posted{id: "one", body: "a message"})
	_, err = mailbox.Next(t.Context())
	require.NoError(t, err)
	assert.Len(t, g.identity.refusals(), 1)

	setToken("")
	_, cursors, _ := g.record()
	_, err = mailbox.Next(t.Context())
	require.Error(t, err, "a poll with no token held")
	_, after, _ := g.record()
	assert.Len(t, after, len(cursors), "a poll was sent with no token")
}

func TestMailbox_JudgesNoMessageWhileGaramsKeysCannotBeReadAndUsesTheKeysHeldADayMore(t *testing.T) {
	now := &clock{now: time.Now()}
	g := startGaram(t, now.Now)
	mailbox := mailboxOf(t, g, now.Now, &logs{})
	keysDown := func(down bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.keysDown = down
	}

	keysDown(true)
	g.post(posted{id: "one", body: "a message"})
	_, err := mailbox.Next(t.Context())
	require.Error(t, err, "a message judged with no keys read")
	acked, _, _ := g.record()
	assert.Empty(t, acked, "the message is acknowledged unjudged")

	keysDown(false)
	now.advance(time.Minute)
	m, err := mailbox.Next(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "one", m.ID)
	assert.NotEmpty(t, m.Input, "control: the message handed out again verifies once the keys are read")
	require.NoError(t, mailbox.Done(t.Context(), m, agent.OutcomeCompleted))

	// The keys held are used for 24 hours past their hour while garam cannot be read, and not after.
	keysDown(true)
	now.advance(24 * time.Hour)
	g.post(posted{id: "two", body: "a message"})
	m, err = mailbox.Next(t.Context())
	require.NoError(t, err)
	assert.NotEmpty(t, m.Input, "the keys held are dropped within the day")
	require.NoError(t, mailbox.Done(t.Context(), m, agent.OutcomeCompleted))
	now.advance(2 * time.Hour)
	g.post(posted{id: "three", body: "a message"})
	_, err = mailbox.Next(t.Context())
	require.Error(t, err, "keys more than a day past their hour are used")
}
