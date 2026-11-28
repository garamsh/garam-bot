package workspaceaccess

import (
	"fmt"
	"net/http"
	"time"
)

// maxTokenLife bounds a session token's life (TOK-1).
const maxTokenLife = time.Hour

// tokens answers the session's model and tool tokens from the issuer, keeping none past the answer
// (TOK-1).
func (s *service) tokens(w http.ResponseWriter, r *http.Request, c caller) error {
	if err := noBody(r); err != nil {
		return err
	}
	ctx := r.Context()
	t, err := s.targetOf(ctx, c)
	if err != nil {
		return err
	}
	if _, err := t.place.Provider.Get(ctx, t.name); err != nil {
		return providerError(err)
	}
	if s.cfg.Issuer == nil || !s.cfg.Issuer.Available(ctx) {
		return errWaitingForGateway
	}
	if err := s.botLimits.take(c.declared.UID); err != nil {
		return err
	}
	answer, err := s.cfg.Issuer.Tokens(ctx, c.bot, c.declared.Tools)
	if err != nil {
		return fmt.Errorf("obtain the session's tokens: %w", err)
	}
	life := int64(maxTokenLife / time.Second)
	if answer.Model.ExpiresIn > life || len(answer.Tools) != len(c.declared.Tools) {
		return fmt.Errorf("the issuer answered a token past an hour, or not one per tool")
	}
	for _, tool := range c.declared.Tools {
		if token, ok := answer.Tools[tool]; !ok || token.ExpiresIn > life {
			return fmt.Errorf("the issuer answered no token for a tool, or one past an hour")
		}
	}
	writeJSON(w, http.StatusOK, answer)
	return nil
}
