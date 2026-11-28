package v1alpha1

import (
	"fmt"
	"time"
)

// The bounds the specification sets.
const (
	// MaxTokenLife bounds a model token's life (TOKEN-1), and an issuer's credential's (ISS-1).
	MaxTokenLife = time.Hour
	// MaxRequestBody bounds a model call's body (MOD-4).
	MaxRequestBody = 32 << 20
	// MaxIdentity bounds each member of a TokenRequest (ISS-2).
	MaxIdentity = 253
	// UpstreamHeaderTimeout bounds how long the upstream may take to begin its answer (MOD-5).
	UpstreamHeaderTimeout = 10 * time.Minute
)

// The model surface's paths of the Anthropic Messages API (MOD-2, MOD-3).
const (
	MessagesPath    = "/v1/messages"
	CountTokensPath = "/v1/messages/count_tokens"
)

// ModelPaths are the model surface's paths, each forwarded; any other is refused (MOD-3).
var ModelPaths = []string{MessagesPath, CountTokensPath}

// ForwardedHeaders are the only headers of a model call the gateway forwards (MOD-2).
var ForwardedHeaders = []string{"Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta"}

// HeaderProviderKey carries the provider's key to Anthropic's API (MOD-2).
const HeaderProviderKey = "X-Api-Key"

// The provider API's error types the gateway answers (MOD-1, MOD-3, MOD-4, MOD-5, LIMIT-1).
const (
	ErrorAuthentication  = "authentication_error"
	ErrorNotFound        = "not_found_error"
	ErrorRequestTooLarge = "request_too_large"
	ErrorAPI             = "api_error"
	ErrorRateLimit       = "rate_limit_error"
)

// TokenRequest is the body of a model tokens call: the bot as the issuer resolved it (ISS-2).
type TokenRequest struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// Check reports whether r names a bot, each member given and within MaxIdentity (ISS-2).
func (r TokenRequest) Check() error {
	for member, value := range map[string]string{"namespace": r.Namespace, "name": r.Name, "uid": r.UID} {
		if value == "" || len(value) > MaxIdentity {
			return fmt.Errorf("%s is required, and at most %d bytes", member, MaxIdentity)
		}
	}
	return nil
}

// ModelToken is the answer of a model tokens call (ISS-2, TOKEN-1).
type ModelToken struct {
	Token string `json:"token"`
	// ExpiresIn is the seconds the token lives, at most MaxTokenLife's.
	ExpiresIn int64 `json:"expiresIn"`
}

// ErrorBody is every refusal of the issue surface (T-3).
type ErrorBody struct {
	Error       Code   `json:"error"`
	Description string `json:"error_description,omitempty"`
}

// ProviderError is the Anthropic Messages API's error form, the model surface's refusals' (MOD-1).
type ProviderError struct {
	Type  string         `json:"type"`
	Error ProviderDetail `json:"error"`
}

// ProviderDetail is a ProviderError's detail.
type ProviderDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// NewProviderError is the model surface's refusal of kind, with message.
func NewProviderError(kind, message string) ProviderError {
	return ProviderError{Type: "error", Error: ProviderDetail{Type: kind, Message: message}}
}
