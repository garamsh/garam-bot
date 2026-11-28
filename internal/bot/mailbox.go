package bot

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"golang.org/x/net/http/httpproxy"
)

// mailboxTimeout is how long a poll is read: garam holds one 30 seconds and more, and asks its clients
// to read at least 45 (pollBotMailbox).
const mailboxTimeout = 75 * time.Second

// MailboxConfig is what a Mailbox polls on.
type MailboxConfig struct {
	// URL is the base URL the bot dials garam's machine listener at.
	URL string
	// Origin is garam's machine origin, which is not the address dialled where a proxy stands between:
	// the htu of each proof is the route's URL at it, and it is the iss of every hand-out's signature,
	// compared as written. It is https://<lowercase host>[:<port>], with no path or trailing slash.
	Origin string
	// RootFile holds the root garam's certificate chains to, read again as it changes.
	RootFile string
	// GRN is the bot's garam GRN, the mailbox polled and the audience of every signature.
	GRN      string
	Identity GaramIdentity
	Now      func() time.Time
	Logger   *slog.Logger
}

// Mailbox is the MessageSource of a bot garam declared: garam's long poll of the messages addressed to
// it (pollBotMailbox, garam ADR-0194), which the bot's process opens with its own token. A poll names
// the cursor of the last message done, which acknowledges it. A message handed out and not
// acknowledged is handed out again, so delivery is at least once and a message's task identifier is
// derived from its identifier. Each hand-out's signature is verified as garam ADR-0101 says; one that
// fails is rejected and acknowledged, and no content of it is logged. One that cannot be judged,
// because garam's keys cannot be read, is held unjudged and judged again at the next Next, with no
// poll made meanwhile, so no attempt of it or another message is spent.
type Mailbox struct {
	cfg  MailboxConfig
	keys *messageKeys

	mu     sync.Mutex
	root   placeTrust
	client *http.Client
	// cursor acknowledges the last message done on the next poll; held is the cursor of the message
	// answered and not yet done; and pending the delivery handed out and not yet judged.
	cursor  string
	held    map[string]string
	pending *delivery
}

// NewMailbox returns the Mailbox under cfg.
func NewMailbox(cfg MailboxConfig) (*Mailbox, error) {
	if cfg.URL == "" || cfg.Origin == "" || cfg.RootFile == "" || cfg.GRN == "" || cfg.Identity == nil {
		return nil, errors.New("--mailbox-url, --mailbox-origin, --garam-root-file and --bot-grn are given together, with the bot's garam token")
	}
	if !canonicalOrigin(cfg.Origin) {
		// Another spelling of garam's origin would refuse, and so acknowledge, every message.
		return nil, errors.New("--mailbox-origin is https://<lowercase host>[:<port>], with no path or trailing slash")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	m := &Mailbox{cfg: cfg, root: placeTrust{file: cfg.RootFile}, held: map[string]string{}}
	m.keys = &messageKeys{url: cfg.URL, issuer: cfg.Origin, client: m.httpClient, now: cfg.Now}
	return m, nil
}

// delivery is one hand-out of pollBotMailbox, BotDelivery.
type delivery struct {
	Cursor  string `json:"cursor"`
	Attempt int    `json:"attempt"`
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	Content   string `json:"content"`
	Signature string `json:"signature"`
}

// content is the garam-message.v1 object a delivery's content is.
type content struct {
	Contract string `json:"contract"`
	Sender   string `json:"sender"`
	Body     string `json:"body"`
}

const contentContract = "garam-message.v1"

// Next judges the delivery held unjudged, where one is, and otherwise polls the mailbox until it answers
// a message. No poll is made while garam's keys cannot be read. A poll garam answers 429 answers a
// *RetryLater.
func (m *Mailbox) Next(ctx context.Context) (Message, error) {
	m.mu.Lock()
	pending := m.pending
	m.mu.Unlock()
	if pending != nil {
		got, _, err := m.judge(ctx, *pending)
		return got, err
	}
	for {
		got, ok, err := m.poll(ctx)
		if err != nil || ok {
			return got, err
		}
	}
}

// poll makes one poll, and answers no message where it is to poll again at once.
func (m *Mailbox) poll(ctx context.Context) (Message, bool, error) {
	token := m.cfg.Identity.Token()
	if token == "" {
		return Message{}, false, errors.New("the bot holds no garam token to poll its mailbox with")
	}
	if err := m.keys.ready(ctx); err != nil {
		return Message{}, false, err
	}
	client, err := m.httpClient()
	if err != nil {
		return Message{}, false, err
	}
	// The GRN is one segment, its / as %2F and its : literal, as garam compares the paths as written.
	route := "/bots/" + url.PathEscape(m.cfg.GRN) + "/mailbox"
	proof, err := m.cfg.Identity.Proof(http.MethodPost, m.cfg.Origin+route, token)
	if err != nil {
		return Message{}, false, err
	}
	m.mu.Lock()
	cursor := m.cursor
	m.mu.Unlock()
	target := strings.TrimRight(m.cfg.URL, "/") + route
	if cursor != "" {
		target += "?cursor=" + url.QueryEscape(cursor)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return Message{}, false, err
	}
	request.Header.Set("Authorization", "DPoP "+token)
	request.Header.Set(headerProof, proof)
	response, err := client.Do(request)
	if err != nil {
		// A poll aborted is sent again with the same cursor. The error names no URL, so no cursor.
		var failed *url.Error
		if errors.As(err, &failed) {
			err = failed.Err
		}
		return Message{}, false, fmt.Errorf("poll the mailbox: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDelivery))
	if err != nil {
		return Message{}, false, fmt.Errorf("read the mailbox's answer: %w", err)
	}
	switch response.StatusCode {
	case http.StatusOK:
		return m.take(ctx, body)
	case http.StatusNoContent:
		return Message{}, false, nil
	case http.StatusConflict:
		// The message the cursor names was handed out again since: poll with no cursor.
		m.mu.Lock()
		m.cursor = ""
		m.mu.Unlock()
		return Message{}, false, nil
	case http.StatusUnauthorized:
		m.cfg.Identity.Refused(token)
	case http.StatusTooManyRequests:
		if seconds, err := strconv.Atoi(response.Header.Get(headerRetryAfter)); err == nil && seconds > 0 {
			return Message{}, false, &RetryLater{After: time.Duration(seconds) * time.Second}
		}
	}
	return Message{}, false, &mailboxRefusal{Status: response.StatusCode, Code: refusalCode(body)}
}

// maxDelivery bounds what is read of one poll's answer: a message of MaxMessage, quoted, and its
// signature beside it.
const maxDelivery = 4*MaxMessage + maxAnswer

// take reads a delivery and judges it.
func (m *Mailbox) take(ctx context.Context, body []byte) (Message, bool, error) {
	var d delivery
	if err := json.Unmarshal(body, &d); err != nil || d.Cursor == "" || d.Message.ID == "" {
		return Message{}, false, errors.New("the mailbox answered no delivery")
	}
	return m.judge(ctx, d)
}

// judge verifies d. One whose signature or content garam's contract refuses is answered as a message of
// no input, which the driver rejects, so it is acknowledged. One that cannot be judged is held, and
// answered as a failure the driver backs off on; held past its signature's exp, it is let go
// unacknowledged, so the next poll is handed it again, signed anew.
func (m *Mailbox) judge(ctx context.Context, d delivery) (Message, bool, error) {
	id := messageTaskID(d.Message.ID)
	signed, err := m.keys.verify(ctx, d.Signature, d.Content, d.Message.ID, m.cfg.GRN)
	m.mu.Lock()
	defer m.mu.Unlock()
	if errors.Is(err, errKeys) {
		m.pending = &d
		if m.keys.lapsed(d.Signature) {
			m.pending = nil
		}
		return Message{}, false, err
	}
	m.pending = nil
	m.held[id] = d.Cursor
	var c content
	if err == nil {
		if decodeErr := json.Unmarshal([]byte(d.Content), &c); decodeErr != nil || c.Contract != contentContract {
			err = errors.New("the content is no " + contentContract)
		}
	}
	if err != nil {
		m.cfg.Logger.Warn("message refused; rejected", "task", id, "attempt", d.Attempt, "error", err)
		return Message{ID: id}, true, nil
	}
	return Message{ID: id, Input: []agent.Input{{Text: c.Body, From: signed.Sender}}}, true, nil
}

// Done acknowledges msg on the next poll, which names its cursor, whatever its outcome.
func (m *Mailbox) Done(_ context.Context, msg Message, _ agent.Outcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cursor, ok := m.held[msg.ID]
	if !ok {
		return fmt.Errorf("the message %s was not handed out by the mailbox", msg.ID)
	}
	delete(m.held, msg.ID)
	m.cursor = cursor
	return nil
}

// messageTaskID is the task identifier of the message garam identifies by id: id itself where it is
// one, and otherwise a digest of it, so every hand-out of one message gives one task (TASK-1).
func messageTaskID(id string) string {
	if taskID.MatchString(id) {
		return id
	}
	digest := sha256.Sum256([]byte(id))
	return "m-" + hex.EncodeToString(digest[:16])
}

// httpClient answers the client trusting garam's root alone, built again when the root file changes.
// It reads a poll for mailboxTimeout, follows no redirect, and goes through the proxy the
// environment names, read as it is built.
func (m *Mailbox) httpClient() (*http.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed, err := m.root.read()
	if err != nil {
		return nil, fmt.Errorf("read garam's root: %w", err)
	}
	if changed || m.client == nil {
		proxy := httpproxy.FromEnvironment().ProxyFunc()
		m.client = &http.Client{
			Timeout: mailboxTimeout,
			Transport: &http.Transport{
				Proxy:           func(r *http.Request) (*url.URL, error) { return proxy(r.URL) },
				TLSClientConfig: &tls.Config{RootCAs: m.root.roots, MinVersion: tls.VersionTLS12},
			},
			// A redirect would carry the token wherever it pointed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return m.client, nil
}

// canonicalOrigin reports whether origin is the one spelling garam's issuer is compared by (ADR-0101):
// https, a lowercase host, a port only where one is written, and nothing after it.
func canonicalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	return u.Host == strings.ToLower(u.Host) && !strings.HasSuffix(u.Host, ":") && "https://"+u.Host == origin
}

// mailboxRefusal is garam's refusal of a poll. Its message names the status and the code, and nothing
// of the request or of garam's message.
type mailboxRefusal struct {
	Status int
	Code   string
}

func (r *mailboxRefusal) Error() string {
	return fmt.Sprintf("garam refused the mailbox poll: %d %s", r.Status, r.Code)
}

// refusalCode is the kind of garam's Error body, empty where it has none.
func refusalCode(body []byte) string {
	var parsed struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(body, &parsed)
	return parsed.Kind
}
