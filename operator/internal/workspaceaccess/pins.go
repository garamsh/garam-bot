package workspaceaccess

import "sync"

// pins holds the key pinned to each workload instance, in memory alone (PIN-1, ADR 0099 decision 2).
type pins struct {
	mu   sync.Mutex
	held map[instance]string
}

// instance is a workload instance: a Pod by its namespace, name and UID, with its runtime container's
// ID, so a restarted container is a new instance.
type instance struct {
	namespace, name, pod, container string
}

func newPins() *pins { return &pins{held: map[instance]string{}} }

// take pins thumbprint to i where none is pinned, and answers whether the instance's pin is it.
func (p *pins) take(i instance, thumbprint string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pinned, ok := p.held[i]
	if !ok {
		p.held[i] = thumbprint
		return true
	}
	return pinned == thumbprint
}

// instances answers every instance a key is pinned to.
func (p *pins) instances() []instance {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]instance, 0, len(p.held))
	for i := range p.held {
		out = append(out, i)
	}
	return out
}

// drop forgets the pin of every instance of the Pod whose UID is pod, but the one whose container is
// keep.
func (p *pins) drop(pod string, keep string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.held {
		if i.pod == pod && i.container != keep {
			delete(p.held, i)
		}
	}
}
