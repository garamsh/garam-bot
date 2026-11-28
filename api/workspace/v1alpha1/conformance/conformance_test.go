package conformance_test

import (
	"testing"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/api/workspace/v1alpha1/conformance"
	"github.com/garamsh/garam-bot/api/workspace/v1alpha1/workspacefake"
)

// image is the digest reference the suite passes the reference implementation, which runs none.
const image = "registry.example/conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000"

var resources = v1alpha1.Resources{CPU: 1, MemoryBytes: 256 << 20, DiskBytes: 1 << 30}

// TestReference runs the suite against the reference implementation offering every capability, and
// again offering none, so the cases a capability gates run on each side.
func TestReference(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps v1alpha1.Capabilities
	}{
		{"every capability", v1alpha1.Capabilities{Snapshot: true, Egress: true, Stdin: true}},
		{"no capability", v1alpha1.Capabilities{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conformance.Run(t, conformance.Subject{Provider: workspacefake.New(tc.caps), Image: image, Resources: resources})
		})
	}
}
