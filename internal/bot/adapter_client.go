package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// The adapter client's bounds: at most two connections, below DIAL-3's four dials of an instance, each
// closed idle before DIAL-3's 120 seconds; and a request's whole time, which leaves an events read held
// for MaxWait room to answer.
const (
	adapterConns   = 2
	adapterIdle    = time.Minute
	adapterTimeout = (agent.MaxWait + 20) * time.Second
)

// redialer dials the adapter, waiting before a dial that follows a connection the server broke or a
// dial that failed: from 1 second, doubling to 60, reset by a dial that is answered 101 (DIAL-2). The
// wait is the dial's, not the caller's, because the HTTP transport may dial again of its own accord
// for a request a broken connection did not answer.
type redialer struct {
	dial  func(ctx context.Context) (net.Conn, error)
	sleep func(ctx context.Context, d time.Duration) error

	mu     sync.Mutex
	wait   backoff
	broken bool
	failed error
}

// DialContext dials the adapter, whatever network and address the transport names.
func (r *redialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	r.mu.Lock()
	var wait time.Duration
	if r.broken {
		wait = r.wait.next(r.failed)
	}
	r.mu.Unlock()
	if wait > 0 {
		if err := r.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	conn, err := r.dial(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broken, r.failed = err != nil, err
	if err != nil {
		return nil, err
	}
	r.wait.reset()
	return &watchedConn{Conn: conn, broke: r.broke}, nil
}

func (r *redialer) broke() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broken = true
}

// watchedConn is one dialled connection, which tells broke when a read fails on a connection not
// closed by the process: one the server broke.
type watchedConn struct {
	net.Conn
	broke  func()
	closed atomic.Bool
}

func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil && !c.closed.Load() {
		c.broke()
	}
	return n, err
}

func (c *watchedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// errUnreached is a call that reached no adapter: the dial failed, or the connection broke.
var errUnreached = errors.New("the adapter was not reached")

// adapterClient speaks interface 2 to the adapter over the workspace's dial, with the adapter's token
// read again for each request (AUTH-1).
type adapterClient struct {
	http *http.Client
	dial *redialer
	// tokens answers the token last written into the workspace and the one being written.
	tokens func() (written, writing string)
}

func newAdapterClient(dial *redialer, tokens func() (string, string)) *adapterClient {
	return &adapterClient{
		dial:   dial,
		tokens: tokens,
		http: &http.Client{
			Timeout: adapterTimeout,
			Transport: &http.Transport{
				DialContext: dial.DialContext, MaxConnsPerHost: adapterConns, IdleConnTimeout: adapterIdle,
			},
		},
	}
}

// adapterRefusal is the adapter's refusal of one call. Its message names the call, the status and
// the code, and nothing of the request or of the adapter's message.
type adapterRefusal struct {
	call   string
	Status int
	Code   agent.Code
}

func (r *adapterRefusal) Error() string {
	return fmt.Sprintf("the adapter refused %s: %d %s", r.call, r.Status, r.Code)
}

// call makes one request of path, under PathPrefix, decodes a 2xx answer into answer where it is given,
// and answers the status. It carries the token last written, and once more the one being written where
// the adapter answers 401: a token swapped in the workspace before its write was answered, or a write
// whose answer was lost.
func (c *adapterClient) call(ctx context.Context, method, path string, body []byte, answer any) (int, error) {
	written, writing := c.tokens()
	status, read, err := c.do(ctx, method, path, body, written)
	if status == http.StatusUnauthorized && writing != "" && writing != written {
		status, read, err = c.do(ctx, method, path, body, writing)
	}
	if err != nil {
		return status, err
	}
	if status < 200 || status >= 300 {
		refused := &adapterRefusal{call: method + " " + path, Status: status}
		var parsed agent.ErrorBody
		if json.Unmarshal(read, &parsed) == nil {
			refused.Code = parsed.Error
		}
		return status, refused
	}
	if answer != nil {
		if err := json.Unmarshal(read, answer); err != nil {
			return 0, fmt.Errorf("decode the adapter's answer to %s %s: %w", method, path, err)
		}
	}
	return status, nil
}

func (c *adapterClient) do(ctx context.Context, method, path string, body []byte, token string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://adapter"+agent.PathPrefix+path, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		// The next dial waits, whatever broke the call: an answer that is no HTTP is not dialled for
		// again at once either.
		c.dial.broke()
		return 0, nil, fmt.Errorf("%w: %w", errUnreached, err)
	}
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	if err != nil {
		return 0, nil, fmt.Errorf("read the adapter's answer: %w", err)
	}
	return response.StatusCode, read, nil
}

// events reads the session's events after after, holding up to wait seconds for one, and keeps the
// types it knows (VER-2). It answers them and the number to read after next.
func (c *adapterClient) events(ctx context.Context, session string, after uint64, wait int) ([]agent.Event, uint64, error) {
	var page struct {
		Events []json.RawMessage `json:"events"`
		Next   uint64            `json:"next"`
	}
	path := "sessions/" + session + "/events?after=" + strconv.FormatUint(after, 10) + "&wait=" + strconv.Itoa(wait)
	if _, err := c.call(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, after, err
	}
	kept := make([]agent.Event, 0, len(page.Events))
	for _, raw := range page.Events {
		var event agent.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, after, fmt.Errorf("decode an event: %w", err)
		}
		switch event.Type {
		case agent.EventMessage, agent.EventToolCall, agent.EventToolResult, agent.EventTaskDone:
			kept = append(kept, event)
		}
	}
	return kept, page.Next, nil
}
