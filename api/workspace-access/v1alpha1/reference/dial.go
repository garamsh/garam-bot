package reference

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"time"

	v1alpha1 "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// dial opens one connection to the workspace's interface 2 port, which in the reference echoes what
// it is sent (DIAL-1, DIAL-3). The dial is reserved under the lock its bounds are checked under, so
// concurrent dials cannot pass them, and an end, a removal or a stop that lands before the connection
// is taken over closes it (DIAL-4).
func (s *server) dial(w http.ResponseWriter, r *http.Request, c caller) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), v1alpha1.DialProtocol) ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, "a dial upgrades to "+v1alpha1.DialProtocol)
		return
	}
	if !noBody(w, r) {
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		refuse(w, http.StatusBadRequest, v1alpha1.CodeInvalid, "a dial is HTTP/1.1 alone")
		return
	}
	p := s.place
	key := spaceKey(c.bot.uid, c.attachment)
	p.mu.Lock()
	sp, acquired := p.spaces[key]
	switch {
	case !acquired:
		p.mu.Unlock()
		refuse(w, http.StatusNotFound, v1alpha1.CodeNotFound, "the workspace is not acquired")
		return
	case sp.phase != workspace.PhaseRunning || sp.deleting:
		p.mu.Unlock()
		refuse(w, http.StatusConflict, v1alpha1.CodeNotRunning, "the workspace is not running")
		return
	case p.openDials(c.instance) >= v1alpha1.MaxDialsPerInstance:
		p.mu.Unlock()
		retry(w, time.Second)
		refuse(w, http.StatusTooManyRequests, v1alpha1.CodeTemporarilyUnavailable, "the instance's dials")
		return
	case len(p.dials) >= p.maxDials:
		p.mu.Unlock()
		retry(w, time.Second)
		refuse(w, http.StatusServiceUnavailable, v1alpha1.CodeTemporarilyUnavailable, "the server's dials")
		return
	}
	d := &dial{instance: c.instance, botUID: c.bot.uid, space: key, last: p.clock()}
	p.dials[d] = true
	p.mu.Unlock()
	if !s.takeBot(w, c.bot) {
		s.forget(d)
		return
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		s.forget(d)
		return
	}
	p.mu.Lock()
	_, open := p.dials[d]
	if open {
		d.conn, d.last = conn, p.clock()
	}
	p.mu.Unlock()
	if !open {
		_ = conn.Close()
		return
	}
	_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " +
		v1alpha1.DialProtocol + "\r\n\r\n")
	if buffered.Flush() != nil {
		s.forget(d)
		return
	}
	go s.echo(d, buffered.Reader)
}

// echo sends back what the dial is sent, until either side closes.
func (s *server) echo(d *dial, in *bufio.Reader) {
	defer s.forget(d)
	buf := make([]byte, 32<<10)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			s.place.mu.Lock()
			d.last = s.place.clock()
			s.place.mu.Unlock()
			if _, werr := d.conn.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *server) forget(d *dial) {
	s.place.mu.Lock()
	delete(s.place.dials, d)
	conn := d.conn
	s.place.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// closeIdle closes, until ctx ends, every dial that passed no byte for DialIdle by the place's clock
// (DIAL-3).
func (s *server) closeIdle(ctx context.Context) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		p := s.place
		p.mu.Lock()
		now := p.clock()
		p.closeDials(func(d *dial) bool { return now.Sub(d.last) > v1alpha1.DialIdle })
		p.mu.Unlock()
	}
}
