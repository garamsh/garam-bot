package modelgateway

import (
	"context"
	"crypto/ecdsa"

	"github.com/go-jose/go-jose/v4"
)

// Keys reads the keys the API server signs ServiceAccount tokens with, and the issuer it names them by.
type Keys interface {
	Keys(ctx context.Context) (Issuer, error)
}

// Issuer is the API server's ServiceAccount token issuer and its public keys.
type Issuer struct {
	// URL is the iss every ServiceAccount token the API server signs carries.
	URL  string
	Keys jose.JSONWebKeySet
}

// SigningKeys holds the key the gateway signs model tokens with. Every replica, and the gateway after a
// restart, loads the same key, so a token it issued is taken by each of them (TOKEN-2).
type SigningKeys interface {
	Load(ctx context.Context) (*ecdsa.PrivateKey, error)
}
