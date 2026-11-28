package gatewayfake

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Seen is one call a Provider, or a Recorder, received. Body is held up to maxSeenBody, and Size is
// the whole body's length.
type Seen struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Query  string              `json:"query"`
	Header map[string][]string `json:"header"`
	Body   []byte              `json:"body"`
	Size   int64               `json:"size"`
}

const maxSeenBody = 1 << 20

func seenOf(r *http.Request) Seen {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxSeenBody))
	rest, _ := io.Copy(io.Discard, r.Body)
	s := Seen{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Header: r.Header.Clone(), Size: int64(len(body)) + rest}
	if rest == 0 {
		s.Body = body
	}
	if r.Host != "" {
		s.Header["Host"] = []string{r.Host}
	}
	return s
}

// Answer is how a Provider answers its next call.
type Answer struct {
	// Status is the status answered. Zero is 200.
	Status int `json:"status,omitempty"`
	// RetryAfter is the Retry-After answered with Status, where it is set.
	RetryAfter string `json:"retryAfter,omitempty"`
	// Stream answers a stream of Messages events, its last sent once Release is called.
	Stream bool `json:"stream,omitempty"`
	// Long pads a stream with more than a MiB of text before its last event.
	Long bool `json:"long,omitempty"`
	// Break hangs up a stream after its first event, mid-answer.
	Break bool `json:"break,omitempty"`
	// Redirect answers that status, a 3xx, with a Location at the Provider's Elsewhere.
	Redirect int `json:"redirect,omitempty"`
	// Close hangs up with no answer.
	Close bool `json:"close,omitempty"`
}

// What a Provider's answers report and carry.
const (
	// The tokens a whole answer reports, and a stream's events end on.
	ProviderInputTokens         = 11
	ProviderOutputTokens        = 7
	ProviderCacheCreationTokens = 3
	ProviderCacheReadTokens     = 5
	// ProviderHeader is a header of the provider's own, which the gateway passes on (MOD-2), with
	// ProviderRequestID.
	ProviderHeader    = "Request-Id"
	ProviderRequestID = "req-fake"
)

// Provider stands in for the Anthropic Messages API: it records each call it receives and answers as
// scripted, a whole Messages answer otherwise.
type Provider struct {
	// Elsewhere is the URL a scripted redirect points at, a Recorder's.
	Elsewhere string

	mu      sync.Mutex
	seen    []Seen
	answers []Answer
	release chan struct{}
}

// Script queues the answers of the next calls.
func (p *Provider) Script(answers ...Answer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answers = append(p.answers, answers...)
}

// Release lets a held stream send its last event.
func (p *Provider) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil {
		close(p.release)
		p.release = nil
	}
}

// Seen are the calls received, in order.
func (p *Provider) Seen() []Seen {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.seen)
}

func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	seen := seenOf(r)
	p.mu.Lock()
	p.seen = append(p.seen, seen)
	answer := Answer{}
	if len(p.answers) > 0 {
		answer, p.answers = p.answers[0], p.answers[1:]
	}
	var release chan struct{}
	if answer.Stream {
		release = make(chan struct{})
		p.release = release
	}
	p.mu.Unlock()

	w.Header().Set(ProviderHeader, ProviderRequestID)
	w.Header().Set("Set-Cookie", "provider-session=1")
	switch {
	case answer.Close:
		hangUp(w)
	case answer.Redirect != 0:
		w.Header().Set("Location", strings.TrimRight(p.Elsewhere, "/")+r.URL.Path)
		w.WriteHeader(answer.Redirect)
	case answer.Stream:
		p.stream(w, r, answer, release)
	default:
		status := answer.Status
		if status == 0 {
			status = http.StatusOK
		}
		if answer.RetryAfter != "" {
			w.Header().Set("Retry-After", answer.RetryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "message", "model": "claude-test", "content": []map[string]string{{"type": "text", "text": "ready"}},
			"usage": usage(ProviderOutputTokens),
		})
	}
}

func (p *Provider) stream(w http.ResponseWriter, r *http.Request, answer Answer, release chan struct{}) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	write := func(event string, data any) {
		encoded, _ := json.Marshal(data)
		_, _ = io.WriteString(w, "event: "+event+"\ndata: "+string(encoded)+"\n\n")
		w.(http.Flusher).Flush()
	}
	write("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"model": "claude-test", "usage": usage(1),
	}})
	if answer.Break {
		hangUp(w)
		return
	}
	select {
	case <-release:
	case <-r.Context().Done():
		return
	}
	if answer.Long {
		text := strings.Repeat("x", 64<<10)
		for i := range 24 {
			write("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]string{"type": "text_delta", "text": strconv.Itoa(i) + text}})
		}
	}
	write("message_delta", map[string]any{"type": "message_delta", "usage": map[string]int{"output_tokens": ProviderOutputTokens}})
}

func usage(output int) map[string]int {
	return map[string]int{
		"input_tokens": ProviderInputTokens, "output_tokens": output,
		"cache_creation_input_tokens": ProviderCacheCreationTokens, "cache_read_input_tokens": ProviderCacheReadTokens,
	}
}

func hangUp(w http.ResponseWriter) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		_ = conn.Close()
	}
}

// Recorder records each call it receives and answers 200, standing where a redirect points (MOD-5).
type Recorder struct {
	mu   sync.Mutex
	seen []Seen
}

func (rec *Recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	seen := seenOf(r)
	rec.mu.Lock()
	rec.seen = append(rec.seen, seen)
	rec.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// Seen are the calls received, in order.
func (rec *Recorder) Seen() []Seen {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.seen)
}
