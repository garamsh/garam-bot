package workspaceaccess

import "context"

// Bots reads what the watched namespaces declare: whether each is claimed for this release, its Bots,
// and its WorkspaceClasses. Every Bot of a claimed namespace is served, garam-owned or not (ADR 0099
// decision 1).
type Bots interface {
	// Served answers whether namespace is claimed for this release.
	Served(ctx context.Context, namespace string) (bool, error)
	// Bot answers the Bot as declared, and false where its namespace declares none of that name.
	Bot(ctx context.Context, bot Bot) (Declared, bool, error)
	// Class answers the WorkspaceClass namespace/name, and false where the namespace declares none.
	Class(ctx context.Context, namespace, name string) (Class, bool, error)
}

// LiveBots reads a Bot from the API server as it is now, never from a cache, so the deletion sweep
// presumes no live Bot absent (ADR 0099 decision 6).
type LiveBots interface {
	// UID answers the Bot's UID, and false where the API server holds no Bot of that name.
	UID(ctx context.Context, bot Bot) (string, bool, error)
}
