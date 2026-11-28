// Package v1alpha1 is version v1alpha1 of garam-bot's model gateway interface, interface 5: the protocol
// by which a workspace's harness calls a model through its place's gateway with a short-lived token, and
// by which the place's issuer obtains that token for a bot. These are its names, bounds and bodies.
// README.md beside this file is the normative specification.
package v1alpha1

// SpecRelease is the release of v1alpha1's specification this package states (VER-1).
const SpecRelease = "0.1.0"

// Version is the version of the protocol these names speak.
const Version = "model-gateway/v1alpha1"

// PathPrefix is the prefix of the issue surface's calls (T-2).
const PathPrefix = "/" + Version + "/"

// TokensPath is the issue surface's one call (ISS-2).
const TokensPath = PathPrefix + "model-tokens"

// Audience is the audience of an issuer's credential at a cluster (ISS-1).
const Audience = "garam-model-gateway"

// The headers the surfaces read and answer.
const (
	// HeaderAuthorization carries the issuer's credential (ISS-1) or the model token (MOD-1).
	HeaderAuthorization = "Authorization"
	// HeaderRetryAfter carries how long to wait, in seconds (LIMIT-1, LIMIT-2).
	HeaderRetryAfter = "Retry-After"
)

// Code names a refusal of the issue surface (T-3).
type Code string

const (
	// CodeInvalid is a body or member the specification refuses.
	CodeInvalid Code = "invalid"
	// CodeInvalidCredential is an issuer's credential ISS-1 refuses.
	CodeInvalidCredential Code = "invalid_credential"
	// CodeForbidden is a credential naming no issuer the gateway serves (ISS-1).
	CodeForbidden Code = "forbidden"
	// CodeNotFound is a path below PathPrefix that is no call of the issue surface (ISS-3).
	CodeNotFound Code = "not_found"
	// CodeTemporarilyUnavailable is a 429 past a limit, or a 503 the gateway cannot answer now.
	CodeTemporarilyUnavailable Code = "temporarily_unavailable"
)
