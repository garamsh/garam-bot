package bot

// GaramIdentity is the bot's garam token and the key it is bound to, which a call to garam presents
// (ADR 0042). TokenSource is one.
type GaramIdentity interface {
	// Token answers the token held, empty where none lives.
	Token() string
	// Refused is told that garam answered 401 to token.
	Refused(token string)
	// Proof signs a DPoP proof of a request of method to url, bound to token (RFC 9449 §4.2).
	Proof(method, url, token string) (string, error)
}
