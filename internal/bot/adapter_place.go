package bot

import (
	"context"
	"net"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// AdapterPlace is what the driver reaches its adapter through. Workspace is one.
type AdapterPlace interface {
	// Dial opens one connection to the adapter's port (interface 7's DIAL-1).
	Dial(ctx context.Context) (net.Conn, error)
	// AdapterTokens answers the adapter's token last written into the workspace, and the one being
	// written where a write is under way or its answer was lost, either empty where there is none. The
	// adapter holds one of them (interface 2's AUTH-1).
	AdapterTokens() (written, writing string)
	// Session answers the session's model gateway and tools, and whether the session's tokens are
	// written, so a session can be opened on them.
	Session() (access.Workspace, bool)
	// Rewrite asks for the workspace's tokens to be written again at once: the adapter refused both
	// tokens, as a restarted one whose token directory is empty does.
	Rewrite()
}
