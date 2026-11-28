package naruworkspace

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/naru/lib/machineapi"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// TestPhase_FollowsTheMappingTable holds README §Mapping onto naru's Machine API's phase table, row by
// row; machineapifake reaches no phase but pending, running, stopping, stopped and deleting.
func TestPhase_FollowsTheMappingTable(t *testing.T) {
	for machine, want := range map[machineapi.Phase]v1alpha1.Phase{
		machineapi.PhasePending:  v1alpha1.PhasePending,
		machineapi.PhaseStarting: v1alpha1.PhasePending,
		machineapi.PhaseMoving:   v1alpha1.PhasePending,
		machineapi.PhaseRunning:  v1alpha1.PhaseRunning,
		machineapi.PhaseStopping: v1alpha1.PhaseStopping,
		machineapi.PhaseDeleting: v1alpha1.PhaseStopping,
		machineapi.PhaseStopped:  v1alpha1.PhaseStopped,
		machineapi.PhaseFailed:   v1alpha1.PhaseFailed,
		machineapi.PhaseLost:     v1alpha1.PhaseFailed,
		machineapi.PhaseUnknown:  v1alpha1.PhaseUnknown,
		"a later phase":          v1alpha1.PhaseUnknown,
	} {
		assert.Equal(t, want, phase(machine), "Machine phase %s", machine)
	}
}

func TestMachineSpec_MapsEveryField(t *testing.T) {
	spec := v1alpha1.Spec{
		Image:        "registry.example.test/w@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Resources:    v1alpha1.Resources{CPU: 2, MemoryBytes: 1 << 30, DiskBytes: 4 << 30},
		Env:          map[string]string{"A": "secret value"},
		Ports:        []uint16{8080, 7},
		Egress:       []v1alpha1.EgressRule{{Prefix: mustPrefix("192.0.2.0/24"), Protocol: v1alpha1.ProtocolTCP, Ports: []v1alpha1.PortRange{{First: 443, Last: 443}}}},
		FromSnapshot: "snap",
	}
	m := machineSpec(spec, 2<<30)
	assert.Equal(t, machineapi.Spec{
		Image: spec.Image, CPU: 2, MemoryBytes: 1 << 30, RootDiskBytes: 2 << 30,
		Volumes: []machineapi.Volume{{Name: "workspace", SizeBytes: 4 << 30, MountPath: "/workspace"}},
		Env:     map[string]string{"A": "secret value"},
		Services: []machineapi.Service{
			{Name: "p8080", Port: 8080, Protocol: machineapi.ServiceTCP},
			{Name: "p7", Port: 7, Protocol: machineapi.ServiceTCP},
		},
		NetworkPolicy: machineapi.NetworkPolicy{Egress: []machineapi.Rule{{Prefix: "192.0.2.0/24", Protocol: "tcp", Ports: []machineapi.PortRange{{First: 443, Last: 443}}}}},
		State:         machineapi.StateRunning,
		FromSnapshot:  &machineapi.SnapshotSource{Snapshot: "snap"},
	}, m)
	assert.NoError(t, m.Check(), "naru takes it")

	back := workspaceSpec(m.Redact())
	want := spec
	want.Env = map[string]string{"A": v1alpha1.Redacted}
	assert.True(t, v1alpha1.SameSpec(want, back), "a read maps back to the spec, its env redacted: %+v", back)
}
