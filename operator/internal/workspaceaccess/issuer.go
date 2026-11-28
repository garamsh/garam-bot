package workspaceaccess

import (
	"context"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// SessionIssuer obtains a session's model and tool tokens for a Bot from the place's model gateway and tool
// proxy (TOK-1). A released connector is given none (ADR 0099 decision 9).
type SessionIssuer interface {
	// Available answers whether the issuer has a gateway to obtain tokens from now. A call to one
	// without is answered waiting_for_gateway, and takes nothing from the Bot's limit.
	Available(ctx context.Context) bool
	// Tokens answers a token for the model and one for each of tools, each living an hour at most.
	Tokens(ctx context.Context, bot Bot, tools []string) (access.SessionTokens, error)
}
