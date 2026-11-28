package gatewayfake

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
)

// Seen is one call a Provider received.
type Seen struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Query  string              `json:"query"`
	Header map[string][]string `json:"header"`
	Body   []byte              `json:"body"`
}

// Answer is how a Provider answers its next call.
type Answer struct {
	// Status is the status answered. Zero is 200.
	Status int `json:"status,omitempty"`
	// Stream answers a stream of two Messages events, the second sent once Release is called.
	Stream bool `json:"stream,omitempty"`
	// Redirect answers a 307 to another address.
	Redirect bool `json:"redirect,omitempty"`
	// Close hangs up with no answer.
	Close bool `json:"close,omitempty"`
}

// ProviderUsage is what a Provider's answers report: input and output tokens, in a whole answer and
// across a stream's events alike.
const (
	ProviderInputTokens  = 11
	ProviderOutputTokens = 7
	// ProviderHeader is a header of the provider's own, which the gateway passes on (MOD-2).
	ProviderHeader = "Request-Id"
)

// Provider stands in for the Anthropic Messages API: it records each call it receives and answers as
// scripted, a whole Messages answer otherwise.
type Provider struct {
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

// Release lets a held stream send its second event.
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
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.seen = append(p.seen, Seen{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
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

	w.Header().Set(ProviderHeader, "req-fake")
	w.Header().Set("Set-Cookie", "provider-session=1")
	switch {
	case answer.Close:
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	case answer.Redirect:
		w.Header().Set("Location", "https://elsewhere.example/v1/messages")
		w.WriteHeader(http.StatusTemporaryRedirect)
	case answer.Stream:
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		write := func(event string, data any) {
			encoded, _ := json.Marshal(data)
			_, _ = io.WriteString(w, "event: "+event+"\ndata: "+string(encoded)+"\n\n")
			w.(http.Flusher).Flush()
		}
		write("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"model": "claude-test", "usage": map[string]int{"input_tokens": ProviderInputTokens, "output_tokens": 1},
		}})
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		write("message_delta", map[string]any{"type": "message_delta", "usage": map[string]int{"output_tokens": ProviderOutputTokens}})
	default:
		status := answer.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "message", "model": "claude-test", "content": []map[string]string{{"type": "text", "text": "ready"}},
			"usage": map[string]int{"input_tokens": ProviderInputTokens, "output_tokens": ProviderOutputTokens},
		})
	}
}
