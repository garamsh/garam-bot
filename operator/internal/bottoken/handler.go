package bottoken

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// errorBody is interface 6's refusal (T-3).
type errorBody struct {
	Error       string `json:"error"`
	Description string `json:"error_description,omitempty"`
	HTU         string `json:"htu,omitempty"`
}

// handler serves the one call. It is the one place a domain error becomes interface 6's status and
// code, and the one place a call is logged. Neither the credential, the proof nor the token is logged.
func (s *service) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path, func(w http.ResponseWriter, r *http.Request) {
		credential, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		bot, obtained, err := s.obtain(r.Context(), credential, r.Header.Values(headerProof))
		if err == nil {
			s.cfg.Log.Info("bot token answered", "namespace", bot.Namespace, "bot", bot.Name,
				"expiresIn", obtained.token.ExpiresIn)
			writeJSON(w, http.StatusOK, obtained.token)
			return
		}
		s.refuse(w, bot, obtained, err)
	})
	return s.recoverPanics(mux)
}

func (s *service) refuse(w http.ResponseWriter, bot Bot, obtained answer, err error) {
	log := s.cfg.Log.With("namespace", bot.Namespace, "bot", bot.Name, "error", err)
	var limited limitedError
	switch refusal, isRefusal := isProof(err); {
	case isRefusal:
		nonce := obtained.nonce
		if nonce == "" {
			nonce = s.exchanger.currentNonce()
		}
		if nonce != "" {
			w.Header().Set(headerNonce, nonce)
		}
		body := errorBody{Error: refusal.code}
		if refusal.code == "use_dpop_nonce" {
			body.HTU = s.cfg.TokenURL
		}
		writeJSON(w, http.StatusBadRequest, body)
	case errors.Is(err, errCredential):
		log.Info("bot token refused")
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "invalid_credential"})
	case errors.Is(err, errUnknownBot):
		log.Info("bot token refused")
		writeJSON(w, http.StatusForbidden, errorBody{Error: "unknown_bot"})
	case errors.Is(err, errRefused):
		log.Warn("bot token refused by garam")
		writeJSON(w, http.StatusForbidden, errorBody{Error: "refused"})
	case errors.As(err, &limited):
		log.Warn("bot token not exchanged")
		if limited.retryAfter != "" {
			w.Header().Set(headerRetryAfter, limited.retryAfter)
		}
		writeJSON(w, http.StatusTooManyRequests, errorBody{Error: "temporarily_unavailable"})
	default:
		log.Warn("bot token not exchanged")
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "temporarily_unavailable"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// recoverPanics answers 500 to a request whose handler panicked, rather than closing its connection.
func (s *service) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.cfg.Log.Error("bot token handler panicked", "panic", v)
				writeJSON(w, http.StatusInternalServerError, errorBody{Error: "server_error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
