package bottoken

import "context"

// Applied is a Bot the connector applied from a declaration it holds: the declaration's identifier, and
// the UID of the object the connector made for it.
type Applied struct {
	DeclarationID string
	UID           string
}

// Bots answers the Bots the connector applied, from what it holds of its own writes. No object's marker
// is read for it, as any writer of the namespace may set one.
type Bots interface {
	// Applied answers the Bot the connector applied at bot, and false where it applied none there or no
	// longer holds the declaration.
	Applied(bot Bot) (Applied, bool)
}

// Pod is a Pod as the API server holds it.
type Pod struct {
	UID string
	// ServiceAccount is the ServiceAccount the Pod runs under.
	ServiceAccount string
	// BotUID is the UID of the Bot whose StatefulSet controls the Pod, and empty where no Bot's does.
	BotUID string
}

// Pods reads the Pods of the watched namespaces.
type Pods interface {
	// Pod answers the Pod namespace/name, and false where the namespace holds none.
	Pod(ctx context.Context, namespace, name string) (Pod, bool, error)
}
