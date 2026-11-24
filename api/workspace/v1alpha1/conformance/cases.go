package conformance

import (
	"bytes"
	"cmp"
	"io"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// file is a path under the data volume the cases write.
const file = v1alpha1.DataPath + "/conformance.txt"

// Cases returns every case of the suite.
func Cases() []Case {
	return []Case{
		{Requirement: "NAME-1", Name: "a name that is not a DNS label is refused", run: nameRefused},
		{Requirement: "SPEC-1", Name: "an image without a digest is refused", run: specRefused},
		{Requirement: "LIFE-1", Name: "create is idempotent by name and refuses another spec", run: createIdempotent},
		{Requirement: "LIFE-2", Name: "a created workspace runs and is listed", run: createdRuns},
		{Requirement: "LIFE-3", Name: "stop and start are idempotent and reach their phase", run: stopStart},
		{Requirement: "LIFE-4", Name: "delete is idempotent and the name is freed", run: deleteFrees},
		{Requirement: "LIFE-5", Name: "a name the caller does not have is not found", run: unknownName},
		{Requirement: "RUN-1", Name: "guest calls to a stopped workspace are refused", run: notRunning},
		{Requirement: "DATA-1", Name: "the data volume survives a stop and a start", run: dataSurvives},
		{Requirement: "EXEC-1", Name: "exec returns the exit code and the output whole", run: execResult},
		{Requirement: "FILE-1", Name: "a written file reads back whole", run: fileRoundTrip},
		{Requirement: "FILE-2", Name: "a read of a missing file is file not found", run: fileMissing},
		{Requirement: "ISO-1", Name: "one workspace does not see another's files", run: isolated},
		{Requirement: "DIAL-1", Name: "dial reaches a spec port and refuses another", run: dial},
		{Requirement: "NET-1", Name: "egress rules are refused without the egress capability", run: egressRefused},
		{
			Requirement: "NET-1", Name: "a workspace sends nothing its egress rules do not allow",
			Pending: "needs a conformance image that reports what it can reach; the reference implementation sends nothing",
		},
		{Requirement: "SNAP-1", Name: "snapshot is idempotent by name and refuses another workspace's", run: snapshotIdempotent},
		{Requirement: "SNAP-2", Name: "a restore holds the snapshot's data and outlives its source", run: restore},
		{Requirement: "SNAP-3", Name: "snapshot is refused without the snapshot capability", run: snapshotRefused},
		{
			Requirement: "VER-2", Name: "a caller reads a phase it does not know as unknown",
			Pending: "a caller obligation; the caller suite arrives with the first caller",
		},
	}
}

func nameRefused(t *testing.T, e *env) {
	_, _, err := e.Provider.Create(t.Context(), "Not_A_Label", e.spec())
	wantErr(t, "Create(Not_A_Label)", err, v1alpha1.ErrInvalid)
	e.running(t, e.spec()) // control: the same spec under a valid name is taken
}

func specRefused(t *testing.T, e *env) {
	spec := e.spec()
	spec.Image = "registry.example/conformance:latest"
	_, _, err := e.Provider.Create(t.Context(), e.name(t), spec)
	wantErr(t, "Create with a tag", err, v1alpha1.ErrInvalid)
	e.running(t, e.spec()) // control: the image as given is taken
}

func createIdempotent(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	_, created, err := e.Provider.Create(t.Context(), name, e.spec())
	wantNoErr(t, "Create again", err)
	if created {
		t.Fatalf("Create again with the same spec reported created")
	}
	other := e.spec()
	other.Env = map[string]string{"CONFORMANCE": "other"}
	_, _, err = e.Provider.Create(t.Context(), name, other)
	wantErr(t, "Create with another spec", err, v1alpha1.ErrConflict)
}

func createdRuns(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	list, err := e.Provider.List(t.Context())
	wantNoErr(t, "List", err)
	if !slices.ContainsFunc(list, func(w v1alpha1.Workspace) bool { return w.Name == name }) {
		t.Fatalf("List does not name %s", name)
	}
	if !slices.IsSortedFunc(list, func(a, b v1alpha1.Workspace) int { return cmp.Compare(a.Name, b.Name) }) {
		t.Fatalf("List is not ordered by name")
	}
}

func stopStart(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	for range 2 {
		_, err := e.Provider.Stop(t.Context(), name, 10*time.Second)
		wantNoErr(t, "Stop", err)
	}
	e.waitPhase(t, name, v1alpha1.PhaseStopped)
	for range 2 {
		_, err := e.Provider.Start(t.Context(), name)
		wantNoErr(t, "Start", err)
	}
	e.waitPhase(t, name, v1alpha1.PhaseRunning)
}

func deleteFrees(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	wantNoErr(t, "Delete", e.Provider.Delete(t.Context(), name))
	wantNoErr(t, "Delete again", e.Provider.Delete(t.Context(), name))
	var err error
	gone := e.poll(t, func() bool {
		_, err = e.Provider.Get(t.Context(), name)
		return err != nil
	})
	if !gone {
		t.Fatalf("Get still answers %s after Delete", name)
	}
	wantErr(t, "Get after Delete", err, v1alpha1.ErrNotFound)
	recreated := e.poll(t, func() bool {
		_, _, err = e.Provider.Create(t.Context(), name, e.spec())
		return err == nil
	})
	if !recreated {
		t.Fatalf("the name %s is not freed after Delete: %v", name, err)
	}
}

func unknownName(t *testing.T, e *env) {
	name := e.name(t)
	_, err := e.Provider.Get(t.Context(), name)
	wantErr(t, "Get", err, v1alpha1.ErrNotFound)
	_, err = e.Provider.Start(t.Context(), name)
	wantErr(t, "Start", err, v1alpha1.ErrNotFound)
	_, err = e.Provider.Exec(t.Context(), name, v1alpha1.ExecRequest{Command: []string{"true"}})
	wantErr(t, "Exec", err, v1alpha1.ErrNotFound)
	_, err = e.Provider.GetSnapshot(t.Context(), name)
	wantErr(t, "GetSnapshot", err, v1alpha1.ErrNotFound)
}

func notRunning(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	_, err := e.Provider.Exec(t.Context(), name, v1alpha1.ExecRequest{Command: []string{"true"}})
	wantNoErr(t, "Exec while running", err) // control
	_, err = e.Provider.Stop(t.Context(), name, 10*time.Second)
	wantNoErr(t, "Stop", err)
	e.waitPhase(t, name, v1alpha1.PhaseStopped)
	_, err = e.Provider.Exec(t.Context(), name, v1alpha1.ExecRequest{Command: []string{"true"}})
	wantErr(t, "Exec", err, v1alpha1.ErrNotRunning)
	_, err = e.Provider.ReadFile(t.Context(), name, file)
	wantErr(t, "ReadFile", err, v1alpha1.ErrNotRunning)
	err = e.Provider.WriteFile(t.Context(), name, file, []byte("x"))
	wantErr(t, "WriteFile", err, v1alpha1.ErrNotRunning)
	_, err = e.Provider.Dial(t.Context(), name, EchoPort)
	wantErr(t, "Dial", err, v1alpha1.ErrNotRunning)
}

func dataSurvives(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	want := []byte("kept across a stop\n")
	wantNoErr(t, "WriteFile", e.Provider.WriteFile(t.Context(), name, file, want))
	_, err := e.Provider.Stop(t.Context(), name, 10*time.Second)
	wantNoErr(t, "Stop", err)
	e.waitPhase(t, name, v1alpha1.PhaseStopped)
	_, err = e.Provider.Start(t.Context(), name)
	wantNoErr(t, "Start", err)
	e.waitPhase(t, name, v1alpha1.PhaseRunning)
	got, err := e.Provider.ReadFile(t.Context(), name, file)
	wantNoErr(t, "ReadFile", err)
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFile after a stop and a start: %q, want %q", got, want)
	}
}

func execResult(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	in := []byte("line one\nline two\n")
	r, err := e.Provider.Exec(t.Context(), name, v1alpha1.ExecRequest{Command: []string{"cat"}, Stdin: in})
	wantNoErr(t, "Exec cat", err)
	if r.ExitCode != 0 || !bytes.Equal(r.Stdout, in) {
		t.Fatalf("Exec cat: exit %d, stdout %q, want 0 and %q", r.ExitCode, r.Stdout, in)
	}
	r, err = e.Provider.Exec(t.Context(), name, v1alpha1.ExecRequest{Command: []string{"false"}})
	wantNoErr(t, "Exec false", err)
	if r.ExitCode != 1 {
		t.Fatalf("Exec false: exit %d, want 1", r.ExitCode)
	}
}

func fileRoundTrip(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	wantNoErr(t, "WriteFile", e.Provider.WriteFile(t.Context(), name, file, []byte("a longer first content\n")))
	want := []byte("second\n")
	wantNoErr(t, "WriteFile again", e.Provider.WriteFile(t.Context(), name, file, want))
	got, err := e.Provider.ReadFile(t.Context(), name, file)
	wantNoErr(t, "ReadFile", err)
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFile: %q, want the whole second write %q", got, want)
	}
}

func fileMissing(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	_, err := e.Provider.ReadFile(t.Context(), name, v1alpha1.DataPath+"/absent.txt")
	wantErr(t, "ReadFile of a missing file", err, v1alpha1.ErrFileNotFound)
}

func isolated(t *testing.T, e *env) {
	a, b := e.running(t, e.spec()), e.running(t, e.spec())
	wantNoErr(t, "WriteFile in a", e.Provider.WriteFile(t.Context(), a, file, []byte("a's\n")))
	_, err := e.Provider.ReadFile(t.Context(), a, file)
	wantNoErr(t, "ReadFile in a", err) // control
	_, err = e.Provider.ReadFile(t.Context(), b, file)
	wantErr(t, "ReadFile in b", err, v1alpha1.ErrFileNotFound)
}

func dial(t *testing.T, e *env) {
	name := e.running(t, e.spec())
	conn, err := e.Provider.Dial(t.Context(), name, EchoPort)
	wantNoErr(t, "Dial the echo port", err)
	defer conn.Close()
	want := []byte("echo\n")
	_ = conn.SetDeadline(time.Now().Add(e.Timeout))
	go func() { _, _ = conn.Write(want) }()
	got := make([]byte, len(want))
	_, err = io.ReadFull(conn, got)
	wantNoErr(t, "read the echo", err)
	if !bytes.Equal(got, want) {
		t.Fatalf("echo: %q, want %q", got, want)
	}
	_, err = e.Provider.Dial(t.Context(), name, EchoPort+1)
	wantErr(t, "Dial a port not in the spec", err, v1alpha1.ErrInvalid)
}

func egressRefused(t *testing.T, e *env) {
	if e.caps.Egress {
		t.Skip("the implementation offers egress rules")
	}
	spec := e.spec()
	spec.Egress = []v1alpha1.EgressRule{{Prefix: netip.MustParsePrefix("192.0.2.0/24"), Protocol: v1alpha1.ProtocolTCP}}
	_, _, err := e.Provider.Create(t.Context(), e.name(t), spec)
	wantErr(t, "Create with an egress rule", err, v1alpha1.ErrUnsupported)
	e.running(t, e.spec()) // control: the spec without the rule is taken
}

func snapshotIdempotent(t *testing.T, e *env) {
	if !e.caps.Snapshot {
		t.Skip("the implementation offers no snapshot")
	}
	a, b := e.running(t, e.spec()), e.running(t, e.spec())
	snap := e.name(t)
	_, created, err := e.Provider.Snapshot(t.Context(), snap, a)
	wantNoErr(t, "Snapshot", err)
	if !created {
		t.Fatalf("the first Snapshot reported not created")
	}
	_, created, err = e.Provider.Snapshot(t.Context(), snap, a)
	wantNoErr(t, "Snapshot again", err)
	if created {
		t.Fatalf("Snapshot again of the same workspace reported created")
	}
	_, _, err = e.Provider.Snapshot(t.Context(), snap, b)
	wantErr(t, "Snapshot of another workspace under the name", err, v1alpha1.ErrConflict)
}

func restore(t *testing.T, e *env) {
	if !e.caps.Snapshot {
		t.Skip("the implementation offers no snapshot")
	}
	source := e.running(t, e.spec())
	want := []byte("restored\n")
	wantNoErr(t, "WriteFile", e.Provider.WriteFile(t.Context(), source, file, want))
	snap := e.name(t)
	_, _, err := e.Provider.Snapshot(t.Context(), snap, source)
	wantNoErr(t, "Snapshot", err)
	ready := e.poll(t, func() bool {
		s, err := e.Provider.GetSnapshot(t.Context(), snap)
		return err == nil && s.Phase == v1alpha1.SnapshotReady
	})
	if !ready {
		t.Fatalf("snapshot %s did not become ready", snap)
	}
	wantNoErr(t, "Delete the source", e.Provider.Delete(t.Context(), source))
	spec := e.spec()
	spec.FromSnapshot = snap
	restored := e.running(t, spec)
	got, err := e.Provider.ReadFile(t.Context(), restored, file)
	wantNoErr(t, "ReadFile in the restore", err)
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFile in the restore: %q, want %q", got, want)
	}
	spec.FromSnapshot = e.name(t)
	_, _, err = e.Provider.Create(t.Context(), e.name(t), spec)
	wantErr(t, "Create from a missing snapshot", err, v1alpha1.ErrSnapshotNotFound)
}

func snapshotRefused(t *testing.T, e *env) {
	if e.caps.Snapshot {
		t.Skip("the implementation offers snapshots")
	}
	name := e.running(t, e.spec())
	_, _, err := e.Provider.Snapshot(t.Context(), e.name(t), name)
	wantErr(t, "Snapshot", err, v1alpha1.ErrUnsupported)
}
