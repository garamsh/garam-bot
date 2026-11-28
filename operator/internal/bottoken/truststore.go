package bottoken

import "context"

// TrustStore publishes the certificates a bot's process trusts the server by (interface 6's T-1).
type TrustStore interface {
	// Publish replaces the published bundle with bundle, PEM.
	Publish(ctx context.Context, bundle []byte) error
}
