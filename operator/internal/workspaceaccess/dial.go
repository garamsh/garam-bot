package workspaceaccess

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// dial opens one connection to interface 2's port in the workspace and splices it to the request's
// connection (DIAL-1). The dial is reserved under its bounds before anything is opened, so concurrent
// dials cannot pass them, and Run closes it once its authority ends (DIAL-3, DIAL-4).
func (s *service) dial(w http.ResponseWriter, r *http.Request, c caller) error {
	if !strings.EqualFold(r.Header.Get("Upgrade"), access.DialProtocol) ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return fmt.Errorf("%w: a dial upgrades to %s", errInvalid, access.DialProtocol)
	}
	if err := noBody(r); err != nil {
		return err
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return fmt.Errorf("%w: a dial is HTTP/1.1 alone", errInvalid)
	}
	ctx := r.Context()
	t, err := s.targetOf(ctx, c)
	if err != nil {
		return err
	}
	ws, err := t.place.Provider.Get(ctx, t.name)
	if err != nil {
		return providerError(err)
	}
	if ws.Status.Phase != workspace.PhaseRunning {
		return fmt.Errorf("%w: it is %s", errNotRunning, ws.Status.Phase)
	}
	d := &dial{instance: c.instance, bot: c.bot, botUID: c.declared.UID, provider: t.provider, workspace: t.name}
	if err := s.dials.reserve(d); err != nil {
		return err
	}
	if err := s.botLimits.take(c.declared.UID); err != nil {
		s.dials.release(d)
		return err
	}
	// The connection outlives the request, so it is bounded by the dial alone.
	upstream, err := t.place.Provider.Dial(context.WithoutCancel(ctx), t.name, agent.Port)
	if err != nil {
		s.dials.release(d)
		return providerError(err)
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		s.dials.release(d)
		return fmt.Errorf("take the connection over: %w", err)
	}
	if !s.dials.attach(d, conn, upstream) {
		return nil
	}
	_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " +
		access.DialProtocol + "\r\n\r\n")
	if err := buffered.Flush(); err != nil {
		s.dials.release(d)
		return nil
	}
	go s.splice(d, buffered.Reader, conn, upstream)
	return nil
}

// splice passes bytes both ways unchanged, reading none and logging none, until either side closes
// (DIAL-1, SEC-1).
func (s *service) splice(d *dial, in *bufio.Reader, conn, upstream net.Conn) {
	defer s.dials.release(d)
	done := make(chan struct{}, 2)
	pass := func(to io.Writer, from io.Reader) {
		_, _ = io.Copy(&passing{to: to, mark: func() { s.dials.passed(d) }}, from)
		done <- struct{}{}
	}
	go pass(upstream, in)
	go pass(conn, upstream)
	<-done
}

// passing marks the dial each time bytes pass through it, for its idle time.
type passing struct {
	to   io.Writer
	mark func()
}

func (p *passing) Write(b []byte) (int, error) {
	p.mark()
	return p.to.Write(b)
}
