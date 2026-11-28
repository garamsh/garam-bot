package bottoken

import (
	"context"
	"encoding/json"
	"errors"
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
	http       *http.Client
	tokenURL   func() string
	actor      func(ctx context.Context) (string, error)
	invalidate func(token string)

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
// (PROOF-2, EXCH-1). Where garam answers 401, the cluster's token is dropped and the exchange made
// once more under a fresh one, with the same proof: garam spends a proof only once the actor token
// authenticated. A second 401 is garam's to cure, and the bot is answered unavailable.
func (e *exchanger) exchange(ctx context.Context, bot GRN, proof string) (Token, error) {
	for attempt := 1; ; attempt++ {
		actor, err := e.actor(ctx)
		if err != nil {
			return Token{}, fmt.Errorf("%w: the cluster's token is not held: %w", errUnavailable, err)
		}
		token, err := e.send(ctx, bot, actor, proof)
		if !errors.Is(err, errActorRefused) {
			return token, err
		}
		e.invalidate(actor)
		if attempt == 2 {
			return Token{}, fmt.Errorf("%w: %w twice", errUnavailable, err)
		}
	}
}

// errActorRefused is garam's 401: the cluster's token, the actor_token, did not authenticate.
var errActorRefused = errors.New("garam refused the cluster's token")

// send is one exchange under actor.
func (e *exchanger) send(ctx context.Context, bot GRN, actor, proof string) (Token, error) {
	form := url.Values{
		"grant_type":         {grantTokenExchange},
		"subject_token":      {string(bot)},
		"subject_token_type": {tokenTypeBot},
		"actor_token":        {actor},
		"actor_token_type":   {tokenTypeAccess},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenURL(), strings.NewReader(form.Encode()))
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
	status := response.StatusCode
	switch status {
	case http.StatusOK:
		var token Token
		if err := json.Unmarshal(body, &token); err != nil {
			return Token{}, fmt.Errorf("%w: garam's token is not JSON", errUnavailable)
		}
		if token.AccessToken == "" || token.TokenType != tokenTypeDPoP || token.ExpiresIn <= 0 {
			return Token{}, fmt.Errorf("%w: garam answered no DPoP token", errUnavailable)
		}
		return token, nil
	case http.StatusTooManyRequests:
		return Token{}, limitedError{retryAfter: response.Header.Get(headerRetryAfter)}
	case http.StatusUnauthorized:
		return Token{}, errActorRefused
	}
	var refusal struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &refusal)
	switch {
	case refusal.Error == codeUseNonce || refusal.Error == codeInvalidProof:
		return Token{}, proofError{code: refusal.Error}
	case status == http.StatusBadRequest && (refusal.Error == "invalid_request" || refusal.Error == "invalid_grant"):
		// garam's refusal of the bot: no bot of that GRN, or none placed at this record. The code names
		// what garam refused and carries nothing of the request.
		return Token{}, fmt.Errorf("%w: garam answered %d %q", errRefused, status, refusal.Error)
	}
	// Anything else is garam's or the connector's to cure, not the bot's.
	return Token{}, fmt.Errorf("%w: garam answered %d %q", errUnavailable, status, refusal.Error)
}
