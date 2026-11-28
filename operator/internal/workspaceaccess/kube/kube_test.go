package kube_test

import (
	"context"
	"testing"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/garamsh/garam-bot/operator/internal/workspaceaccess"
	"github.com/garamsh/garam-bot/operator/internal/workspaceaccess/kube"
)

const (
	team    = "game-team"
	release = "garam-system/garam"
	// mira names the Bot, its StatefulSet and its ServiceAccount.
	mira      = "mira"
	botUID    = "bot-uid"
	runtimeID = "containerd://runtime"
	standard  = "standard"
	rin       = "rin"
)

// controller is the owner reference of mira's controller, of the kind.
func controller(apiVersion, kind string, uid types.UID) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: mira, UID: uid, Controller: &yes}}
}

// botPod is mira's Pod, controlled by mira's StatefulSet, its runtime container running.
func botPod() *corev1.Pod {
	return &corev1.Pod{
		Namespace: team, Name: "mira-0", UID: "pod-uid",
		OwnerReferences: controller("apps/v1", "StatefulSet", "set-uid"),
		Spec:            corev1.PodSpec{ServiceAccountName: mira},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "init", ContainerID: "containerd://other", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			{Name: kube.RuntimeContainer, ContainerID: runtimeID, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}},
	}
}

func botSet() *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		Namespace: team, Name: mira, UID: "set-uid",
		OwnerReferences: controller(botv1.GroupVersion.String(), "Bot", botUID)}
}

func TestPods_ReadsTheRunningContainerAndTheBotControllingTheStatefulSet(t *testing.T) {
	tests := []struct {
		name      string
		pod       func(*corev1.Pod)
		set       func(*appsv1.StatefulSet)
		botUID    string
		container string
	}{
		{name: "the Bot's Pod", botUID: botUID, container: runtimeID},
		{name: "a Pod being deleted runs no container", pod: func(p *corev1.Pod) { p.DeletionTimestamp = &metav1.Time{} },
			botUID: botUID},
		{name: "a runtime container that waits", pod: func(p *corev1.Pod) {
			p.Status.ContainerStatuses[1].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
		}, botUID: botUID},
		{name: "a Pod no StatefulSet controls", pod: func(p *corev1.Pod) {
			p.OwnerReferences = controller("apps/v1", "ReplicaSet", "set-uid")
		}, container: runtimeID},
		{name: "a StatefulSet made again under the name", set: func(s *appsv1.StatefulSet) { s.UID = "another" },
			container: runtimeID},
		{name: "a StatefulSet another kind of the Bot's group controls", set: func(s *appsv1.StatefulSet) {
			s.OwnerReferences = controller(botv1.GroupVersion.String(), "Workspace", botUID)
		}, container: runtimeID},
		{name: "a StatefulSet no Bot controls", set: func(s *appsv1.StatefulSet) {
			s.OwnerReferences = controller("apps/v1", "Deployment", botUID)
		}, container: runtimeID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod, set := botPod(), botSet()
			if tt.pod != nil {
				tt.pod(pod)
			}
			if tt.set != nil {
				tt.set(set)
			}
			pods := kube.Pods{Kube: kubefake.NewClientset(pod, set)}

			read, exists, err := pods.Pod(context.Background(), team, "mira-0")

			require.NoError(t, err)
			require.True(t, exists)
			assert.Equal(t, workspaceaccess.Pod{
				UID: "pod-uid", ServiceAccount: mira, BotUID: tt.botUID, Container: tt.container,
			}, read)
		})
	}
}

func TestPods_AnswersAPodThatDoesNotExistAsAbsent(t *testing.T) {
	pods := kube.Pods{Kube: kubefake.NewClientset(botPod(), botSet())}

	_, exists, err := pods.Pod(context.Background(), team, "mira-1")

	require.NoError(t, err)
	assert.False(t, exists)
	// Control: the Pod that exists is read.
	_, exists, err = pods.Pod(context.Background(), team, "mira-0")
	require.NoError(t, err)
	assert.True(t, exists)
}

type claims map[string]string

func (c claims) Claim(_ context.Context, namespace string) (string, bool, error) {
	holder, ok := c[namespace]
	return holder, ok, nil
}

func reader(t *testing.T, objects ...client.Object) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, botv1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func TestBots_ServesANamespaceClaimedForTheReleaseAlone(t *testing.T) {
	bots := kube.Bots{Claims: claims{team: release, "other-team": "other/release"}, Release: release}

	for namespace, want := range map[string]bool{team: true, "other-team": false, "unclaimed": false} {
		served, err := bots.Served(context.Background(), namespace)
		require.NoError(t, err)
		assert.Equal(t, want, served, namespace)
	}
}

func TestBots_ReadsTheBotsAttachmentsAndToolsAndRefusesWhatThisReleaseDoesNotServe(t *testing.T) {
	bot := &botv1.Bot{
		Namespace: team, Name: mira, UID: botUID,
		Spec: botv1.BotSpec{
			Agent: botv1.Agent{Harness: "claude-code"},
			Workspaces: []botv1.WorkspaceAttachment{
				{Name: "main", Template: &botv1.WorkspaceTemplate{Class: standard, Lifetime: botv1.LifetimeBot}},
				{Name: "scratch", Template: &botv1.WorkspaceTemplate{Class: standard, Lifetime: botv1.LifetimeSession}},
				{Name: "shared", Ref: &botv1.WorkspaceRef{Name: "repo"}},
			},
			Permissions: botv1.Permissions{Tools: []botv1.ToolPermission{{Name: "github"}, {Name: "linear"}}},
		},
	}
	bots := kube.Bots{Reader: reader(t, bot)}

	declared, ok, err := bots.Bot(context.Background(), workspaceaccess.Bot{Namespace: team, Name: mira})

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, botUID, declared.UID)
	assert.Equal(t, "claude-code", declared.Harness)
	assert.Equal(t, []string{"github", "linear"}, declared.Tools)
	assert.Equal(t, workspaceaccess.Attachment{Class: standard}, declared.Attachments["main"])
	assert.NotEmpty(t, declared.Attachments["scratch"].Unsupported, "the Session lifetime")
	assert.NotEmpty(t, declared.Attachments["shared"].Unsupported, "a ref")
	_, ok, err = bots.Bot(context.Background(), workspaceaccess.Bot{Namespace: team, Name: rin})
	require.NoError(t, err)
	assert.False(t, ok, "a Bot that does not exist")
}

func TestBots_ReadsAClassInWholeCPUsAndBytesRoundedUp(t *testing.T) {
	class := &botv1.WorkspaceClass{
		Namespace: team, Name: standard,
		Spec: botv1.WorkspaceClassSpec{Provider: "pod", Resources: botv1.WorkspaceResources{
			CPU: resource.MustParse("1500m"), Memory: resource.MustParse("1Gi"), Disk: resource.MustParse("1.5k"),
		}},
	}
	bots := kube.Bots{Reader: reader(t, class)}

	read, ok, err := bots.Class(context.Background(), team, standard)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, workspaceaccess.Class{Provider: "pod", Resources: workspace.Resources{
		CPU: 2, MemoryBytes: 1 << 30, DiskBytes: 1500,
	}}, read)
	_, ok, err = bots.Class(context.Background(), team, "large")
	require.NoError(t, err)
	assert.False(t, ok, "a class that does not exist")
}

func TestBots_AnswersABotBeingDeletedAsAbsent(t *testing.T) {
	deleting := &botv1.Bot{
		Namespace: team, Name: mira, UID: botUID, Finalizers: []string{"bot.garam.sh/test"},
		DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
	}
	living := &botv1.Bot{Namespace: team, Name: rin, UID: "rin-uid"}
	bots := kube.Bots{Reader: reader(t, deleting, living)}

	_, ok, err := bots.Bot(context.Background(), workspaceaccess.Bot{Namespace: team, Name: mira})
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = bots.Bot(context.Background(), workspaceaccess.Bot{Namespace: team, Name: rin})
	require.NoError(t, err)
	assert.True(t, ok, "control: a Bot not being deleted is read")
}

func TestLiveBots_AnswersTheBotsUIDAndAnAbsentBotAsAbsent(t *testing.T) {
	live := kube.LiveBots{Reader: reader(t, &botv1.Bot{Namespace: team, Name: mira, UID: botUID})}

	uid, exists, err := live.UID(context.Background(), workspaceaccess.Bot{Namespace: team, Name: mira})
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, botUID, uid)

	_, exists, err = live.UID(context.Background(), workspaceaccess.Bot{Namespace: team, Name: "rin"})
	require.NoError(t, err)
	assert.False(t, exists)
}
