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
