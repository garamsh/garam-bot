package claudeadapter

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// streamLine is one line of `claude -p --output-format stream-json --verbose`, of which the
// adapter reads what the events need.
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// IsError and Result are the final `result` line's.
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Message struct {
		// Content is an array of blocks, or a string for a user line that replays a prompt.
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

// outcome is how a run of the harness ended, read from its `result` line.
type outcome struct {
	// Seen is whether the harness reported an end at all.
	Seen    bool
	Outcome v1alpha1.Outcome
	Reason  string
}

// readStream turns the harness's output into events, calling emit for each in order, until the
// stream ends. A line of a type the adapter does not read is skipped: the stream is the harness's,
// and carries more than the interface reports. Output that is not JSON ends the reading.
func readStream(r io.Reader, emit func(v1alpha1.Event)) outcome {
	var end outcome
	dec := json.NewDecoder(r)
	for {
		var line streamLine
		if err := dec.Decode(&line); err != nil {
			// A syntax error leaves the decoder unable to resynchronise, so the rest is read and
			// dropped: the harness must not block on a pipe nobody reads.
			_, _ = io.Copy(io.Discard, r)

			return end
		}
		switch line.Type {
		case "assistant", "user":
			for _, b := range blocks(line.Message.Content) {
				if e, ok := b.event(line.Type); ok {
					emit(e)
				}
			}
		case "result":
			end = outcome{Seen: true, Outcome: v1alpha1.OutcomeCompleted}
			if line.IsError || line.Subtype != "success" {
				end.Outcome, end.Reason = v1alpha1.OutcomeFailed, failure(line)
			}
		}
	}
}

func blocks(raw json.RawMessage) []block {
	var bs []block
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil
	}

	return bs
}

// event is the interface's event for a block, without its sequence number or task.
func (b block) event(line string) (v1alpha1.Event, bool) {
	switch {
	case line == "assistant" && b.Type == "text" && b.Text != "":
		return v1alpha1.Event{Type: v1alpha1.EventMessage, Text: b.Text}, true
	case line == "assistant" && b.Type == "tool_use":
		server, name := splitTool(b.Name)

		return v1alpha1.Event{Type: v1alpha1.EventToolCall, Call: &v1alpha1.ToolCall{ID: b.ID, Tool: server, Name: name, Input: b.Input}}, true
	case line == "user" && b.Type == "tool_result":
		return v1alpha1.Event{Type: v1alpha1.EventToolResult, Result: &v1alpha1.ToolResult{ID: b.ToolUseID, IsError: b.IsError}}, true
	}

	return v1alpha1.Event{}, false
}

// splitTool reads the name Claude Code gives a tool: an MCP tool is mcp__<server>__<tool>, and
// the server is empty for a tool of the harness's own, such as Bash.
func splitTool(name string) (server, tool string) {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return "", name
	}
	server, tool, ok = strings.Cut(rest, "__")
	if !ok {
		return "", name
	}

	return server, tool
}

func failure(line streamLine) string {
	if line.Result != "" {
		return line.Result
	}

	return "the harness ended with " + line.Subtype
}
