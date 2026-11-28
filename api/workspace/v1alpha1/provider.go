package v1alpha1

import (
	"context"
	"net"
	"time"
)

// SpecRelease is the release of v1alpha1's specification this package states.
const SpecRelease = "0.2.1"

// DataPath is where a workspace's data volume is mounted: what survives a stop, a start and a
// restore is under it (DATA-1, SNAP-2).
const DataPath = "/workspace"

// Provider is an implementation of the workspace provider interface. Every method is safe for
// concurrent use, and every name it takes is the caller's own: a name another caller of the same
// implementation holds is answered as ErrNotFound.
type Provider interface {
	// Capabilities reports what this implementation offers (NET-1, SNAP-3).
	Capabilities(ctx context.Context) (Capabilities, error)

	// Create makes the workspace name from spec, running. It is idempotent by name: created is
	// false when name already holds the same spec, and ErrConflict is returned when it holds
	// another (LIFE-1).
	Create(ctx context.Context, name Name, spec Spec) (ws Workspace, created bool, err error)
	// Get returns the workspace name.
	Get(ctx context.Context, name Name) (Workspace, error)
	// List returns every workspace of the caller, ordered by name.
	List(ctx context.Context) ([]Workspace, error)
	// Start sets the workspace's desired state to running. It is idempotent (LIFE-3).
	Start(ctx context.Context, name Name) (Workspace, error)
	// Stop sets the workspace's desired state to stopped, giving it grace to stop before it is
	// ended. It is idempotent (LIFE-3).
	Stop(ctx context.Context, name Name, grace time.Duration) (Workspace, error)
	// Delete deletes the workspace and its data volume, not its snapshots. It is idempotent
	// (LIFE-4).
	Delete(ctx context.Context, name Name) error

	// Exec runs one command in the running workspace and returns its result whole (EXEC-1).
	Exec(ctx context.Context, name Name, req ExecRequest) (ExecResult, error)
	// ReadFile returns the content of the file at path in the running workspace (FILE-1).
	ReadFile(ctx context.Context, name Name, path string) ([]byte, error)
	// WriteFile replaces the file at path in the running workspace with content, whole (FILE-1).
	WriteFile(ctx context.Context, name Name, path string, content []byte) error
	// Dial opens a connection to port in the running workspace, one of its spec's Ports (DIAL-1).
	Dial(ctx context.Context, name Name, port uint16) (net.Conn, error)

	// Snapshot takes the data volume of the workspace of into the snapshot name. It is idempotent
	// by name (SNAP-1).
	Snapshot(ctx context.Context, name Name, of Name) (snap Snapshot, created bool, err error)
	// GetSnapshot returns the snapshot name.
	GetSnapshot(ctx context.Context, name Name) (Snapshot, error)
	// DeleteSnapshot deletes the snapshot name. It is idempotent.
	DeleteSnapshot(ctx context.Context, name Name) error
}

// Capabilities are what an implementation offers beyond the operations every one serves.
type Capabilities struct {
	// Snapshot is whether Snapshot and a restore through Spec.FromSnapshot are served (SNAP-3).
	Snapshot bool
	// Egress is whether a spec's Egress rules are enforced. Without it, only a spec with no rule is
	// taken, and its workspace sends nothing out (NET-1).
	Egress bool
	// Stdin is whether an Exec's Stdin reaches the command. Without it, only an Exec with no Stdin
	// is taken (EXEC-2).
	Stdin bool
}
