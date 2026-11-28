package bottoken

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// The RFC 8693 names garam's bot token exchange takes (garam's platform/api/machine.yaml
// §exchangeClientAssertion, at garam 371af83ebcc3a3af033929578e0b12abf54ed872).
const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeBot       = "urn:garam:params:oauth:token-type:bot"
	tokenTypeAccess    = "urn:ietf:params:oauth:token-type:access_token"
	tokenTypeDPoP      = "DPoP"
	headerProof        = "DPoP"
	headerNonce        = "DPoP-Nonce"
	headerRetryAfter   = "Retry-After"
	// maxAnswer bounds what is read of garam's answer.
	maxAnswer = 64 << 10
)

// Token is garam's bot token, bound to the key that signed the proof it was exchanged with.
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

// exchanger makes garam's bot token exchange under the cluster's token, and keeps the DPoP nonce garam
// last answered (NONCE-1).
type exchanger struct {
	http     *http.Client
	tokenURL string
	actor    func(ctx context.Context) (string, error)

	mu    sync.Mutex
	nonce string
}

// currentNonce is the nonce garam last answered, empty before its first answer.
func (e *exchanger) currentNonce() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nonce
}

// exchange asks garam for bot's token, bound to the key that signed proof, which is sent unchanged
// (PROOF-2, EXCH-1).
func (e *exchanger) exchange(ctx context.Context, bot GRN, proof string) (Token, error) {
	actor, err := e.actor(ctx)
	if err != nil {
		return Token{}, fmt.Errorf("%w: the cluster's token is not held: %w", errUnavailable, err)
	}
	form := url.Values{
		"grant_type":         {grantTokenExchange},
		"subject_token":      {string(bot)},
		"subject_token_type": {tokenTypeBot},
		"actor_token":        {actor},
		"actor_token_type":   {tokenTypeAccess},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("%w: %w", errUnavailable, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set(headerProof, proof)
	response, err := e.http.Do(request)
	if err != nil {
		// The error names the URL and the failure, never the form.
		return Token{}, fmt.Errorf("%w: %w", errUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if nonce := response.Header.Get(headerNonce); nonce != "" {
		e.mu.Lock()
		e.nonce = nonce
		e.mu.Unlock()
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	if err != nil {
		return Token{}, fmt.Errorf("%w: read garam's answer: %w", errUnavailable, err)
	}
	return answerOf(response, body)
}

// answerOf reads garam's answer to the exchange (BOT-2, TOK-1).
func answerOf(response *http.Response, body []byte) (Token, error) {
	switch status := response.StatusCode; {
	case status == http.StatusOK:
		var token Token
		if err := json.Unmarshal(body, &token); err != nil {
			return Token{}, fmt.Errorf("%w: garam's token is not JSON", errUnavailable)
		}
		if token.AccessToken == "" || token.TokenType != tokenTypeDPoP || token.ExpiresIn <= 0 {
			return Token{}, fmt.Errorf("%w: garam answered no DPoP token", errUnavailable)
		}
		return token, nil
	case status == http.StatusTooManyRequests:
		return Token{}, limitedError{retryAfter: response.Header.Get(headerRetryAfter)}
	case status >= http.StatusInternalServerError:
		return Token{}, fmt.Errorf("%w: garam answered %d", errUnavailable, status)
	}
	var refusal struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &refusal)
	switch refusal.Error {
	case codeUseNonce, codeInvalidProof:
		return Token{}, proofError{code: refusal.Error}
	}
	// The code names what garam refused and carries nothing of the request.
	return Token{}, fmt.Errorf("%w: garam answered %d %q", errRefused, response.StatusCode, refusal.Error)
}
