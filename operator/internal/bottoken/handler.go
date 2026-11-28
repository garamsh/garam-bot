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
		if s.cfg.TokenURL == nil {
			s.cfg.Log.Info("bot token refused: garam is off")
			writeJSON(w, http.StatusForbidden, errorBody{Error: "unknown_bot"})
			return
		}
		// A header without the Bearer scheme carries no credential.
		credential, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !bearer {
			credential = ""
		}
		bot, obtained, err := s.obtain(r.Context(), credential, r.Header.Values(headerProof))
		if err == nil {
			s.cfg.Log.Info("bot token answered", "namespace", bot.Namespace, "bot", bot.Name,
				"expiresIn", obtained.token.ExpiresIn)
			writeJSON(w, http.StatusOK, obtained.token)
			return
		}
		s.refuse(w, bot, obtained, err)
	})
	if s.cfg.Beside == nil {
		return s.recoverPanics(mux)
	}
	// The other interface answers its own refusals, a panic's among them, in its own codes.
	outer := http.NewServeMux()
	outer.Handle(Path, s.recoverPanics(mux))
	outer.Handle("/", s.cfg.Beside)
	return outer
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
		if refusal.code == codeUseNonce {
			body.HTU = s.cfg.TokenURL()
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
		writeJSON(w, http.StatusTooManyRequests, errorBody{Error: codeUnavailable})
	default:
		log.Warn("bot token not exchanged")
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: codeUnavailable})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// recoverPanics answers 503 temporarily_unavailable, one of interface 6's codes (T-3), to a request
// whose handler panicked, rather than closing its connection.
func (s *service) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.cfg.Log.Error("bot token handler panicked", "panic", v)
				writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: codeUnavailable})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
