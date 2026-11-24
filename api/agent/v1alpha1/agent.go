package v1alpha1

import "encoding/json"

// SpecRelease is the release of v1alpha1's specification this package states.
const SpecRelease = "0.1.0"

// Version is the version of the protocol these types speak.
const Version = "agent/v1alpha1"

// PathPrefix is the path every operation of Version is served under.
const PathPrefix = "/" + Version + "/"

// Port is the workspace port the adapter listens on, which the caller reaches through the
// workspace provider's Dial (T-1).
const Port uint16 = 7700

// TokenPath is the file in the workspace holding the bearer token every request carries (AUTH-1).
const TokenPath = "/run/garam-bot/agent/token"

// MaxWait is the most seconds an events read holds for a change (EVT-1).
const MaxWait = 50

// Info is the body of GET .../info: which harness the adapter runs.
type Info struct {
	// Harness is the name a Bot's spec.agent.harness names, such as claude-code or codex.
	Harness string `json:"harness"`
	// Version is the harness's own version.
	Version string `json:"version"`
	// SpecRelease is the release of this version the adapter implements.
	SpecRelease string `json:"specRelease"`
}

// SessionSpec is the body of PUT .../sessions/{session}: what one session of the harness runs as.
type SessionSpec struct {
	Bot Bot `json:"bot"`
	// Instructions are the bot's instructions, which the adapter gives the harness as its system
	// prompt or its equivalent.
	Instructions string `json:"instructions,omitempty"`
	Model        Model  `json:"model"`
	// Tools are the MCP servers the harness reaches, and the only ones (TOOL-1).
	Tools []Tool `json:"tools,omitempty"`
	// WorkingDir is where the harness works, the workspace's data volume when empty.
	WorkingDir string `json:"workingDir,omitempty"`
}

// Bot names the bot a session runs for.
type Bot struct {
	Name string `json:"name"`
	// GRN is the bot's garam GRN, empty for a bot that runs without garam.
	GRN string `json:"grn,omitempty"`
}

// Model is where the harness sends its model requests (MODEL-1).
type Model struct {
	// BaseURL is the model gateway's base URL, interface 5's.
	BaseURL string `json:"baseUrl"`
	// TokenFile is the workspace file holding the short-lived token the gateway takes.
	TokenFile string `json:"tokenFile"`
	// Name is the model the harness asks for, the harness's default when empty.
	Name string `json:"name,omitempty"`
}

// Tool is one MCP server, reached over MCP's Streamable HTTP transport.
type Tool struct {
	// Name is the server's name as the harness shows it.
	Name string `json:"name"`
	URL  string `json:"url"`
	// TokenFile is the workspace file holding the short-lived bearer token the server takes.
	TokenFile string `json:"tokenFile"`
}

// SessionState is where a session is.
type SessionState string

// The session states. A caller reads a state it does not know as SessionUnknown.
const (
	// SessionIdle: no task is running.
	SessionIdle SessionState = "idle"
	// SessionWorking: a task is running.
	SessionWorking SessionState = "working"
	SessionUnknown SessionState = "unknown"
)

// Session is the body of GET .../sessions/{session}.
type Session struct {
	ID    string       `json:"id"`
	Spec  SessionSpec  `json:"spec"`
	State SessionState `json:"state"`
	// Task is the running task's identifier, empty while idle.
	Task string `json:"task,omitempty"`
}

// TaskRequest is the body of POST .../sessions/{session}/tasks.
type TaskRequest struct {
	// ID is the caller's identifier of the task, which makes the request idempotent (TASK-1).
	ID    string  `json:"id"`
	Input []Input `json:"input"`
}

// Input is one message the task gives the harness.
type Input struct {
	Text string `json:"text"`
	// From names who sent it, as the bot's mailbox records them.
	From string `json:"from,omitempty"`
}

// EventType says what an Event reports. A caller skips a type it does not know.
type EventType string

// The event types.
const (
	// EventMessage is text the harness answered with: Text.
	EventMessage EventType = "message"
	// EventToolCall is a call the harness made to a tool: Call.
	EventToolCall EventType = "toolCall"
	// EventToolResult is the result of a call: Result.
	EventToolResult EventType = "toolResult"
	// EventTaskDone is the task's end, its last event: Outcome and Reason (EVT-2).
	EventTaskDone EventType = "taskDone"
)

// Outcome is how a task ended.
type Outcome string

// The outcomes.
const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	// OutcomeStopped: the caller stopped the task (TASK-2).
	OutcomeStopped Outcome = "stopped"
)

// Event is one thing a session did.
type Event struct {
	// Seq numbers a session's events from 1, without a gap.
	Seq  uint64    `json:"seq"`
	Task string    `json:"task"`
	Type EventType `json:"type"`
	// Text is an EventMessage's text.
	Text   string      `json:"text,omitempty"`
	Call   *ToolCall   `json:"call,omitempty"`
	Result *ToolResult `json:"result,omitempty"`
	// Outcome and Reason are an EventTaskDone's.
	Outcome Outcome `json:"outcome,omitempty"`
	Reason  string  `json:"reason,omitempty"`
}

// ToolCall is a call to a tool, as the harness made it.
type ToolCall struct {
	ID    string          `json:"id"`
	Tool  string          `json:"tool"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ToolResult is the result of the call ID.
type ToolResult struct {
	ID      string `json:"id"`
	IsError bool   `json:"isError"`
}

// EventPage is the body of GET .../sessions/{session}/events: the events after the sequence number
// asked for, oldest first, and the number to ask after next.
type EventPage struct {
	Events []Event `json:"events"`
	Next   uint64  `json:"next"`
}

// Code names a refusal, so a caller branches on it and not on the message. A caller keeps a code
// it does not know as the string it is and acts on the status.
type Code string

// The codes, each with the status it comes with.
const (
	// CodeInvalid: 400, a body that is not the operation's JSON, names a member the adapter does
	// not know, or breaks a rule.
	CodeInvalid Code = "invalid"
	// CodeUnauthorized: 401, a request without the token of TokenPath.
	CodeUnauthorized Code = "unauthorized"
	// CodeNotFound: 404, no session or task under the path.
	CodeNotFound Code = "not-found"
	// CodeConflict: 409, the session or task identifier holds another body.
	CodeConflict Code = "conflict"
	// CodeBusy: 409, a session already lives, or a task already runs.
	CodeBusy Code = "busy"
	// CodeInternal: 500, a failure of the adapter's own.
	CodeInternal Code = "internal"
)

// ErrorBody is the body of every refusal and failure.
type ErrorBody struct {
	Error   Code   `json:"error"`
	Message string `json:"message"`
}
