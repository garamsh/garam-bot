package v1alpha1

// SpecRelease is the release of v1alpha1's specification this package states (VER-1).
const SpecRelease = "0.1.0"

// Version is the version of the protocol these names speak.
const Version = "bot-token/v1alpha1"

// TokenPath is the path of the one call, POST with no body (T-2).
const TokenPath = "/" + Version + "/token"

// The headers the call reads and answers.
const (
	// HeaderProof carries the client's DPoP proof (PROOF-1).
	HeaderProof = "DPoP"
	// HeaderNonce carries garam's current DPoP nonce (NONCE-1).
	HeaderNonce = "DPoP-Nonce"
	// HeaderRetryAfter carries how long to wait, in seconds, with a 429 or a 503 (REN-3).
	HeaderRetryAfter = "Retry-After"
)

// TokenTypeDPoP is the token_type of every token answered: it is bound to the proof's key.
const TokenTypeDPoP = "DPoP"

// Token is the body of a 200: garam's token, bound to the key that signed the proof (TOK-1).
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	// ExpiresIn is the seconds garam accepts the token for.
	ExpiresIn int64 `json:"expires_in"`
}

// Code names a refusal (T-3).
type Code string

const (
	// CodeUseDPoPNonce asks for a proof signed with the nonce answered, for the URL in HTU (NONCE-1).
	CodeUseDPoPNonce Code = "use_dpop_nonce"
	// CodeInvalidDPoPProof is a proof garam refused.
	CodeInvalidDPoPProof Code = "invalid_dpop_proof"
	// CodeInvalidCredential is a place credential that is missing, does not verify, has expired, names
	// another audience, or was spent (CRED-2, CRED-3).
	CodeInvalidCredential Code = "invalid_credential"
	// CodeUnknownBot is a credential naming no bot the place runs under a garam identity (BOT-1).
	CodeUnknownBot Code = "unknown_bot"
	// CodeRefused is an exchange garam refused for the bot (BOT-2).
	CodeRefused Code = "refused"
	// CodeTemporarilyUnavailable is a 429 or a 503 (REN-3).
	CodeTemporarilyUnavailable Code = "temporarily_unavailable"
)

// ErrorBody is every refusal's body (T-3).
type ErrorBody struct {
	Error       Code   `json:"error"`
	Description string `json:"error_description,omitempty"`
	// HTU is garam's token endpoint URL, which a proof names as its htu. It is answered with
	// CodeUseDPoPNonce (NONCE-1).
	HTU string `json:"htu,omitempty"`
}
