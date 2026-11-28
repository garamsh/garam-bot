// Package claudetest holds the stand-ins the claude-code adapter's tests put where the model
// gateway and a tool server would be: a gateway that speaks enough of the Anthropic Messages API
// for Claude Code to hold a conversation without a model, and an MCP server of one tool. Both
// record the bearer token each request carried, so a test can say which token reached which.
package claudetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// HoldText is the input of a task the gateway never answers, so the harness keeps working on it
// until it is stopped.
const HoldText = "conformance: hold"

// ToolText asks the gateway for a call of the MCP tool Tool, which it then answers with the
// tool's result.
const ToolText = "use the stub tool"

// Tool is the name of the one tool of Server, as Claude Code names it.
const Tool = "mcp__stub__echo"

// Seen is a request a stand-in took.
type Seen struct {
	Path string
	// Authorization is the Authorization header the request carried.
	Authorization string
	// APIKey is the x-api-key header the request carried.
	APIKey string
	// Host is the Host header the request carried.
	Host string
}

// Recorder keeps what a stand-in saw.
type Recorder struct {
	mu   sync.Mutex
	seen []Seen
}

// Record notes a request.
func (r *Recorder) Record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, Seen{Path: req.URL.Path, Authorization: req.Header.Get("Authorization"), APIKey: req.Header.Get("x-api-key"), Host: req.Host})
}

// Seen returns the requests taken so far, oldest first.
func (r *Recorder) Seen() []Seen {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]Seen(nil), r.seen...)
}

// Gateway answers Anthropic Messages API requests with scripted text.
type Gateway struct {
	Recorder
}

// ServeHTTP serves POST /v1/messages and refuses everything else.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.Record(r)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Claude Code puts its own context after the prompt, so the input is every message since the
	// harness last spoke.
	var last string
	for i := len(req.Messages) - 1; i >= 0 && req.Messages[i].Role != "assistant"; i-- {
		last += string(req.Messages[i].Content)
	}
	var blocks []map[string]any
	switch {
	case strings.Contains(last, HoldText):
		<-r.Context().Done()
		return
	case strings.Contains(last, `"tool_result"`):
		blocks = []map[string]any{{"type": "text", "text": "the tool answered"}}
	case strings.Contains(last, ToolText):
		blocks = []map[string]any{{"type": "tool_use", "id": "toolu_stub", "name": Tool, "input": map[string]any{"text": "hello"}}}
	default:
		blocks = []map[string]any{{"type": "text", "text": "ready"}}
	}
	stop := "end_turn"
	if blocks[0]["type"] == "tool_use" {
		stop = "tool_use"
	}
	message := map[string]any{"id": "msg_stub", "type": "message", "role": "assistant", "model": "stub", "content": blocks, "stop_reason": stop, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}
	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	start := map[string]any{"id": "msg_stub", "type": "message", "role": "assistant", "model": "stub", "content": []any{}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}
	event(w, "message_start", map[string]any{"type": "message_start", "message": start})
	for i, b := range blocks {
		empty := map[string]any{"type": b["type"]}
		var delta map[string]any
		if b["type"] == "text" {
			empty["text"] = ""
			delta = map[string]any{"type": "text_delta", "text": b["text"]}
		} else {
			empty["id"], empty["name"], empty["input"] = b["id"], b["name"], map[string]any{}
			raw, _ := json.Marshal(b["input"])
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(raw)}
		}
		event(w, "content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": empty})
		event(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": delta})
		event(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	event(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 1}})
	event(w, "message_stop", map[string]any{"type": "message_stop"})
}

func event(w http.ResponseWriter, name string, data any) {
	raw, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// Server is an MCP server of one tool, echo, over the Streamable HTTP transport, answering each
// POST with one JSON body.
type Server struct {
	Recorder
}

// ServeHTTP serves POST requests of JSON-RPC and refuses the rest.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Record(r)
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "stub", "version": "1"}}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echoes its text.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}}}}
	case "tools/call":
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo"}}}
	default:
		result = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}
