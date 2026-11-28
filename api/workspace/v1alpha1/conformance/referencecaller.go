package conformance

import (
	"context"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// ReferenceCaller is the smallest caller that keeps interface 3's obligations on one: the suite's
// own subject, as workspacefake is the provider's. A caller binds the same cases to its own code
// with RunCaller, and does not copy this.
type ReferenceCaller struct{}

// WorkspacePhase reads the workspace's phase, and any phase it does not know as unknown (VER-2).
func (ReferenceCaller) WorkspacePhase(ctx context.Context, p v1alpha1.Provider, name v1alpha1.Name) (v1alpha1.Phase, error) {
	ws, err := p.Get(ctx, name)
	if err != nil {
		return "", err
	}
	switch ws.Status.Phase {
	case v1alpha1.PhasePending, v1alpha1.PhaseRunning, v1alpha1.PhaseStopping, v1alpha1.PhaseStopped, v1alpha1.PhaseFailed:
		return ws.Status.Phase, nil
	default:
		return v1alpha1.PhaseUnknown, nil
	}
}

// SnapshotPhase reads the snapshot's phase, and any phase it does not know as unknown (VER-2).
func (ReferenceCaller) SnapshotPhase(ctx context.Context, p v1alpha1.Provider, name v1alpha1.Name) (v1alpha1.SnapshotPhase, error) {
	snap, err := p.GetSnapshot(ctx, name)
	if err != nil {
		return "", err
	}
	switch snap.Phase {
	case v1alpha1.SnapshotPending, v1alpha1.SnapshotReady, v1alpha1.SnapshotFailed:
		return snap.Phase, nil
	default:
		return v1alpha1.SnapshotUnknown, nil
	}
}
