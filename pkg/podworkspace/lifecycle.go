package podworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

func isNotFound(err error) bool { return apierrors.IsNotFound(err) }

// Create makes the workspace: its claim, its network policy and its Pod (LIFE-1, LIFE-2).
func (p *Provider) Create(ctx context.Context, name v1alpha1.Name, spec v1alpha1.Spec) (v1alpha1.Workspace, bool, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if err := spec.Check(); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if err := checkLabels(spec); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	caps, err := p.Capabilities(ctx)
	if err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if len(spec.Egress) > 0 && !caps.Egress {
		return v1alpha1.Workspace{}, false, fmt.Errorf("%w: this cluster enforces no egress rules", v1alpha1.ErrUnsupported)
	}
	if spec.FromSnapshot != "" {
		if !caps.Snapshot {
			return v1alpha1.Workspace{}, false, fmt.Errorf("%w: this cluster serves no snapshots", v1alpha1.ErrUnsupported)
		}
		if err := p.checkRestore(ctx, spec); err != nil {
			return v1alpha1.Workspace{}, false, err
		}
	}

	claims := p.kube.CoreV1().PersistentVolumeClaims(p.cfg.Namespace)
	existing, err := claims.Get(ctx, string(name), metav1.GetOptions{})
	switch {
	case err == nil:
		return p.createAgain(ctx, name, spec, existing)
	case !isNotFound(err):
		return v1alpha1.Workspace{}, false, fmt.Errorf("get claim %s: %w", name, err)
	}

	claim, err := claimFor(name, spec, p.cfg.Namespace)
	if err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if _, err := claims.Create(ctx, claim, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return v1alpha1.Workspace{}, false, fmt.Errorf("%w: %s was made by another creator", v1alpha1.ErrConflict, name)
		}

		return v1alpha1.Workspace{}, false, fmt.Errorf("create claim %s: %w", name, err)
	}
	if err := p.ensureChildren(ctx, name, spec, true); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if err := p.refuseRootImage(ctx, name); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	ws, err := p.Get(ctx, name)

	return ws, err == nil, err
}

// refuseRootImage waits for the kubelet to start the workspace's container or to refuse it. The
// Pod must run as non-root (PodSecurity restricted), so an image that runs as root never starts:
// the kubelet says so in the container's waiting state, and Create answers it as ErrInvalid and
// removes what it made. A container still waiting for anything else when AdmitTimeout ends is
// not refused: its reason is the workspace's status.
func (p *Provider) refuseRootImage(ctx context.Context, name v1alpha1.Name) error {
	timeout, every := p.cfg.AdmitTimeout, p.pollEvery
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if every <= 0 {
		every = 250 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		pod, err := p.kube.CoreV1().Pods(p.cfg.Namespace).Get(ctx, string(name), metav1.GetOptions{})
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("get pod %s: %w", name, err)
		}
		if err == nil {
			if workspaceRunning(pod) {
				return nil
			}
			if reason := rootImageReason(pod); reason != "" {
				if err := p.Delete(ctx, name); err != nil {
					return fmt.Errorf("remove refused workspace %s: %w", name, err)
				}

				return fmt.Errorf("%w: the image cannot run as non-root, which every workspace must: %s", v1alpha1.ErrInvalid, reason)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-time.After(every):
		}
	}
}

// rootImageReason is the kubelet's refusal of a workspace container whose image runs as root (or
// names a user it cannot check), or "".
func rootImageReason(pod *corev1.Pod) string {
	for _, c := range pod.Status.ContainerStatuses {
		if w := c.State.Waiting; c.Name == containerName && w != nil && w.Reason == "CreateContainerConfigError" && strings.Contains(w.Message, "runAsNonRoot") {
			return w.Message
		}
	}

	return ""
}

// createAgain answers a Create of a name that has a claim: the same spec is the same workspace,
// whose missing Pod or policy a failed earlier Create left behind and this one makes.
func (p *Provider) createAgain(ctx context.Context, name v1alpha1.Name, spec v1alpha1.Spec, claim *corev1.PersistentVolumeClaim) (v1alpha1.Workspace, bool, error) {
	if claim.Labels[labelWorkspace] != string(name) {
		return v1alpha1.Workspace{}, false, fmt.Errorf("%w: %s was made by another creator", v1alpha1.ErrConflict, name)
	}
	if claim.DeletionTimestamp != nil {
		return v1alpha1.Workspace{}, false, fmt.Errorf("%w: %s", v1alpha1.ErrDeleting, name)
	}
	held, err := specOf(claim)
	if err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if !v1alpha1.SameSpec(held, spec) {
		return v1alpha1.Workspace{}, false, fmt.Errorf("%w: %s holds another spec", v1alpha1.ErrConflict, name)
	}
	if err := p.ensureChildren(ctx, name, held, claim.Annotations[annotationDesired] == desiredRunning); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	ws, err := p.Get(ctx, name)

	return ws, false, err
}

// ensureChildren makes the workspace's network policy and, when pod is set, its Pod, each unless
// it exists.
func (p *Provider) ensureChildren(ctx context.Context, name v1alpha1.Name, spec v1alpha1.Spec, pod bool) error {
	_, err := p.kube.NetworkingV1().NetworkPolicies(p.cfg.Namespace).Create(ctx, policyFor(name, spec, p.cfg.Namespace), metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create network policy %s: %w", name, err)
	}
	if !pod {
		return nil
	}

	return p.ensurePod(ctx, name, spec)
}

func (p *Provider) ensurePod(ctx context.Context, name v1alpha1.Name, spec v1alpha1.Spec) error {
	pods := p.kube.CoreV1().Pods(p.cfg.Namespace)
	for {
		_, err := pods.Create(ctx, podFor(name, spec, p.cfg.Namespace, p.cfg.HelperImage), metav1.CreateOptions{})
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create pod %s: %w", name, err)
		}
		// A Pod that is being deleted holds the name until it is gone: wait for it.
		existing, err := pods.Get(ctx, string(name), metav1.GetOptions{})
		switch {
		case isNotFound(err):
			continue
		case err != nil:
			return fmt.Errorf("get pod %s: %w", name, err)
		case existing.DeletionTimestamp == nil:
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for pod %s to go: %w", name, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// record returns the claim of the workspace name and the spec it holds, or ErrNotFound.
func (p *Provider) record(ctx context.Context, name v1alpha1.Name) (*corev1.PersistentVolumeClaim, v1alpha1.Spec, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return nil, v1alpha1.Spec{}, err
	}
	claim, err := p.kube.CoreV1().PersistentVolumeClaims(p.cfg.Namespace).Get(ctx, string(name), metav1.GetOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, v1alpha1.Spec{}, fmt.Errorf("%w: workspace %s", v1alpha1.ErrNotFound, name)
		}

		return nil, v1alpha1.Spec{}, fmt.Errorf("get claim %s: %w", name, err)
	}
	if claim.Labels[labelWorkspace] != string(name) {
		return nil, v1alpha1.Spec{}, fmt.Errorf("%w: workspace %s", v1alpha1.ErrNotFound, name)
	}
	spec, err := specOf(claim)

	return claim, spec, err
}

// Get returns the workspace name (LIFE-2).
func (p *Provider) Get(ctx context.Context, name v1alpha1.Name) (v1alpha1.Workspace, error) {
	claim, spec, err := p.record(ctx, name)
	if err != nil {
		return v1alpha1.Workspace{}, err
	}
	pod, err := p.kube.CoreV1().Pods(p.cfg.Namespace).Get(ctx, string(name), metav1.GetOptions{})
	if err != nil && !isNotFound(err) {
		return v1alpha1.Workspace{}, fmt.Errorf("get pod %s: %w", name, err)
	}
	if isNotFound(err) {
		pod = nil
	}

	return workspaceOf(name, spec, claim, pod), nil
}

// List returns every workspace in the namespace, ordered by name (LIFE-2).
func (p *Provider) List(ctx context.Context) ([]v1alpha1.Workspace, error) {
	opts := metav1.ListOptions{LabelSelector: labelWorkspace}
	claims, err := p.kube.CoreV1().PersistentVolumeClaims(p.cfg.Namespace).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list claims: %w", err)
	}
	pods, err := p.kube.CoreV1().Pods(p.cfg.Namespace).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	podOf := make(map[string]*corev1.Pod, len(pods.Items))
	for i := range pods.Items {
		podOf[pods.Items[i].Name] = &pods.Items[i]
	}
	list := make([]v1alpha1.Workspace, 0, len(claims.Items))
	for i := range claims.Items {
		claim := &claims.Items[i]
		name := v1alpha1.Name(claim.Name)
		if claim.Labels[labelWorkspace] != claim.Name {
			continue
		}
		spec, err := specOf(claim)
		if err != nil {
			return nil, err
		}
		list = append(list, workspaceOf(name, spec, claim, podOf[claim.Name]))
	}
	slices.SortFunc(list, func(a, b v1alpha1.Workspace) int { return strings.Compare(string(a.Name), string(b.Name)) })

	return list, nil
}

// workspaceOf reads the phase of a workspace from its claim and its Pod, which is nil when it has
// none, and answers its env as Redacted.
func workspaceOf(name v1alpha1.Name, spec v1alpha1.Spec, claim *corev1.PersistentVolumeClaim, pod *corev1.Pod) v1alpha1.Workspace {
	redacted := make(map[string]string, len(spec.Env))
	for k := range spec.Env {
		redacted[k] = v1alpha1.Redacted
	}
	if len(spec.Env) == 0 {
		redacted = nil
	}
	spec.Env = redacted

	return v1alpha1.Workspace{Name: name, Spec: spec, Status: statusOf(claim, pod)}
}

func statusOf(claim *corev1.PersistentVolumeClaim, pod *corev1.Pod) v1alpha1.Status {
	switch {
	case claim.DeletionTimestamp != nil:
		return v1alpha1.Status{Phase: v1alpha1.PhaseStopping, Reason: "being deleted"}
	case pod == nil && claim.Annotations[annotationDesired] == desiredStopped:
		return v1alpha1.Status{Phase: v1alpha1.PhaseStopped}
	case pod == nil:
		return v1alpha1.Status{Phase: v1alpha1.PhaseUnknown, Reason: "the pod is gone, though the workspace is meant to run"}
	case pod.DeletionTimestamp != nil:
		return v1alpha1.Status{Phase: v1alpha1.PhaseStopping, Reason: "the pod is ending"}
	}
	switch pod.Status.Phase {
	case corev1.PodRunning:
		if workspaceRunning(pod) {
			return v1alpha1.Status{Phase: v1alpha1.PhaseRunning}
		}

		return v1alpha1.Status{Phase: v1alpha1.PhasePending, Reason: waitingReason(pod)}
	case corev1.PodFailed, corev1.PodSucceeded:
		return v1alpha1.Status{Phase: v1alpha1.PhaseFailed, Reason: strings.TrimSpace(pod.Status.Reason + " " + pod.Status.Message)}
	case corev1.PodPending:
		return v1alpha1.Status{Phase: v1alpha1.PhasePending, Reason: waitingReason(pod)}
	}

	return v1alpha1.Status{Phase: v1alpha1.PhaseUnknown, Reason: fmt.Sprintf("pod phase %q", pod.Status.Phase)}
}

func workspaceRunning(pod *corev1.Pod) bool {
	for _, c := range pod.Status.ContainerStatuses {
		if c.Name == containerName {
			return c.State.Running != nil
		}
	}

	return false
}

// waitingReason says what a Pod that is not running waits for.
func waitingReason(pod *corev1.Pod) string {
	statuses := append(slices.Clone(pod.Status.InitContainerStatuses), pod.Status.ContainerStatuses...)
	for _, c := range statuses {
		if w := c.State.Waiting; w != nil && w.Reason != "" {
			return strings.TrimSpace(w.Reason + " " + w.Message)
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return strings.TrimSpace(c.Reason + " " + c.Message)
		}
	}

	return "starting"
}

// Start sets the workspace to run and makes its Pod when it has none (LIFE-3).
func (p *Provider) Start(ctx context.Context, name v1alpha1.Name) (v1alpha1.Workspace, error) {
	claim, spec, err := p.record(ctx, name)
	if err != nil {
		return v1alpha1.Workspace{}, err
	}
	if claim.DeletionTimestamp != nil {
		return v1alpha1.Workspace{}, fmt.Errorf("%w: %s", v1alpha1.ErrDeleting, name)
	}
	if err := p.setDesired(ctx, name, desiredRunning); err != nil {
		return v1alpha1.Workspace{}, err
	}
	if err := p.ensurePod(ctx, name, spec); err != nil {
		return v1alpha1.Workspace{}, err
	}

	return p.Get(ctx, name)
}

// Stop sets the workspace to stop and ends its Pod after grace, and keeps its claim (LIFE-3).
func (p *Provider) Stop(ctx context.Context, name v1alpha1.Name, grace time.Duration) (v1alpha1.Workspace, error) {
	if _, _, err := p.record(ctx, name); err != nil {
		return v1alpha1.Workspace{}, err
	}
	if err := p.setDesired(ctx, name, desiredStopped); err != nil {
		return v1alpha1.Workspace{}, err
	}
	err := p.kube.CoreV1().Pods(p.cfg.Namespace).Delete(ctx, string(name), metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(grace.Seconds()))})
	if err != nil && !isNotFound(err) {
		return v1alpha1.Workspace{}, fmt.Errorf("delete pod %s: %w", name, err)
	}

	return p.Get(ctx, name)
}

func (p *Provider) setDesired(ctx context.Context, name v1alpha1.Name, desired string) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{annotationDesired: desired}}})
	if err != nil {
		return fmt.Errorf("encode patch: %w", err)
	}
	_, err = p.kube.CoreV1().PersistentVolumeClaims(p.cfg.Namespace).Patch(ctx, string(name), types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("record desired state of %s: %w", name, err)
	}

	return nil
}

// Delete deletes the workspace and its data volume, and keeps its snapshots (LIFE-4).
func (p *Provider) Delete(ctx context.Context, name v1alpha1.Name) error {
	if _, _, err := p.record(ctx, name); err != nil {
		if errors.Is(err, v1alpha1.ErrNotFound) {
			return nil
		}

		return err
	}
	if err := p.kube.NetworkingV1().NetworkPolicies(p.cfg.Namespace).Delete(ctx, string(name), metav1.DeleteOptions{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("delete network policy %s: %w", name, err)
	}
	if err := p.kube.CoreV1().Pods(p.cfg.Namespace).Delete(ctx, string(name), metav1.DeleteOptions{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("delete pod %s: %w", name, err)
	}
	if err := p.kube.CoreV1().PersistentVolumeClaims(p.cfg.Namespace).Delete(ctx, string(name), metav1.DeleteOptions{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("delete claim %s: %w", name, err)
	}

	return nil
}
