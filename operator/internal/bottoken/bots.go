package bottoken

import (
	"context"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
)

// Bots reads the Bots of the watched namespaces.
type Bots interface {
	// Management answers the ownership marker of the Bot, and false where the namespace holds no Bot of
	// that name.
	Management(ctx context.Context, bot Bot) (botv1.Management, bool, error)
}
