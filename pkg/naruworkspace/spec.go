package naruworkspace

import (
	"errors"
	"maps"
	"net/netip"
	"strconv"

	"github.com/garamsh/naru/lib/machineapi"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// volumeName is the one volume of a workspace's Machine, mounted at v1alpha1.DataPath.
const volumeName = "workspace"

// machineSpec is the Machine spec a workspace spec maps to (README §Mapping onto naru's Machine
// API): every field as written, so naru's create comparison is SameSpec's.
func machineSpec(spec v1alpha1.Spec, rootDiskBytes uint64) machineapi.Spec {
	m := machineapi.Spec{
		Image:         spec.Image,
		CPU:           spec.Resources.CPU,
		MemoryBytes:   spec.Resources.MemoryBytes,
		RootDiskBytes: rootDiskBytes,
		Volumes:       []machineapi.Volume{{Name: volumeName, SizeBytes: spec.Resources.DiskBytes, MountPath: v1alpha1.DataPath}},
		Env:           maps.Clone(spec.Env),
		State:         machineapi.StateRunning,
	}
	for _, port := range spec.Ports {
		m.Services = append(m.Services, machineapi.Service{Name: serviceName(port), Port: port, Protocol: machineapi.ServiceTCP})
	}
	for _, r := range spec.Egress {
		rule := machineapi.Rule{Prefix: r.Prefix.String(), Protocol: string(r.Protocol)}
		for _, p := range r.Ports {
			rule.Ports = append(rule.Ports, machineapi.PortRange{First: p.First, Last: p.Last})
		}
		m.NetworkPolicy.Egress = append(m.NetworkPolicy.Egress, rule)
	}
	if spec.FromSnapshot != "" {
		m.FromSnapshot = &machineapi.SnapshotSource{Snapshot: string(spec.FromSnapshot)}
	}

	return m
}

// workspaceSpec is the workspace spec a Machine's spec, as a read answers it, maps back to. naru
// answers each env value as Redacted, which is interface 3's Redacted too (LIFE-2).
func workspaceSpec(m machineapi.Spec) v1alpha1.Spec {
	spec := v1alpha1.Spec{
		Image:     m.Image,
		Resources: v1alpha1.Resources{CPU: m.CPU, MemoryBytes: m.MemoryBytes, DiskBytes: diskBytes(m)},
	}
	if len(m.Env) > 0 {
		spec.Env = make(map[string]string, len(m.Env))
		for name := range m.Env {
			spec.Env[name] = v1alpha1.Redacted
		}
	}
	for _, s := range m.Services {
		spec.Ports = append(spec.Ports, s.Port)
	}
	for _, r := range m.NetworkPolicy.Egress {
		// naru takes only a prefix in canonical form, so one it answers parses.
		prefix, _ := netip.ParsePrefix(r.Prefix)
		rule := v1alpha1.EgressRule{Prefix: prefix, Protocol: v1alpha1.Protocol(r.Protocol)}
		for _, p := range r.Ports {
			rule.Ports = append(rule.Ports, v1alpha1.PortRange{First: p.First, Last: p.Last})
		}
		spec.Egress = append(spec.Egress, rule)
	}
	if m.FromSnapshot != nil {
		spec.FromSnapshot = v1alpha1.Name(m.FromSnapshot.Snapshot)
	}

	return spec
}

// diskBytes is the size of the workspace volume of m, the one volume a workspace's Machine has.
func diskBytes(m machineapi.Spec) uint64 {
	for _, v := range m.Volumes {
		if v.Name == volumeName {
			return v.SizeBytes
		}
	}

	return 0
}

// serviceName is the Machine service a workspace port is: p<port>.
func serviceName(port uint16) string { return "p" + strconv.Itoa(int(port)) }

// serviceHost is the name naru-proxy routes a Machine's service by, the SNI Dial sends: the
// service in front of the Machine's stable name, <service>--<id>.<zone> (naru ADR 0112 item 1).
func serviceHost(address machineapi.Address, port uint16) (string, error) {
	if address.Name == "" {
		return "", errors.New("the Machine has no stable name yet")
	}

	return serviceName(port) + "--" + address.Name, nil
}

// phase is the workspace phase a Machine phase maps to (README §Mapping onto naru's Machine API).
func phase(p machineapi.Phase) v1alpha1.Phase {
	switch p {
	case machineapi.PhasePending, machineapi.PhaseStarting, machineapi.PhaseMoving:
		return v1alpha1.PhasePending
	case machineapi.PhaseRunning:
		return v1alpha1.PhaseRunning
	case machineapi.PhaseStopping, machineapi.PhaseDeleting:
		return v1alpha1.PhaseStopping
	case machineapi.PhaseStopped:
		return v1alpha1.PhaseStopped
	case machineapi.PhaseFailed, machineapi.PhaseLost:
		return v1alpha1.PhaseFailed
	}

	return v1alpha1.PhaseUnknown
}

// snapshotPhase is the workspace snapshot phase a Machine snapshot phase maps to, one to one.
func snapshotPhase(p machineapi.SnapshotPhase) v1alpha1.SnapshotPhase {
	switch p {
	case machineapi.SnapshotPending:
		return v1alpha1.SnapshotPending
	case machineapi.SnapshotReady:
		return v1alpha1.SnapshotReady
	case machineapi.SnapshotFailed:
		return v1alpha1.SnapshotFailed
	}

	return v1alpha1.SnapshotUnknown
}
