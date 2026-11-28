//go:build conformance

package podworkspace_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/api/workspace/v1alpha1/conformance"
	"github.com/garamsh/garam-bot/pkg/podworkspace"
)

// TestConformance runs interface 3's suite against a real cluster, as the namespaced ServiceAccount
// `make test-podworkspace` makes (testdata/rbac.yaml). The environment names the cluster and the
// two images, which the target builds and pushes by digest.
func TestConformance(t *testing.T) {
	kubeconfig := os.Getenv("POD_WORKSPACE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("POD_WORKSPACE_KUBECONFIG is not set: run make test-podworkspace")
	}
	rest, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err)

	provider, err := podworkspace.New(podworkspace.Config{
		Rest:        rest,
		Namespace:   os.Getenv("POD_WORKSPACE_NAMESPACE"),
		HelperImage: os.Getenv("POD_WORKSPACE_HELPER_IMAGE"),
		Egress:      os.Getenv("POD_WORKSPACE_EGRESS") == "true",
	})
	require.NoError(t, err)

	conformance.Run(t, conformance.Subject{
		Provider:  provider,
		Image:     os.Getenv("POD_WORKSPACE_CONFORMANCE_IMAGE"),
		Resources: v1alpha1.Resources{CPU: 1, MemoryBytes: 64 << 20, DiskBytes: 64 << 20},
		Timeout:   3 * time.Minute,
	})
}
