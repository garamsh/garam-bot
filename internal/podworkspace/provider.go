package podworkspace

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// Config is what the place decides about its cluster.
type Config struct {
	// Rest reaches the cluster. The provider holds namespaced rights only, in Namespace (see
	// docs/architecture/interfaces.md §Current decisions for its Role).
	Rest *rest.Config
	// Namespace is the one namespace the provider's workspaces live in: the caller's own, so a
	// workspace of another caller is another namespace's.
	Namespace string
	// HelperImage is the workspace file helper's image, repository@sha256:<digest>
	// (build/workspacefile.Dockerfile). A workspace's image needs no shell and no cat.
	HelperImage string
	// Egress is whether the cluster's network plugin enforces NetworkPolicy. The Kubernetes API
	// cannot say, so the place does, and Capabilities.Egress reports it (NET-1).
	Egress bool
	// SnapshotClass is the VolumeSnapshotClass a snapshot names. Empty, the cluster's default for
	// the volume's driver applies.
	SnapshotClass string
	// AdmitTimeout bounds how long Create waits for the kubelet to start the workspace's
	// container, which is when a root image is refused. Zero is 30 seconds.
	AdmitTimeout time.Duration
}

var helperImagePattern = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)

// Provider implements v1alpha1.Provider over the Kubernetes API.
type Provider struct {
	cfg       Config
	kube      kubernetes.Interface
	dynamic   dynamic.Interface
	discovery discovery.DiscoveryInterface
	guest     guest
	// pollEvery is how often Create looks at the Pod it made; zero is 250 milliseconds.
	pollEvery time.Duration
}

var _ v1alpha1.Provider = (*Provider)(nil)

// New returns a Provider for the cluster cfg.Rest reaches.
func New(cfg Config) (*Provider, error) {
	if cfg.Rest == nil || cfg.Namespace == "" {
		return nil, errors.New("podworkspace: a rest config and a namespace are required")
	}
	if !helperImagePattern.MatchString(cfg.HelperImage) {
		return nil, fmt.Errorf("podworkspace: helper image %q is not repository@sha256:<digest>", cfg.HelperImage)
	}
	kube, err := kubernetes.NewForConfig(cfg.Rest)
	if err != nil {
		return nil, fmt.Errorf("podworkspace: kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg.Rest)
	if err != nil {
		return nil, fmt.Errorf("podworkspace: dynamic client: %w", err)
	}

	return &Provider{cfg: cfg, kube: kube, dynamic: dyn, discovery: kube.Discovery(), guest: &clusterGuest{rest: cfg.Rest, kube: kube, namespace: cfg.Namespace}}, nil
}

// Capabilities reports egress as the place configured it and snapshots as the cluster serves
// them: the VolumeSnapshot API is installed (SNAP-3).
func (p *Provider) Capabilities(context.Context) (v1alpha1.Capabilities, error) {
	snapshot, err := p.snapshotsServed()
	if err != nil {
		return v1alpha1.Capabilities{}, err
	}

	return v1alpha1.Capabilities{Snapshot: snapshot, Egress: p.cfg.Egress, Stdin: true, Labels: true}, nil
}

func (p *Provider) snapshotsServed() (bool, error) {
	list, err := p.discovery.ServerResourcesForGroupVersion(snapshotGroupVersion)
	if err != nil {
		if meta.IsNoMatchError(err) || isNotFound(err) {
			return false, nil
		}

		return false, fmt.Errorf("discover %s: %w", snapshotGroupVersion, err)
	}
	for _, r := range list.APIResources {
		if r.Name == "volumesnapshots" {
			return true, nil
		}
	}

	return false, nil
}
