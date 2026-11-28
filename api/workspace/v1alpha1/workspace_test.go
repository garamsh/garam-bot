package v1alpha1_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

const digest = "registry.example/w@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func valid() v1alpha1.Spec {
	return v1alpha1.Spec{
		Image:     digest,
		Resources: v1alpha1.Resources{CPU: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 30},
		Ports:     []uint16{7700},
		Egress:    []v1alpha1.EgressRule{{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Protocol: v1alpha1.ProtocolTCP, Ports: []v1alpha1.PortRange{{First: 443, Last: 443}}}},
		Labels:    map[string]string{"bot.garam.sh/bot-uid": "6d3c0f1e-1b2a-4c5d-8e9f-0a1b2c3d4e5f", "attachment": "main", "empty": ""},
	}
}

func TestParseName_NAME1(t *testing.T) {
	for _, ok := range []string{"a", "w-1", "0123456789012345678901234567890123456789012345678901234567890ab"} {
		_, err := v1alpha1.ParseName(ok)
		assert.NoError(t, err, ok)
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a.b", "0123456789012345678901234567890123456789012345678901234567890abc"} {
		_, err := v1alpha1.ParseName(bad)
		assert.ErrorIs(t, err, v1alpha1.ErrInvalid, bad)
	}
}

func TestSpecCheck_SPEC1(t *testing.T) {
	require.NoError(t, valid().Check()) // control
	for name, change := range map[string]func(*v1alpha1.Spec){
		"an image by tag":           func(s *v1alpha1.Spec) { s.Image = "registry.example/w:latest" },
		"no cpu":                    func(s *v1alpha1.Spec) { s.Resources.CPU = 0 },
		"no memory":                 func(s *v1alpha1.Spec) { s.Resources.MemoryBytes = 0 },
		"no disk":                   func(s *v1alpha1.Spec) { s.Resources.DiskBytes = 0 },
		"port 0":                    func(s *v1alpha1.Spec) { s.Ports = []uint16{0} },
		"a snapshot name not label": func(s *v1alpha1.Spec) { s.FromSnapshot = "Snap" },
		"an egress rule without prefix": func(s *v1alpha1.Spec) {
			s.Egress = []v1alpha1.EgressRule{{Protocol: v1alpha1.ProtocolTCP}}
		},
		"an egress protocol of icmp": func(s *v1alpha1.Spec) { s.Egress[0].Protocol = "icmp" },
		"a prefix not canonical":     func(s *v1alpha1.Spec) { s.Egress[0].Prefix = netip.MustParsePrefix("10.0.0.1/8") },
		"a repeated port":            func(s *v1alpha1.Spec) { s.Ports = []uint16{7700, 7700} },
		"an empty env name":          func(s *v1alpha1.Spec) { s.Env = map[string]string{"": "v"} },
		"an env name holding =":      func(s *v1alpha1.Spec) { s.Env = map[string]string{"A=B": "v"} },
		"an egress range reversed":   func(s *v1alpha1.Spec) { s.Egress[0].Ports = []v1alpha1.PortRange{{First: 9, Last: 8}} },
		"an empty label key":         func(s *v1alpha1.Spec) { s.Labels = map[string]string{"": "v"} },
		"a label key of two slashes": func(s *v1alpha1.Spec) { s.Labels = map[string]string{"a/b/c": "v"} },
		"a label prefix not DNS":     func(s *v1alpha1.Spec) { s.Labels = map[string]string{"Bot.example/k": "v"} },
		"a label name of 64":         func(s *v1alpha1.Spec) { s.Labels = map[string]string{strings.Repeat("k", 64): "v"} },
		"a label value with a slash": func(s *v1alpha1.Spec) { s.Labels = map[string]string{"k": "a/b"} },
		"a label value of 64":        func(s *v1alpha1.Spec) { s.Labels = map[string]string{"k": strings.Repeat("v", 64)} },
	} {
		spec := valid()
		change(&spec)
		assert.ErrorIs(t, spec.Check(), v1alpha1.ErrInvalid, name)
	}
}

func TestSameSpec_LIFE1(t *testing.T) {
	assert.True(t, v1alpha1.SameSpec(valid(), valid()))
	for name, change := range map[string]func(*v1alpha1.Spec){
		"another image":     func(s *v1alpha1.Spec) { s.Image = digest[:len(digest)-1] + "0" },
		"another env":       func(s *v1alpha1.Spec) { s.Env = map[string]string{"A": "b"} },
		"another port":      func(s *v1alpha1.Spec) { s.Ports = []uint16{7701} },
		"another rule port": func(s *v1alpha1.Spec) { s.Egress[0].Ports = nil },
		"from a snapshot":   func(s *v1alpha1.Spec) { s.FromSnapshot = "s" },
		"more memory":       func(s *v1alpha1.Spec) { s.Resources.MemoryBytes++ },
		"another label":     func(s *v1alpha1.Spec) { s.Labels = map[string]string{"attachment": "other"} },
	} {
		spec := valid()
		change(&spec)
		assert.False(t, v1alpha1.SameSpec(valid(), spec), name)
	}
}

func TestCheckPath_FILE1(t *testing.T) {
	for _, ok := range []string{"/workspace/a", "/run/garam-bot/agent/token", "/procfs"} {
		assert.NoError(t, v1alpha1.CheckPath(ok), ok)
	}
	for _, bad := range []string{"", "a", "/a/../b", "/a/", "/", "/proc", "/proc/1", "/sys/x", "/dev/null"} {
		assert.ErrorIs(t, v1alpha1.CheckPath(bad), v1alpha1.ErrInvalid, bad)
	}
}
