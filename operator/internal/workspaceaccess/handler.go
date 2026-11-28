package workspaceaccess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// caller is who a request was authenticated as (CRED-2, BOT-1, PIN-1).
type caller struct {
	bot      Bot
	declared Declared
	instance instance
	// attachment is the request's attachment, which the Bot declares.
	attachment string
}

// call is one of interface 7's calls on an authenticated caller.
type call func(w http.ResponseWriter, r *http.Request, c caller) error

func (s *service) Handler() http.Handler {
	mux := http.NewServeMux()
	ws := access.WorkspacesPath + "{attachment}"
	mux.Handle("PUT "+ws, s.serve("acquire", s.acquire))
	mux.Handle("GET "+ws, s.serve("read", s.read))
	mux.Handle("PUT "+ws+access.FilesSuffix, s.serve("files", s.files))
	mux.Handle("GET "+ws+access.DialSuffix, s.serve("dial", s.dial))
	mux.Handle("POST "+ws+access.TokensSuffix, s.serve("session tokens", s.tokens))
	mux.HandleFunc(access.PathPrefix, func(w http.ResponseWriter, _ *http.Request) {
		s.refuse(w, fmt.Errorf("%w: no such call", errNotFound))
	})
	return s.recovering(mux)
}

// serve authenticates a request and runs the call, logging the call with the Bot and its outcome alone:
// never the credential, the proof, a token, a file's content or a dial's bytes (SEC-1).
func (s *service) serve(name string, run call) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.authenticate(r)
		if err == nil {
			err = run(w, r, c)
		}
		log := s.cfg.Log.With("call", name, "namespace", c.bot.Namespace, "bot", c.bot.Name,
			"attachment", r.PathValue("attachment"))
		if err == nil {
			log.Info("workspace call answered")
			return
		}
		log.Info("workspace call refused", "outcome", s.refuse(w, err), "error", err)
	})
}

// authenticate resolves the request's caller: the credential, the instance's limit right after it, the
// Bot it names, the instance it is bound to, the proof and the pin, then the attachment (CRED-2,
// LIMIT-2, BOT-1, PROOF-1, PIN-1).
func (s *service) authenticate(r *http.Request) (caller, error) {
	ctx := r.Context()
	credential, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !bearer {
		credential = ""
	}
	held, err := s.credentials.verify(ctx, credential)
	if err != nil {
		return caller{}, err
	}
	c := caller{bot: held.bot}
	if err := s.instLimits.take(held.pod.UID); err != nil {
		return c, err
	}
	declared, err := s.declared(ctx, held.bot)
	if err != nil {
		return c, err
	}
	c.declared = declared
	pod, err := s.instanceOf(ctx, held, declared, false)
	if err != nil {
		return c, err
	}
	thumbprint, err := s.proofs.verify(r, credential)
	if err != nil {
		return c, err
	}
	c.instance = instance{namespace: held.bot.Namespace, name: held.pod.Name, pod: pod.UID, container: pod.Container}
	if !s.pins.take(c.instance, thumbprint) {
		// A restart the binding cache has not seen yet is a new instance, so read the Pod again first.
		again, err := s.instanceOf(ctx, held, declared, true)
		if err != nil {
			return c, err
		}
		c.instance.container = again.Container
		if again.Container == pod.Container || !s.pins.take(c.instance, thumbprint) {
			return c, errKey
		}
	}
	c.attachment = r.PathValue("attachment")
	if _, ok := declared.Attachments[c.attachment]; !ok {
		return c, fmt.Errorf("%w: the bot declares no attachment %q", errNotFound, c.attachment)
	}
	return c, nil
}

// declared answers the Bot as its namespace declares it, where the namespace is one this release serves.
func (s *service) declared(ctx context.Context, bot Bot) (Declared, error) {
	if !s.namespaces[bot.Namespace] {
		return Declared{}, fmt.Errorf("%w: the namespace %s is not watched", errUnknownBot, bot.Namespace)
	}
	served, err := s.cfg.Bots.Served(ctx, bot.Namespace)
	if err != nil {
		return Declared{}, err
	}
	if !served {
		return Declared{}, fmt.Errorf("%w: the namespace %s is not claimed for the release", errUnknownBot, bot.Namespace)
	}
	declared, ok, err := s.cfg.Bots.Bot(ctx, bot)
	switch {
	case err != nil:
		return Declared{}, err
	case !ok:
		return Declared{}, fmt.Errorf("%w: no Bot %s", errUnknownBot, bot)
	}
	return declared, nil
}

// instanceOf answers the running Pod the credential is bound to, which the Bot's StatefulSet controls,
// read again from the API server where again is set (CRED-2, BOT-1).
func (s *service) instanceOf(ctx context.Context, held bound, declared Declared, again bool) (Pod, error) {
	pod, exists, err := s.bindings.pod(ctx, held.bot.Namespace, held.pod.Name, again)
	switch {
	case err != nil:
		return Pod{}, err
	case !exists || pod.UID != held.pod.UID || pod.ServiceAccount != held.bot.Name:
		return Pod{}, fmt.Errorf("%w: its Pod does not exist", errCredential)
	case pod.BotUID == "" || pod.BotUID != declared.UID:
		// A workload no Bot controls, or one of the Bot's earlier incarnation, is no Bot's (BOT-1).
		return Pod{}, fmt.Errorf("%w: its Pod runs no Bot %s", errUnknownBot, held.bot)
	case pod.Container == "":
		return Pod{}, fmt.Errorf("%w: its Pod's runtime container does not run", errCredential)
	}
	return pod, nil
}

// refuse answers err as interface 7's status and code (T-3), and answers the status. It is the one
// place a domain error is translated.
func (s *service) refuse(w http.ResponseWriter, err error) int {
	status, code := http.StatusServiceUnavailable, access.CodeTemporarilyUnavailable
	for _, c := range codes {
		if errors.Is(err, c.err) {
			status, code = c.status, c.code
			break
		}
	}
	if limited, ok := errors.AsType[limitedError](err); ok {
		status, code = http.StatusTooManyRequests, access.CodeTemporarilyUnavailable
		retry(w, limited.wait)
	}
	switch {
	case errors.Is(err, errDeleting), errors.Is(err, errServerDials):
		retry(w, 0)
	case status == http.StatusUnauthorized:
		w.Header().Set(access.HeaderAuthenticate, fmt.Sprintf(`DPoP error=%q, algs="ES256"`, code))
	}
	body := access.ErrorBody{Error: code, Description: describe(err, status)}
	if failed, ok := errors.AsType[fileError](err); ok {
		body.Path = failed.path
	}
	writeJSON(w, status, body)
	return status
}

// describe is a refusal's description: the domain's words for what the caller may act on, and none of
// a provider's or the server's own failure, which may name what is not the caller's.
func describe(err error, status int) string {
	if status >= 500 && !errors.Is(err, errWaitingForGateway) {
		return "the server cannot answer now"
	}
	return err.Error()
}

// retry sets Retry-After to wait in whole seconds, rounded up, and one at least.
func retry(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set(access.HeaderRetryAfter, strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// noBody refuses a body that is not empty or {} (T-2).
func noBody(r *http.Request) error {
	read, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err == nil && len(bytes.TrimSpace(read)) > 0 {
		err = decodeWhole(bytes.NewReader(read), &struct{}{})
	}
	if err != nil {
		return fmt.Errorf("%w: the call takes no body", errInvalid)
	}
	return nil
}

// decodeWhole decodes one JSON value into into, refusing a member it does not name and anything after
// the value (T-2).
func decodeWhole(r io.Reader, into any) error {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("data after the body")
	}
	return nil
}

// recovering answers a handler's panic with 503 temporarily_unavailable where nothing is answered yet
// (T-3). net/http would close the connection instead.
func (s *service) recovering(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &answering{ResponseWriter: w}
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler || rw.answered || rw.hijacked { //nolint:errorlint // compared as net/http does
				panic(recovered)
			}
			s.cfg.Log.Error("workspace call panicked", "panic", recovered)
			writeJSON(rw, http.StatusServiceUnavailable, access.ErrorBody{Error: access.CodeTemporarilyUnavailable})
		}()
		next.ServeHTTP(rw, r)
	})
}

// answering records whether a response has begun, or its connection was taken over.
type answering struct {
	http.ResponseWriter
	answered, hijacked bool
}

func (a *answering) WriteHeader(status int) {
	a.answered = true
	a.ResponseWriter.WriteHeader(status)
}

func (a *answering) Write(b []byte) (int, error) {
	a.answered = true
	return a.ResponseWriter.Write(b)
}

func (a *answering) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := a.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("the connection cannot be taken over")
	}
	a.hijacked = true
	return hijacker.Hijack()
}
