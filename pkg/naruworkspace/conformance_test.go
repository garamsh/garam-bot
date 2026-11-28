package naruworkspace_test

import (
	"testing"
	"time"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/api/workspace/v1alpha1/conformance"
)

// image is the digest reference the suite passes; machineapifake runs no image.
const image = "registry.example.test/conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000"

// TestConformance_Machineapifake runs interface 3's suite against the provider on naru's
// machineapifake, registered with a one-time token, its Dial through a stand-in for naru-proxy.
func TestConformance_Machineapifake(t *testing.T) {
	f := newFleet(t)
	p := run(t, f.config(&memoryStore{}, nil))
	conformance.Run(t, conformance.Subject{
		Provider:  p,
		Image:     image,
		Resources: v1alpha1.Resources{CPU: 1, MemoryBytes: 256 << 20, DiskBytes: 1 << 30},
		Timeout:   30 * time.Second,
	})
}
