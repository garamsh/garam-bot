// Package v1alpha1 is version v1alpha1 of garam-bot's workspace access interface, interface 7: the
// protocol by which a bot's process acquires its workspace from its execution place's connector, writes
// the workspace's token files, dials its adapter and obtains the session's tokens. These are its names,
// bounds and answer types. README.md beside this file is the normative specification.
package v1alpha1

// SpecRelease is the release of v1alpha1's specification this package states (VER-1).
const SpecRelease = "0.2.0"

// Version is the version of the protocol these names speak.
const Version = "workspace-access/v1alpha1"

// PathPrefix is the prefix of every call (T-2).
const PathPrefix = "/" + Version + "/"

// TokenDir is the one directory of a workspace FILE-1 writes under: interface 2's token directory.
const TokenDir = "/run/garam-bot/"

// DialProtocol is the Upgrade token of a dial, in its request and its 101 (DIAL-1).
const DialProtocol = "workspace-access-dial/v1alpha1"

// The headers a call reads and answers.
const (
	// HeaderProof carries the client's DPoP proof (PROOF-1).
	HeaderProof = "DPoP"
	// HeaderRetryAfter carries how long to wait, in seconds (WS-3, REN-1).
	HeaderRetryAfter = "Retry-After"
)

// Code names a refusal (T-3).
type Code string

const (
	// CodeInvalid is a body, member, path or header the specification refuses, or content too large.
	CodeInvalid Code = "invalid"
	// CodeInvalidCredential is a place credential CRED-2 refuses.
	CodeInvalidCredential Code = "invalid_credential"
	// CodeInvalidDPoPProof is a proof PROOF-1 refuses.
	CodeInvalidDPoPProof Code = "invalid_dpop_proof"
	// CodeInvalidKey is a proof signed by another key than the one pinned to the instance (PIN-1).
	CodeInvalidKey Code = "invalid_key"
	// CodeUnknownBot is a credential naming no bot the place runs (BOT-1).
	CodeUnknownBot Code = "unknown_bot"
	// CodeNotFound is an attachment the bot does not declare, or a workspace not acquired yet.
	CodeNotFound Code = "not_found"
	// CodeNotRunning is a workspace that is not running (FILE-1, DIAL-1).
	CodeNotRunning Code = "not_running"
	// CodeDeleting is a workspace whose earlier incarnation is still being deleted (WS-1).
	CodeDeleting Code = "deleting"
	// CodeUnsupported is an attachment the server cannot serve as declared (WS-1).
	CodeUnsupported Code = "unsupported"
	// CodeWaitingForGateway is a place with no issuer of session tokens (TOK-1).
	CodeWaitingForGateway Code = "waiting_for_gateway"
	// CodeTemporarilyUnavailable is a 429 past a limit, or a 503 the server cannot answer now.
	CodeTemporarilyUnavailable Code = "temporarily_unavailable"
)
