package naruworkspace

import (
	"context"
	"crypto/ecdsa"
	"errors"
)

// ErrNoCredential is what a CredentialStore answers while it holds no credential.
var ErrNoCredential = errors.New("no caller credential is stored")

// Credential is the provider's Machine API caller pair: the key it made where it runs and the
// certificate naru's signer issued for it. It never leaves the place.
type Credential struct {
	Key *ecdsa.PrivateKey
	// Certificate is the caller certificate, DER.
	Certificate []byte
	// Attempted is the SHA-256, in hex, of the registration token last presented while no
	// Certificate is held, so a token is never presented twice: presenting one spends it, whatever
	// the answer (naru ADR 0116 item 3).
	Attempted string
}

// CredentialStore keeps the caller's Credential at the place.
type CredentialStore interface {
	// Load answers the stored Credential, or ErrNoCredential.
	Load(ctx context.Context) (Credential, error)
	// Save replaces the stored Credential.
	Save(ctx context.Context, c Credential) error
}
