package workspaceaccess

import (
	"net"
	"sync"
	"time"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// dial is one open dial, or one reserved while its connections are made (DIAL-3).
type dial struct {
	instance  instance
	bot       Bot
	botUID    string
	provider  string
	workspace workspace.Name

	// The fields below are held under dials.mu.
	// conns are the process's and the workspace's ends, nil while the dial is reserved.
	conns []net.Conn
	// last is when the dial last passed a byte.
	last   time.Time
	closed bool
}

// dials holds the open dials and bounds them (DIAL-3).
type dials struct {
	max int
	now func() time.Time

	mu   sync.Mutex
	open map[*dial]bool
}

func newDials(maxOpen int, now func() time.Time) *dials {
	return &dials{max: maxOpen, now: now, open: map[*dial]bool{}}
}

// reserve holds a slot for d under the bounds, or answers why none is free.
func (ds *dials) reserve(d *dial) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	held := 0
	for other := range ds.open {
		if other.instance == d.instance {
			held++
		}
	}
	switch {
	case held >= access.MaxDialsPerInstance:
		return limitedError{what: "the instance's dials", wait: time.Second}
	case len(ds.open) >= ds.max:
		return errServerDials
	}
	d.last = ds.now()
	ds.open[d] = true
	return nil
}

// attach gives a reserved dial its connections, and answers false where it was closed meanwhile, when
// it closes them.
func (ds *dials) attach(d *dial, conns ...net.Conn) bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if d.closed {
		closeAll(conns)
		return false
	}
	d.conns, d.last = conns, ds.now()
	return true
}

// passed records that d passed a byte.
func (ds *dials) passed(d *dial) {
	ds.mu.Lock()
	d.last = ds.now()
	ds.mu.Unlock()
}

// release closes d and frees its slot.
func (ds *dials) release(d *dial) {
	ds.closeWhere(func(other *dial) bool { return other == d })
}

// closeWhere closes and frees every dial match takes.
func (ds *dials) closeWhere(match func(*dial) bool) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	for d := range ds.open {
		if match(d) {
			d.closed = true
			closeAll(d.conns)
			delete(ds.open, d)
		}
	}
}

// closeIdle closes every dial that passed no byte for DialIdle (DIAL-3).
func (ds *dials) closeIdle() {
	now := ds.now()
	ds.closeWhere(func(d *dial) bool { return d.conns != nil && now.Sub(d.last) > access.DialIdle })
}

// snapshot answers the open dials, for their authority to be read again.
func (ds *dials) snapshot() []*dial {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	out := make([]*dial, 0, len(ds.open))
	for d := range ds.open {
		out = append(out, d)
	}
	return out
}

func closeAll(conns []net.Conn) {
	for _, conn := range conns {
		_ = conn.Close()
	}
}
