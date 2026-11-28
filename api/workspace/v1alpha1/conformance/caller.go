package conformance

import (
	"context"
	"testing"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// Caller is the code that reads a provider's answers, as far as interface 3 binds it: a caller
// suite runs it with RunCaller. A caller adapts to it, as a provider adapts to Subject.
type Caller interface {
	// WorkspacePhase reads the phase of the workspace name through p, as the caller reads it.
	WorkspacePhase(ctx context.Context, p v1alpha1.Provider, name v1alpha1.Name) (v1alpha1.Phase, error)
	// SnapshotPhase reads the phase of the snapshot name through p, as the caller reads it.
	SnapshotPhase(ctx context.Context, p v1alpha1.Provider, name v1alpha1.Name) (v1alpha1.SnapshotPhase, error)
}

// CallerCase is one test of the caller suite.
type CallerCase struct {
	// Requirement is the identifier of the requirement in ../README.md the case tests.
	Requirement string
	// Name says what the case holds.
	Name string
	run  func(t *testing.T, c Caller)
}

// CallerCases returns every case of the caller suite.
func CallerCases() []CallerCase {
	return []CallerCase{
		{Requirement: "VER-2", Name: "a caller reads a phase it does not know as unknown", run: readsUnknownPhase},
		{Requirement: "VER-2", Name: "a caller reads a snapshot phase it does not know as unknown", run: readsUnknownSnapshotPhase},
	}
}

// RunCaller runs every case of the caller suite against c, each as a subtest named
// <requirement>/<case>. The suite serves the caller a provider that answers what it scripts, and
// needs no real one.
func RunCaller(t *testing.T, c Caller) {
	t.Helper()
	for _, cc := range CallerCases() {
		t.Run(cc.Requirement+"/"+cc.Name, func(t *testing.T) { cc.run(t, c) })
	}
}

// scripted is a provider that answers every workspace and snapshot with the phase it holds,
// including one a later release adds.
type scripted struct {
	v1alpha1.Provider // nil: a caller that reads a phase calls nothing else
	workspace         v1alpha1.Phase
	snapshot          v1alpha1.SnapshotPhase
}

func (s scripted) Get(_ context.Context, name v1alpha1.Name) (v1alpha1.Workspace, error) {
	return v1alpha1.Workspace{Name: name, Status: v1alpha1.Status{Phase: s.workspace}}, nil
}

func (s scripted) GetSnapshot(_ context.Context, name v1alpha1.Name) (v1alpha1.Snapshot, error) {
	return v1alpha1.Snapshot{Name: name, Phase: s.snapshot}, nil
}

func readsUnknownPhase(t *testing.T, c Caller) {
	for _, tc := range []struct{ answered, want v1alpha1.Phase }{
		{"migrating", v1alpha1.PhaseUnknown},
		{v1alpha1.PhasePending, v1alpha1.PhasePending}, // controls: a caller that reads every phase as unknown is wrong
		{v1alpha1.PhaseRunning, v1alpha1.PhaseRunning},
		{v1alpha1.PhaseStopping, v1alpha1.PhaseStopping},
		{v1alpha1.PhaseStopped, v1alpha1.PhaseStopped},
		{v1alpha1.PhaseFailed, v1alpha1.PhaseFailed},
		{v1alpha1.PhaseUnknown, v1alpha1.PhaseUnknown},
	} {
		got, err := c.WorkspacePhase(t.Context(), scripted{workspace: tc.answered}, "w")
		if err != nil {
			t.Fatalf("WorkspacePhase of the phase %q: %v", tc.answered, err)
		}
		if got != tc.want {
			t.Fatalf("WorkspacePhase of the phase %q = %q, want %q", tc.answered, got, tc.want)
		}
	}
}

func readsUnknownSnapshotPhase(t *testing.T, c Caller) {
	for _, tc := range []struct{ answered, want v1alpha1.SnapshotPhase }{
		{"archived", v1alpha1.SnapshotUnknown},
		{v1alpha1.SnapshotPending, v1alpha1.SnapshotPending}, // controls
		{v1alpha1.SnapshotReady, v1alpha1.SnapshotReady},
		{v1alpha1.SnapshotFailed, v1alpha1.SnapshotFailed},
		{v1alpha1.SnapshotUnknown, v1alpha1.SnapshotUnknown},
	} {
		got, err := c.SnapshotPhase(t.Context(), scripted{snapshot: tc.answered}, "s")
		if err != nil {
			t.Fatalf("SnapshotPhase of the phase %q: %v", tc.answered, err)
		}
		if got != tc.want {
			t.Fatalf("SnapshotPhase of the phase %q = %q, want %q", tc.answered, got, tc.want)
		}
	}
}
