// Package memory holds the types of the conversation memory store, the candidate for the bot's
// memory (ADR 0040): the visible messages it keeps, the episodes it writes from them and
// the interfaces its callers supply. No binary reaches it until the bot-memory interface is decided.
package memory

import "context"

// Role identifies who produced a conversation message.
type Role string

const (
	// RoleUser marks an inbound task from the person talking to the bot.
	RoleUser Role = "user"
	// RoleAssistant marks a model response.
	RoleAssistant Role = "assistant"
)

// SourceMessage is visible conversation evidence. Runtime instructions, tool
// output, and model reasoning have no representation in this type.
// ExternalID is unique within (Issuer, Sender): the verified issuer and
// subject of a signed message, or an empty Issuer and the transport
// principal that delivered an unsigned one. Poster is the transport
// principal that posted the message, signed or not.
type SourceMessage struct {
	ID         int64
	ExternalID string
	Issuer     string
	Sender     string
	Poster     string
	Role       Role
	Content    string
}

// Synthesis is one batch's episode as the summarizer wrote it: the
// summary and the names of the entities the batch mentions, in the order
// it wrote them.
type Synthesis struct {
	Summary  string
	Entities []string
}

// Embedder turns text into a vector whose cosine with another of the same
// model's vectors measures their similarity.
type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
}

// ConversationSummarizer synthesizes one bounded episode from new messages.
type ConversationSummarizer interface {
	Summarize(context.Context, []string) (Synthesis, error)
}

// ConversationHit is an immutable mid episode linked directly to its source interval.
type ConversationHit struct {
	ID          int64
	Summary     string
	SourceFirst int64
	SourceLast  int64
}

// Linked is the entity walk's answer for a query: the stored entity names
// the query contains, sorted, and the mid episodes reached from them, best
// first.
type Linked struct {
	Seeds []string
	Hits  []LinkedHit
}

// LinkedHit is a mid episode the walk reached. Its subject is the first
// entity its synthesis named.
type LinkedHit struct {
	ConversationHit
	Subject string
}

// ConversationMemory persists visible turns and links retrieval to evidence.
type ConversationMemory interface {
	RecordTurn(context.Context, []SourceMessage, ConversationSummarizer) error
	Recent(context.Context, int) ([]SourceMessage, error)
	Search(context.Context, string, int) ([]ConversationHit, error)
	PendingSources(context.Context) ([]SourceMessage, error)
	RecentMid(context.Context, int) ([]ConversationHit, error)
	// Linked ranks the mid episodes the entity walk reaches from the
	// entities the query names.
	Linked(context.Context, string) (Linked, error)
	Source(context.Context, int64) (SourceMessage, error)
	// Note returns the saved note, empty when none has been written.
	Note(context.Context) (string, error)
	// SetNote replaces the saved note.
	SetNote(context.Context, string) error
}
