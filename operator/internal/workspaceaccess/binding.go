package workspaceaccess

import (
	"context"
	"sync"
	"time"
)

// maxBindingFresh is the longest a credential's binding to its instance is kept (CRED-2's cache).
const maxBindingFresh = 10 * time.Second

// bindings keeps each Pod a credential names, as last read, for fresh at most, so the API server is not
// read for every request.
type bindings struct {
	pods  Pods
	fresh time.Duration
	now   func() time.Time

	mu   sync.Mutex
	held map[podKey]heldPod
}

type podKey struct{ namespace, name string }

type heldPod struct {
	pod    Pod
	exists bool
	readAt time.Time
}

func newBindings(pods Pods, fresh time.Duration, now func() time.Time) *bindings {
	if fresh <= 0 || fresh > maxBindingFresh {
		fresh = maxBindingFresh
	}
	return &bindings{pods: pods, fresh: fresh, now: now, held: map[podKey]heldPod{}}
}

// pod answers the Pod namespace/name, from what was read within fresh unless again is set, when it is
// read from the API server.
func (b *bindings) pod(ctx context.Context, namespace, name string, again bool) (Pod, bool, error) {
	key := podKey{namespace, name}
	b.mu.Lock()
	held, ok := b.held[key]
	b.mu.Unlock()
	if ok && !again && b.now().Sub(held.readAt) < b.fresh {
		return held.pod, held.exists, nil
	}
	pod, exists, err := b.pods.Pod(ctx, namespace, name)
	if err != nil {
		return Pod{}, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held[key] = heldPod{pod: pod, exists: exists, readAt: b.now()}
	return pod, exists, nil
}

// forget drops the reads no longer fresh, on Run's tick rather than per request.
func (b *bindings) forget() {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, h := range b.held {
		if now.Sub(h.readAt) >= b.fresh {
			delete(b.held, k)
		}
	}
}
