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
	if r.ProtoMajor != 1 {
		return fmt.Errorf("%w: a dial is HTTP/1.1 alone", errInvalid)
	}
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
	d := &dial{instance: c.instance, bot: c.bot, botUID: c.declared.UID, provider: t.provider, workspace: t.name}
	if err := s.dials.reserve(d); err != nil {
		return err
	}
	// The Bot's limit is taken before the first call that reaches the provider (LIMIT-1).
	if err := s.botLimits.take(c.declared.UID); err != nil {
		s.dials.release(d)
		return err
	}
	ws, err := t.place.Provider.Get(ctx, t.name)
	if err == nil && ws.Status.Phase != workspace.PhaseRunning {
		err = fmt.Errorf("%w: it is %s", workspace.ErrNotRunning, ws.Status.Phase)
	}
	if err != nil {
		s.dials.release(d)
		return providerError(err)
	}
	upstream, err := s.connect(ctx, t)
	if err != nil {
		s.dials.release(d)
		return err
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

// connect dials the workspace's adapter port. The connection outlives the request, so only the connect
// is bounded: where the provider has not connected within DialTimeout, its connection is closed when it
// comes, and the dial's slot is given back now.
func (s *service) connect(ctx context.Context, t target) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.DialTimeout)
	defer cancel()
	type answer struct {
		conn net.Conn
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		conn, err := t.place.Provider.Dial(ctx, t.name, agent.Port)
		done <- answer{conn, err}
	}()
	select {
	case a := <-done:
		if a.err != nil {
			return nil, providerError(a.err)
		}
		return a.conn, nil
	case <-ctx.Done():
		go func() {
			if a := <-done; a.conn != nil {
				_ = a.conn.Close()
			}
		}()
		return nil, fmt.Errorf("the %s provider did not connect within %s", t.provider, s.cfg.DialTimeout)
	}
}
