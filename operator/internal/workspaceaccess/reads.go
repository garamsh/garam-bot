package workspaceaccess

import (
	"context"
	"errors"
	"sync"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// errStillReading answers a read of a workspace whose earlier read has not returned.
var errStillReading = errors.New("an earlier read of the workspace has not returned")

// reads runs a recheck's provider reads so that one which ignores its context cannot hold the
// recheck: the recheck stops waiting at its deadline, and the read is not started again while it runs.
type reads struct {
	mu      sync.Mutex
	running map[spaceKey]bool
}

func newReads() *reads { return &reads{running: map[spaceKey]bool{}} }

// get reads the workspace from provider, and answers ctx's error where ctx ends first.
func (r *reads) get(ctx context.Context, key spaceKey, provider workspace.Provider) (workspace.Workspace, error) {
	r.mu.Lock()
	if r.running[key] {
		r.mu.Unlock()
		return workspace.Workspace{}, errStillReading
	}
	r.running[key] = true
	r.mu.Unlock()
	type answer struct {
		ws  workspace.Workspace
		err error
	}
	done := make(chan answer, 1)
	go func() {
		ws, err := provider.Get(ctx, key.name)
		r.mu.Lock()
		delete(r.running, key)
		r.mu.Unlock()
		done <- answer{ws, err}
	}()
	select {
	case a := <-done:
		return a.ws, a.err
	case <-ctx.Done():
		return workspace.Workspace{}, ctx.Err()
	}
}
