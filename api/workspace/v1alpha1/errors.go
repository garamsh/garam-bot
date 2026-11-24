package v1alpha1

import "errors"

// The refusals a Provider returns, each wrapped with %w so a caller branches with errors.Is. An
// implementation translates its backend's errors into these before it returns, and returns any
// other failure as an error none of them matches.
var (
	// ErrInvalid: a name, a spec or a request this version refuses (NAME-1, SPEC-1, DIAL-1).
	ErrInvalid = errors.New("invalid")
	// ErrNotFound: no workspace or snapshot of the caller's under the name (LIFE-5).
	ErrNotFound = errors.New("not found")
	// ErrSnapshotNotFound: a restore names a snapshot the caller does not have (SNAP-2).
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrConflict: the name holds another spec, or a snapshot of another workspace (LIFE-1,
	// SNAP-1).
	ErrConflict = errors.New("conflict")
	// ErrDeleting: the name is held by a workspace being deleted (LIFE-4).
	ErrDeleting = errors.New("deleting")
	// ErrNotRunning: an Exec, a file call or a Dial to a workspace that is not running (RUN-1).
	ErrNotRunning = errors.New("not running")
	// ErrFileNotFound: a read of a path that holds no file (FILE-2).
	ErrFileNotFound = errors.New("file not found")
	// ErrUnsupported: a call the implementation's Capabilities exclude (NET-1, SNAP-3).
	ErrUnsupported = errors.New("unsupported")
)
