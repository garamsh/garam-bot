package bot

import (
	"context"
	"fmt"
	"time"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// Message is one message the bot is given: its identifier, stable across the source's deliveries of it,
// which is its task's identifier (interface 2's TASK-1), so a message delivered twice runs once; and its
// input.
type Message struct {
	ID    string
	Input []agent.Input
}

// MessageSource is where the bot's messages come from: the inbox directory until garam's mailbox
// serves the bot (ADR 0043 decision 7). The driver asks for one message at a time: it calls Next again
// only after Done of the message Next last answered, and never two calls at once.
type MessageSource interface {
	// Next waits for the next message, until ctx ends. A source that asks to be polled no sooner than a
	// time answers a *RetryLater.
	Next(ctx context.Context) (Message, error)
	// Done acknowledges m, its task ended with outcome, or OutcomeRejected where no task could be made
	// of it.
	Done(ctx context.Context, m Message, outcome agent.Outcome) error
}

// OutcomeRejected is the end of a message no task could be made of: the adapter refused its task, or
// the message is no task's. It is the bot's own, and no adapter answers it.
const OutcomeRejected agent.Outcome = "rejected"

// RetryLater is a source's answer that it is to be polled again no sooner than After, as a 429 with
// Retry-After asks.
type RetryLater struct {
	After time.Duration
}

func (r *RetryLater) Error() string {
	return fmt.Sprintf("the message source asks to be polled again in %s", r.After)
}

// noMessages is the source of a bot given none: Next waits until ctx ends.
type noMessages struct{}

func (noMessages) Next(ctx context.Context) (Message, error) {
	<-ctx.Done()
	return Message{}, ctx.Err()
}

func (noMessages) Done(context.Context, Message, agent.Outcome) error { return nil }
