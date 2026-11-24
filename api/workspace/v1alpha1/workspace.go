package v1alpha1

import (
	"fmt"
	"maps"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Redacted is what a read answers in place of an Env value, which it never returns (LIFE-2).
const Redacted = "<set>"

// CheckPath returns ErrInvalid for a guest path a file call refuses: one that is not absolute and
// clean, is "/", or is under /proc, /sys or /dev (FILE-1).
func CheckPath(p string) error {
	if !path.IsAbs(p) || path.Clean(p) != p || p == "/" {
		return fmt.Errorf("%w: path %q is not absolute and clean, or is /", ErrInvalid, p)
	}
	for _, d := range []string{"/proc", "/sys", "/dev"} {
		if p == d || strings.HasPrefix(p, d+"/") {
			return fmt.Errorf("%w: path %q is under %s", ErrInvalid, p, d)
		}
	}

	return nil
}

// Name names a workspace or a snapshot: a DNS label, chosen by the caller (NAME-1).
type Name string

var labelPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ParseName returns s as a Name, or ErrInvalid when it is not a DNS label of at most 63
// characters.
func ParseName(s string) (Name, error) {
	if !labelPattern.MatchString(s) {
		return "", fmt.Errorf("%w: name %q is not a DNS label", ErrInvalid, s)
	}

	return Name(s), nil
}

// Spec is what a caller asks a workspace to be.
type Spec struct {
	// Image is the workspace's image, repository@sha256:<64 hex digits>. Which image runs is the
	// execution place's configuration, never a declaration's.
	Image     string
	Resources Resources
	// Env is the environment of the image's entrypoint and of every Exec. It holds no secret, and a
	// read answers each value as Redacted (LIFE-2).
	Env map[string]string
	// Ports are the workspace's ports Dial may reach.
	Ports []uint16
	// Egress are the only destinations the workspace may send to. Empty, it sends nothing out.
	Egress []EgressRule
	// FromSnapshot, when set, makes the data volume from that snapshot (SNAP-2).
	FromSnapshot Name
}

// Resources are what one workspace gets, each more than zero.
type Resources struct {
	// CPU is whole CPUs. A caller rounds a WorkspaceClass's quantity up to it.
	CPU uint32
	// MemoryBytes is the workspace's memory.
	MemoryBytes uint64
	// DiskBytes is the size of the data volume at DataPath.
	DiskBytes uint64
}

// Protocol is the transport protocol of an egress rule.
type Protocol string

// The protocols an egress rule takes.
const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

// EgressRule lets the workspace send to Prefix over Protocol, on Ports, every port when empty.
type EgressRule struct {
	Prefix   netip.Prefix
	Protocol Protocol
	Ports    []PortRange
}

// PortRange is First to Last, inclusive.
type PortRange struct {
	First uint16
	Last  uint16
}

var imagePattern = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)

// Check returns ErrInvalid naming the first part of spec this version refuses (SPEC-1).
func (s Spec) Check() error {
	switch {
	case !imagePattern.MatchString(s.Image):
		return fmt.Errorf("%w: image %q is not repository@sha256:<digest>", ErrInvalid, s.Image)
	case s.Resources.CPU == 0 || s.Resources.MemoryBytes == 0 || s.Resources.DiskBytes == 0:
		return fmt.Errorf("%w: cpu, memory and disk are each more than zero", ErrInvalid)
	case slices.Contains(s.Ports, 0):
		return fmt.Errorf("%w: port 0", ErrInvalid)
	case len(slices.Compact(slices.Sorted(slices.Values(s.Ports)))) != len(s.Ports):
		return fmt.Errorf("%w: a port is repeated", ErrInvalid)
	case s.FromSnapshot != "" && !labelPattern.MatchString(string(s.FromSnapshot)):
		return fmt.Errorf("%w: snapshot name %q is not a DNS label", ErrInvalid, s.FromSnapshot)
	}
	for name := range s.Env {
		if name == "" || strings.Contains(name, "=") {
			return fmt.Errorf("%w: env name %q is empty or holds \"=\"", ErrInvalid, name)
		}
	}
	for _, r := range s.Egress {
		if err := r.check(); err != nil {
			return err
		}
	}

	return nil
}

func (r EgressRule) check() error {
	if !r.Prefix.IsValid() || r.Prefix.Masked() != r.Prefix {
		return fmt.Errorf("%w: egress prefix %s is not a prefix in canonical form", ErrInvalid, r.Prefix)
	}
	if r.Protocol != ProtocolTCP && r.Protocol != ProtocolUDP {
		return fmt.Errorf("%w: egress protocol %q", ErrInvalid, r.Protocol)
	}
	for _, p := range r.Ports {
		if p.First == 0 || p.First > p.Last {
			return fmt.Errorf("%w: egress ports %d-%d", ErrInvalid, p.First, p.Last)
		}
	}

	return nil
}

// SameSpec reports whether a and b ask for the same workspace, the comparison Create's
// idempotency makes (LIFE-1). The order of Ports and of Egress counts, as written. Every field it
// compares reaches the backend unchanged, so a backend that compares what it was given, as naru's
// Machine API does, makes the same comparison.
func SameSpec(a, b Spec) bool {
	return a.Image == b.Image &&
		a.Resources == b.Resources &&
		maps.Equal(a.Env, b.Env) &&
		slices.Equal(a.Ports, b.Ports) &&
		slices.EqualFunc(a.Egress, b.Egress, func(x, y EgressRule) bool {
			return x.Prefix == y.Prefix && x.Protocol == y.Protocol && slices.Equal(x.Ports, y.Ports)
		}) &&
		a.FromSnapshot == b.FromSnapshot
}

// Phase is where a workspace is in its life. A caller reads a phase it does not know as
// PhaseUnknown (VER-2).
type Phase string

// The phases.
const (
	// PhasePending: recorded, and not yet running: being placed, started, restored or moved.
	PhasePending  Phase = "pending"
	PhaseRunning  Phase = "running"
	PhaseStopping Phase = "stopping"
	PhaseStopped  Phase = "stopped"
	// PhaseFailed: it cannot run as specified, and Status.Reason says why.
	PhaseFailed Phase = "failed"
	// PhaseUnknown: the implementation cannot tell, and Status.Reason says why.
	PhaseUnknown Phase = "unknown"
)

// Workspace is a workspace as a Provider answers it.
type Workspace struct {
	Name   Name
	Spec   Spec
	Status Status
}

// Status is what the implementation last learned of the workspace.
type Status struct {
	Phase Phase
	// Reason says why a pending, failed or unknown workspace is so.
	Reason string
}

// ExecRequest is one command: Command[0] is run with the rest as its arguments, not through a
// shell.
type ExecRequest struct {
	Command []string
	Stdin   []byte
	// Timeout, when more than zero, bounds the command's run.
	Timeout time.Duration
}

// ExecResult is what a command returned, whole. A command that ran and exited non-zero is a result,
// not an error.
type ExecResult struct {
	ExitCode int32
	Stdout   []byte
	Stderr   []byte
}

// SnapshotPhase is where a snapshot is in being taken. A caller reads a phase it does not know as
// SnapshotUnknown.
type SnapshotPhase string

// The snapshot phases.
const (
	SnapshotPending SnapshotPhase = "pending"
	SnapshotReady   SnapshotPhase = "ready"
	SnapshotFailed  SnapshotPhase = "failed"
	SnapshotUnknown SnapshotPhase = "unknown"
)

// Snapshot is the data volume of one workspace, taken at one time. It outlives the workspace until
// it is deleted.
type Snapshot struct {
	Name Name
	// Workspace is the name of the workspace it was taken of.
	Workspace Name
	// DiskBytes is the size of the data volume it holds, which a restore's spec must equal (SNAP-2).
	DiskBytes uint64
	Phase     SnapshotPhase
	Reason    string
}
