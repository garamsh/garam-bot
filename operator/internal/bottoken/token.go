package bottoken

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// answer is what one call obtained: a token, or the nonce to sign with.
type answer struct {
	token Token
	// nonce is garam's current nonce, answered with a use_dpop_nonce the server made itself.
	nonce string
}

// obtain is one call: the credential, the Bot it names, then the exchange with the proof (CRED-2,
// BOT-1, NONCE-1, EXCH-1). The credential is spent only where a token is answered (CRED-3).
func (s *service) obtain(ctx context.Context, credential string, proofs []string) (Bot, answer, error) {
	held, err := s.credentials.take(ctx, credential)
	if err != nil {
		return Bot{}, answer{}, err
	}
	obtained := false
	defer func() { s.credentials.release(held, obtained) }()
	subject, err := s.subject(ctx, held)
	if err != nil {
		return held.bot, answer{}, err
	}
	if len(proofs) == 0 || !carriesNonce(proofs[0]) {
		if nonce := s.exchanger.currentNonce(); nonce != "" || len(proofs) == 0 {
			return held.bot, answer{nonce: nonce}, proofError{code: codeUseNonce}
		}
	}
	if len(proofs) != 1 {
		return held.bot, answer{}, proofError{code: codeInvalidProof}
	}
	if err := s.limits.take(held.bot); err != nil {
		return held.bot, answer{}, err
	}
	token, err := s.exchanger.exchange(ctx, subject, proofs[0])
	if err != nil {
		return held.bot, answer{}, err
	}
	obtained = true
	return held.bot, answer{token: token}, nil
}

// subject is the GRN of the credential's Bot where the connector applied that Bot from a declaration it
// still holds, in a watched namespace, and the credential's Pod is that Bot's workload under the Bot's
// ServiceAccount (BOT-1, CRED-2). It answers errUnknownBot for a Bot the connector did not apply, and
// errCredential for a credential whose Pod is gone or is not the Bot's.
func (s *service) subject(ctx context.Context, held claim) (GRN, error) {
	bot := held.bot
	if !s.namespaces[bot.Namespace] {
		return "", fmt.Errorf("%w: %s is not watched", errUnknownBot, bot.Namespace)
	}
	applied, ok := s.cfg.Bots.Applied(bot)
	if !ok {
		return "", fmt.Errorf("%w: the connector applied no Bot %s", errUnknownBot, bot)
	}
	pod, found, err := s.cfg.Pods.Pod(ctx, bot.Namespace, held.pod.Name)
	switch {
	case err != nil:
		return "", fmt.Errorf("%w: %w", errUnavailable, err)
	case !found || pod.UID != held.pod.UID:
		return "", fmt.Errorf("%w: its Pod %s/%s is gone", errCredential, bot.Namespace, held.pod.Name)
	case pod.ServiceAccount != bot.Name || pod.BotUID != applied.UID:
		return "", fmt.Errorf("%w: its Pod %s/%s is not the Bot's workload", errCredential, bot.Namespace,
			held.pod.Name)
	}
	return botGRN(s.cfg.Record, applied.DeclarationID)
}

// carriesNonce reports whether the proof's claims name a nonce. The proof is not verified here: garam
// verifies it, and a proof that cannot be read is sent to garam to refuse.
func carriesNonce(proof string) bool {
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		return true
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return true
	}
	var claims struct {
		Nonce *string `json:"nonce"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return true
	}
	return claims.Nonce != nil && *claims.Nonce != ""
}

// isProof is a refusal of the proof, which is relayed with the nonce and htu (NONCE-1, BOT-2).
func isProof(err error) (proofError, bool) {
	var refusal proofError
	ok := errors.As(err, &refusal)
	return refusal, ok
}
