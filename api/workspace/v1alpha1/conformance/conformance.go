// Package conformance is the conformance suite of interface 3, the workspace provider: the cases a
// v1alpha1.Provider must pass, each named for the requirement in ../README.md it tests. An
// implementation plugs in as a Subject and runs the suite with Run.
package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// Subject is an implementation under test, with what the suite needs to drive it.
type Subject struct {
	Provider v1alpha1.Provider
	// Image is the conformance image as this implementation runs it (../conformance.md).
	Image string
	// Resources are what each workspace the suite makes gets.
	Resources v1alpha1.Resources
	// Timeout bounds each wait for a workspace or a snapshot to reach a phase. Zero is 3 minutes.
	Timeout time.Duration
}

// EchoPort is the port on which the conformance image echoes every byte it is sent.
const EchoPort uint16 = 7

// Case is one test of the suite.
type Case struct {
	// Requirement is the identifier of the requirement in ../README.md the case tests.
	Requirement string
	// Name says what the case holds.
	Name string
	// Pending, when set, says why the case cannot run yet; the case is skipped.
	Pending string
	run     func(t *testing.T, e *env)
}

// Run runs every case against s, each as a subtest named <requirement>/<case>.
func Run(t *testing.T, s Subject) {
	t.Helper()
	if s.Timeout == 0 {
		s.Timeout = 3 * time.Minute
	}
	caps, err := s.Provider.Capabilities(t.Context())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	for _, c := range Cases() {
		t.Run(c.Requirement+"/"+c.Name, func(t *testing.T) {
			if c.Pending != "" {
				t.Skip("pending: " + c.Pending)
			}
			c.run(t, &env{Subject: s, caps: caps})
		})
	}
}

// env is what a case runs with.
type env struct {
	Subject
	caps v1alpha1.Capabilities
}

// spec returns the spec the suite makes a workspace from, with the echo port open.
func (e *env) spec() v1alpha1.Spec {
	return v1alpha1.Spec{Image: e.Image, Resources: e.Resources, Ports: []uint16{EchoPort}}
}

// name returns a name no other case uses, and deletes the workspace and the snapshot of that name
// when the case ends.
func (e *env) name(t *testing.T) v1alpha1.Name {
	t.Helper()
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	name := v1alpha1.Name("conformance-" + hex.EncodeToString(b))
	t.Cleanup(func() {
		ctx := context.Background()
		_ = e.Provider.Delete(ctx, name)
		_ = e.Provider.DeleteSnapshot(ctx, name)
	})

	return name
}

// running creates a workspace from spec and waits for it to run.
func (e *env) running(t *testing.T, spec v1alpha1.Spec) v1alpha1.Name {
	t.Helper()
	name := e.name(t)
	if _, created, err := e.Provider.Create(t.Context(), name, spec); err != nil || !created {
		t.Fatalf("Create(%s): created %v, %v", name, created, err)
	}
	e.waitPhase(t, name, v1alpha1.PhaseRunning)

	return name
}

// waitPhase waits for the workspace name to report phase, and fails the case at the timeout.
func (e *env) waitPhase(t *testing.T, name v1alpha1.Name, phase v1alpha1.Phase) {
	t.Helper()
	var last v1alpha1.Workspace
	var err error
	ok := e.poll(t, func() bool {
		last, err = e.Provider.Get(t.Context(), name)
		return err == nil && last.Status.Phase == phase
	})
	if !ok {
		t.Fatalf("%s did not reach %s within %s: last %+v, %v", name, phase, e.Timeout, last.Status, err)
	}
}

// poll calls cond until it holds or the timeout passes, and reports whether it held.
func (e *env) poll(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(e.Timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-t.Context().Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// wantErr fails the case unless err matches target.
func wantErr(t *testing.T, call string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got %v, want %v", call, err, target)
	}
}

// wantNoErr fails the case on err.
func wantNoErr(t *testing.T, call string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", call, err)
	}
}
