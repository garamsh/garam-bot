package workspaceaccess

import "context"

// Pods reads the Pods of the watched namespaces from the API server, as they are now and not as a
// cache last saw them.
type Pods interface {
	// Pod answers the Pod namespace/name, and false where the namespace holds none.
	Pod(ctx context.Context, namespace, name string) (Pod, bool, error)
}
