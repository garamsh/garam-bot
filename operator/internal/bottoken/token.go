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
	subject, err := s.subject(ctx, held.bot)
	if err != nil {
		return held.bot, answer{}, err
	}
	if len(proofs) == 0 || !carriesNonce(proofs[0]) {
		if nonce := s.exchanger.currentNonce(); nonce != "" || len(proofs) == 0 {
			return held.bot, answer{nonce: nonce}, proofError{code: "use_dpop_nonce"}
		}
	}
	if len(proofs) != 1 {
		return held.bot, answer{}, proofError{code: "invalid_dpop_proof"}
	}
	token, err := s.exchanger.exchange(ctx, subject, proofs[0])
	if err != nil {
		return held.bot, answer{}, err
	}
	obtained = true
	return held.bot, answer{token: token}, nil
}

// subject is the GRN of bot where it is a garam-owned Bot this record applied in a watched namespace,
// and errUnknownBot otherwise (BOT-1).
func (s *service) subject(ctx context.Context, bot Bot) (GRN, error) {
	if !s.namespaces[bot.Namespace] {
		return "", fmt.Errorf("%w: %s is not watched", errUnknownBot, bot.Namespace)
	}
	management, found, err := s.cfg.Bots.Management(ctx, bot)
	switch {
	case err != nil:
		// A malformed marker is no garam-owned Bot's.
		return "", fmt.Errorf("%w: %s: %w", errUnknownBot, bot, err)
	case !found:
		return "", fmt.Errorf("%w: %s holds no Bot", errUnknownBot, bot)
	}
	// A cluster-owned Bot's marker names no record, so it is never this record's.
	declared, _ := management.Garam()
	if declared.Cluster != s.cfg.Record {
		return "", fmt.Errorf("%w: %s is not garam-owned under this record", errUnknownBot, bot)
	}
	return botGRN(s.cfg.Record, declared.ID)
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
