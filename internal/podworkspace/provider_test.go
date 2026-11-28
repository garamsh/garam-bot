package podworkspace

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

const (
	testNamespace = "workspaces"
	testHelper    = "registry.example/helper@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	testImage     = "registry.example/image@sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

// recordingGuest answers what the Pod would and records what it was asked.
type recordingGuest struct {
	result   v1alpha1.ExecResult
	commands [][]string
	stdin    [][]byte
	deadline bool
	dialed   []uint16
}

func (g *recordingGuest) exec(ctx context.Context, _ string, command []string, stdin []byte) (v1alpha1.ExecResult, error) {
	_, g.deadline = ctx.Deadline()
	g.commands = append(g.commands, command)
	g.stdin = append(g.stdin, stdin)

	return g.result, nil
}

func (g *recordingGuest) dial(_ context.Context, _ string, port uint16) (net.Conn, error) {
	g.dialed = append(g.dialed, port)
	client, server := net.Pipe()
	_ = server.Close()

	return client, nil
}

type fixture struct {
	provider *Provider
	kube     *fake.Clientset
	guest    *recordingGuest
}

func newFixture(t *testing.T, egress, snapshots bool) *fixture {
	t.Helper()
	kube := fake.NewClientset()
	discovery, ok := kube.Discovery().(*fakediscovery.FakeDiscovery)
	require.True(t, ok)
	if snapshots {
		discovery.Resources = []*metav1.APIResourceList{{GroupVersion: snapshotGroupVersion, APIResources: []metav1.APIResource{{Name: "volumesnapshots"}}}}
	}
	guest := &recordingGuest{}

	return &fixture{
		provider: &Provider{
			cfg:       Config{Namespace: testNamespace, HelperImage: testHelper, Egress: egress, AdmitTimeout: 20 * time.Millisecond},
			kube:      kube,
			dynamic:   dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
			discovery: discovery,
			guest:     guest,
			pollEvery: time.Millisecond,
		},
		kube:  kube,
		guest: guest,
	}
}

func testSpec() v1alpha1.Spec {
	return v1alpha1.Spec{
		Image:     testImage,
		Resources: v1alpha1.Resources{CPU: 1, MemoryBytes: 64 << 20, DiskBytes: 1 << 30},
		Env:       map[string]string{"TOKEN": "hunter2"},
		Ports:     []uint16{7},
	}
}

// run marks the workspace's Pod as the kubelet would once its container runs.
func (f *fixture) run(t *testing.T, name v1alpha1.Name) {
	t.Helper()
	pod, err := f.kube.CoreV1().Pods(testNamespace).Get(t.Context(), string(name), metav1.GetOptions{})
	require.NoError(t, err)
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: containerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	_, err = f.kube.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)
}

func TestCreateMakesClaimPolicyAndPodAndIsIdempotent(t *testing.T) {
	f := newFixture(t, false, false)
	ctx := t.Context()

	ws, created, err := f.provider.Create(ctx, "one", testSpec())
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, map[string]string{"TOKEN": v1alpha1.Redacted}, ws.Spec.Env, "a read never returns an env value")

	_, err = f.kube.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, "one", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = f.kube.CoreV1().Pods(testNamespace).Get(ctx, "one", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = f.kube.NetworkingV1().NetworkPolicies(testNamespace).Get(ctx, "one", metav1.GetOptions{})
	require.NoError(t, err)

	_, created, err = f.provider.Create(ctx, "one", testSpec())
	require.NoError(t, err)
	assert.False(t, created, "the same spec is the same workspace")

	other := testSpec()
	other.Resources.CPU = 2
	_, _, err = f.provider.Create(ctx, "one", other)
	assert.ErrorIs(t, err, v1alpha1.ErrConflict)
}

func TestCreateRefusesWhatTheClusterCannotDo(t *testing.T) {
	f := newFixture(t, false, false)

	withEgress := testSpec()
	withEgress.Egress = []v1alpha1.EgressRule{{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Protocol: v1alpha1.ProtocolTCP}}
	_, _, err := f.provider.Create(t.Context(), "one", withEgress)
	assert.ErrorIs(t, err, v1alpha1.ErrUnsupported, "egress rules without an enforcing network plugin")

	restore := testSpec()
	restore.FromSnapshot = "snap"
	_, _, err = f.provider.Create(t.Context(), "two", restore)
	assert.ErrorIs(t, err, v1alpha1.ErrUnsupported, "a restore on a cluster that serves no snapshots")
}

func TestCapabilitiesFollowTheClusterAndTheConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name              string
		egress, snapshots bool
	}{{"neither", false, false}, {"egress", true, false}, {"snapshots", false, true}, {"both", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			caps, err := newFixture(t, tc.egress, tc.snapshots).provider.Capabilities(t.Context())
			require.NoError(t, err)
			assert.Equal(t, v1alpha1.Capabilities{Egress: tc.egress, Snapshot: tc.snapshots}, caps)
		})
	}
}

func TestStopEndsThePodAndKeepsTheClaim(t *testing.T) {
	f := newFixture(t, false, false)
	ctx := t.Context()
	_, _, err := f.provider.Create(ctx, "one", testSpec())
	require.NoError(t, err)
	f.run(t, "one")

	ws, err := f.provider.Stop(ctx, "one", 0)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.PhaseStopped, ws.Status.Phase)
	_, err = f.kube.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, "one", metav1.GetOptions{})
	require.NoError(t, err, "the data volume outlives the Pod (DATA-1)")

	ws, err = f.provider.Start(ctx, "one")
	require.NoError(t, err)
	_, err = f.kube.CoreV1().Pods(testNamespace).Get(ctx, "one", metav1.GetOptions{})
	require.NoError(t, err, "start makes the Pod again")
	assert.NotEqual(t, v1alpha1.PhaseStopped, ws.Status.Phase)
}

func TestDeleteRemovesEverythingAndIsIdempotent(t *testing.T) {
	f := newFixture(t, false, false)
	ctx := t.Context()
	_, _, err := f.provider.Create(ctx, "one", testSpec())
	require.NoError(t, err)

	require.NoError(t, f.provider.Delete(ctx, "one"))
	require.NoError(t, f.provider.Delete(ctx, "one"))
	_, err = f.provider.Get(ctx, "one")
	assert.ErrorIs(t, err, v1alpha1.ErrNotFound)
	_, err = f.kube.NetworkingV1().NetworkPolicies(testNamespace).Get(ctx, "one", metav1.GetOptions{})
	assert.Error(t, err, "the policy goes with the workspace")
}

func TestListAnswersOnlyWorkspacesOrderedByName(t *testing.T) {
	f := newFixture(t, false, false)
	ctx := t.Context()
	for _, name := range []v1alpha1.Name{"b", "a"} {
		_, _, err := f.provider.Create(ctx, name, testSpec())
		require.NoError(t, err)
	}
	stray := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "stray", Namespace: testNamespace, Labels: map[string]string{labelWorkspace: "another"}}}
	_, err := f.kube.CoreV1().PersistentVolumeClaims(testNamespace).Create(ctx, stray, metav1.CreateOptions{})
	require.NoError(t, err)

	list, err := f.provider.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, []v1alpha1.Name{"a", "b"}, []v1alpha1.Name{list[0].Name, list[1].Name})
}

func TestGuestCallsNeedARunningWorkspace(t *testing.T) {
	f := newFixture(t, false, false)
	ctx := t.Context()
	_, _, err := f.provider.Create(ctx, "one", testSpec())
	require.NoError(t, err)

	_, err = f.provider.Exec(ctx, "one", v1alpha1.ExecRequest{Command: []string{"true"}})
	assert.ErrorIs(t, err, v1alpha1.ErrNotRunning, "a Pod that is not running yet")
	_, err = f.provider.ReadFile(ctx, "one", "/workspace/a")
	assert.ErrorIs(t, err, v1alpha1.ErrNotRunning)
	assert.ErrorIs(t, f.provider.WriteFile(ctx, "one", "/workspace/a", nil), v1alpha1.ErrNotRunning)
	_, err = f.provider.Dial(ctx, "one", 7)
	assert.ErrorIs(t, err, v1alpha1.ErrNotRunning)
	assert.Empty(t, f.guest.commands, "nothing reached the Pod")
}

func TestFileCallsGoThroughTheHelperAndNeverAShell(t *testing.T) {
	f := newFixture(t, false, false)
	ctx := t.Context()
	_, _, err := f.provider.Create(ctx, "one", testSpec())
	require.NoError(t, err)
	f.run(t, "one")

	require.NoError(t, f.provider.WriteFile(ctx, "one", "/workspace/a", []byte("body")))
	f.guest.result = v1alpha1.ExecResult{Stdout: []byte("body")}
	got, err := f.provider.ReadFile(ctx, "one", "/workspace/a")
	require.NoError(t, err)
	assert.Equal(t, []byte("body"), got)

	assert.Equal(t, [][]string{{helperPath, "write", "/workspace/a"}, {helperPath, "read", "/workspace/a"}}, f.guest.commands)
	assert.Equal(t, []byte("body"), f.guest.stdin[0], "the content goes on stdin, not into a command line")

	f.guest.result = v1alpha1.ExecResult{ExitCode: exitFileNotFound}
	_, err = f.provider.ReadFile(ctx, "one", "/workspace/missing")
	assert.ErrorIs(t, err, v1alpha1.ErrFileNotFound)

	f.guest.result = v1alpha1.ExecResult{ExitCode: 1, Stderr: []byte("disk full")}
	err = f.provider.WriteFile(ctx, "one", "/workspace/a", nil)
	require.Error(t, err)
	assert.False(t, errors.Is(err, v1alpha1.ErrFileNotFound))
	assert.Contains(t, err.Error(), "disk full")

	_, err = f.provider.ReadFile(ctx, "one", "/proc/self/environ")
	assert.ErrorIs(t, err, v1alpha1.ErrInvalid, "a system directory is refused before the Pod is reached")
}

func TestExecBoundsTheCommandByItsTimeout(t *testing.T) {
	f := newFixture(t, false, false)
	ctx := t.Context()
	_, _, err := f.provider.Create(ctx, "one", testSpec())
	require.NoError(t, err)
	f.run(t, "one")

	_, err = f.provider.Exec(ctx, "one", v1alpha1.ExecRequest{Command: []string{"true"}})
	require.NoError(t, err)
	assert.False(t, f.guest.deadline)
	_, err = f.provider.Exec(ctx, "one", v1alpha1.ExecRequest{Command: []string{"true"}, Timeout: time.Minute})
	require.NoError(t, err)
	assert.True(t, f.guest.deadline)
	_, err = f.provider.Exec(ctx, "one", v1alpha1.ExecRequest{})
	assert.ErrorIs(t, err, v1alpha1.ErrInvalid)
}

func TestDialReachesOnlyTheSpecsPorts(t *testing.T) {
	f := newFixture(t, false, false)
	ctx := t.Context()
	_, _, err := f.provider.Create(ctx, "one", testSpec())
	require.NoError(t, err)
	f.run(t, "one")

	conn, err := f.provider.Dial(ctx, "one", 7)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	_, err = f.provider.Dial(ctx, "one", 8)
	assert.ErrorIs(t, err, v1alpha1.ErrInvalid)
	assert.Equal(t, []uint16{7}, f.guest.dialed)
}

func TestPodHoldsTheWorkspaceToItsSpec(t *testing.T) {
	spec := testSpec()
	pod := podFor("one", spec, testNamespace, testHelper)

	require.Len(t, pod.Spec.Containers, 1)
	main := pod.Spec.Containers[0]
	assert.Equal(t, spec.Image, main.Image)
	assert.Equal(t, main.Resources.Requests, main.Resources.Limits, "a workspace gets exactly what it asked for")
	assert.False(t, *pod.Spec.AutomountServiceAccountToken, "a workspace holds no cluster credential")
	assert.False(t, *main.SecurityContext.AllowPrivilegeEscalation)
	assert.Equal(t, []corev1.Capability{"ALL"}, main.SecurityContext.Capabilities.Drop)
	assert.True(t, *pod.Spec.SecurityContext.RunAsNonRoot, "PodSecurity restricted asks for it on the pod")
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, pod.Spec.SecurityContext.SeccompProfile.Type)
	for _, c := range append(pod.Spec.InitContainers, main) {
		assert.True(t, *c.SecurityContext.RunAsNonRoot, "container %s", c.Name)
		assert.False(t, *c.SecurityContext.AllowPrivilegeEscalation, "container %s", c.Name)
		assert.Equal(t, []corev1.Capability{"ALL"}, c.SecurityContext.Capabilities.Drop, "container %s", c.Name)
	}

	var helperMount, dataMount *corev1.VolumeMount
	for i := range main.VolumeMounts {
		switch main.VolumeMounts[i].MountPath {
		case helperDir:
			helperMount = &main.VolumeMounts[i]
		case v1alpha1.DataPath:
			dataMount = &main.VolumeMounts[i]
		}
	}
	require.NotNil(t, helperMount)
	assert.True(t, helperMount.ReadOnly, "the workspace cannot rewrite the helper that reads its files")
	require.NotNil(t, dataMount)
	assert.False(t, dataMount.ReadOnly)

	require.Len(t, pod.Spec.InitContainers, 1)
	assert.Equal(t, testHelper, pod.Spec.InitContainers[0].Image)
}

func TestPolicyAdmitsNothingInAndOnlyTheRulesOut(t *testing.T) {
	none := policyFor("one", testSpec(), testNamespace)
	assert.Len(t, none.Spec.PolicyTypes, 2, "both directions are selected, so both are closed")
	assert.Empty(t, none.Spec.Ingress)
	assert.Empty(t, none.Spec.Egress)

	spec := testSpec()
	spec.Egress = []v1alpha1.EgressRule{
		{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Protocol: v1alpha1.ProtocolTCP, Ports: []v1alpha1.PortRange{{First: 80, Last: 90}}},
		{Prefix: netip.MustParsePrefix("192.0.2.0/24"), Protocol: v1alpha1.ProtocolUDP},
	}
	rules := policyFor("one", spec, testNamespace).Spec.Egress
	require.Len(t, rules, 2)
	assert.Equal(t, "10.0.0.0/8", rules[0].To[0].IPBlock.CIDR)
	assert.Equal(t, int32(90), *rules[0].Ports[0].EndPort)
	assert.Equal(t, corev1.ProtocolUDP, *rules[1].Ports[0].Protocol, "a rule with no ports still names its protocol")
	assert.Nil(t, rules[1].Ports[0].Port)
}

func TestCreateRefusesAnImageThatRunsAsRoot(t *testing.T) {
	f := newFixture(t, false, false)
	f.kube.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: containerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CreateContainerConfigError", Message: "container has runAsNonRoot and image will run as root (pod: \"one_workspaces(uid)\", container: workspace)",
		}}}}

		return true, pod, f.kube.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, testNamespace)
	})

	_, created, err := f.provider.Create(t.Context(), "one", testSpec())

	require.ErrorIs(t, err, v1alpha1.ErrInvalid)
	assert.Contains(t, err.Error(), "runAsNonRoot")
	assert.False(t, created)
	_, err = f.provider.Get(t.Context(), "one")
	assert.ErrorIs(t, err, v1alpha1.ErrNotFound, "a refused Create leaves nothing behind")
}
