package controller

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"
)

const (
	botRelease         = "garam-system/garam"
	botReleaseNS       = "garam-system"
	botTrustConfigMap  = "garam-connector-trust"
	botTrustBundle     = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	botImage           = "example.com/garam-bot/runtime@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	botConnectorURL    = "https://garam-connector.garam-system.svc:8443"
	botConnectorAudien = "garam-connector"
)

var botNamespaces atomic.Int32

// newBotNamespace creates a namespace of its own for one spec, claimed for holder where it is not
// empty, with labels.
func newBotNamespace(holder string, labels map[string]string) string {
	GinkgoHelper()
	name := fmt.Sprintf("bots-%d", botNamespaces.Add(1))
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}})).
		To(Succeed())
	if holder != "" {
		claimNamespace(name, holder)
	}
	return name
}

// claimNamespace writes namespace's claim, naming holder, as the connector's release does.
func claimNamespace(namespace, holder string) {
	GinkgoHelper()
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: ClaimLeaseName, Namespace: namespace}}
	err := createOrUpdate(lease, func() { lease.Spec.HolderIdentity = ptr.To(holder) })
	Expect(err).NotTo(HaveOccurred())
}

func createOrUpdate(object client.Object, apply func()) error {
	key := client.ObjectKeyFromObject(object)
	if err := k8sClient.Get(ctx, key, object); apierrors.IsNotFound(err) {
		apply()
		return k8sClient.Create(ctx, object)
	} else if err != nil {
		return err
	}
	apply()
	return k8sClient.Update(ctx, object)
}

// publishTrust writes the connector's trust bundle into the release namespace.
func publishTrust(bundle string) {
	GinkgoHelper()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: botReleaseNS}}
	if err := k8sClient.Create(ctx, namespace); err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
	trust := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: botTrustConfigMap, Namespace: botReleaseNS}}
	err := createOrUpdate(trust, func() { trust.Data = map[string]string{trustKeyForTest: bundle} })
	Expect(err).NotTo(HaveOccurred())
}

const trustKeyForTest = "ca.crt"

func newBotReconciler() *BotReconciler {
	return &BotReconciler{
		Client: k8sClient, Scheme: k8sClient.Scheme(),
		Image: botImage, Release: botRelease,
		TrustNamespace: botReleaseNS, TrustConfigMap: botTrustConfigMap, TrustKey: trustKeyForTest,
		ConnectorURL: botConnectorURL, Audience: botConnectorAudien,
	}
}

// createMira creates the Bot mira in namespace, garam-owned as the connector applies one.
func createMira(namespace string) *botv1.Bot {
	GinkgoHelper()
	return createBotOwned(namespace, "mira", true)
}

// createBotOwned creates the Bot name in namespace, garam-owned where garamOwned is set and
// cluster-owned otherwise.
func createBotOwned(namespace, name string, garamOwned bool) *botv1.Bot {
	GinkgoHelper()
	bot := &botv1.Bot{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: botv1.BotSpec{
			Persona: botv1.Persona{Role: "Game Lead"},
			Agent:   botv1.Agent{Harness: "claude-code"},
		},
	}
	if garamOwned {
		Expect(botv1.MarkGaramOwned(bot, botv1.GaramDeclaration{
			Cluster: "grn:acme/iam/cluster/c-1", ID: "decl-" + name, Generation: 1,
		})).To(Succeed())
	}
	Expect(k8sClient.Create(ctx, bot)).To(Succeed())
	return bot
}

func reconcileBot(r *BotReconciler, bot *botv1.Bot) {
	GinkgoHelper()
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(bot)})
	Expect(err).NotTo(HaveOccurred())
}

func getIn[T client.Object](namespace, name string, object T) T {
	GinkgoHelper()
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, object)).To(Succeed())
	return object
}

func absentIn(namespace, name string, object client.Object) bool {
	return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, object))
}

// expectControlledByBot asserts object's controller is bot, so the garbage collector deletes it with
// the Bot. envtest runs no garbage collector, so the reference is what is asserted.
func expectControlledByBot(object client.Object, bot *botv1.Bot) {
	GinkgoHelper()
	owner := metav1.GetControllerOf(object)
	Expect(owner).NotTo(BeNil(), "%T %s has a controller", object, object.GetName())
	Expect(owner.UID).To(Equal(bot.UID))
	Expect(owner.Kind).To(Equal("Bot"))
	Expect(owner.BlockOwnerDeletion).To(Equal(ptr.To(true)))
}

func runningOf(namespace, name string) *metav1.Condition {
	GinkgoHelper()
	bot := getIn(namespace, name, &botv1.Bot{})
	return meta.FindStatusCondition(bot.Status.Conditions, botv1.ConditionRunning)
}

var _ = Describe("Bot Controller", func() {
	BeforeEach(func() { publishTrust(botTrustBundle) })

	It("builds the ServiceAccount, Bot file, trust copy and StatefulSet a Bot runs on, each controlled by the Bot", func() {
		namespace := newBotNamespace(botRelease, nil)
		bot := createMira(namespace)

		reconcileBot(newBotReconciler(), bot)

		account := getIn(namespace, "mira", &corev1.ServiceAccount{})
		Expect(account.AutomountServiceAccountToken).To(Equal(ptr.To(false)), "the ServiceAccount's token is mounted nowhere")
		expectControlledByBot(account, bot)

		file := getIn(namespace, "mira-bot", &corev1.ConfigMap{})
		expectControlledByBot(file, bot)
		var read map[string]any
		Expect(yaml.Unmarshal([]byte(file.Data["bot.yaml"]), &read)).To(Succeed())
		Expect(read).To(HaveKeyWithValue("apiVersion", "bot.garam.sh/v1alpha1"))
		Expect(read).To(HaveKeyWithValue("kind", "Bot"))
		Expect(read["metadata"]).To(Equal(map[string]any{"name": "mira", "namespace": namespace, "generation": float64(1)}))
		Expect(read["spec"]).To(HaveKeyWithValue("persona", map[string]any{"role": "Game Lead"}))

		trust := getIn(namespace, "mira-connector-trust", &corev1.ConfigMap{})
		expectControlledByBot(trust, bot)
		Expect(trust.Data).To(Equal(map[string]string{"ca.crt": botTrustBundle}))

		set := getIn(namespace, "mira", &appsv1.StatefulSet{})
		expectControlledByBot(set, bot)
		Expect(set.Spec.Replicas).To(Equal(ptr.To[int32](1)))
		spec := set.Spec.Template.Spec
		Expect(spec.ServiceAccountName).To(Equal("mira"))
		Expect(spec.AutomountServiceAccountToken).To(Equal(ptr.To(false)))
		Expect(spec.Containers).To(HaveLen(1))
		container := spec.Containers[0]
		Expect(container.Image).To(Equal(botImage))
		Expect(container.ImagePullPolicy).To(Equal(corev1.PullAlways))
		Expect(container.Args).To(Equal([]string{"run",
			"--bot-file", "/etc/garam-bot/bot/bot.yaml",
			"--connector-url", botConnectorURL,
			"--connector-trust-file", "/etc/garam-bot/connector/ca.crt",
			"--credential-file", "/var/run/secrets/garam-connector/token",
		}))
		Expect(container.ReadinessProbe.Exec.Command).To(Equal([]string{"/garam-bot", "probe"}))
		Expect(container.LivenessProbe.Exec.Command).To(Equal([]string{"/garam-bot", "probe"}))
		var projected *corev1.ServiceAccountTokenProjection
		for _, volume := range spec.Volumes {
			if volume.Projected != nil {
				projected = volume.Projected.Sources[0].ServiceAccountToken
			}
		}
		Expect(projected).To(Equal(&corev1.ServiceAccountTokenProjection{
			Audience: botConnectorAudien, ExpirationSeconds: ptr.To[int64](600), Path: "token",
		}))
	})

	It("builds a cluster-owned Bot's Pod with no connector, no credential and no trust copy", func() {
		namespace := newBotNamespace(botRelease, nil)
		bot := createBotOwned(namespace, "local", false)
		r := newBotReconciler()
		r.TrustConfigMap = "unpublished"

		reconcileBot(r, bot)

		set := getIn(namespace, "local", &appsv1.StatefulSet{})
		Expect(set.Spec.Template.Spec.Containers[0].Args).To(Equal([]string{"run", "--bot-file", "/etc/garam-bot/bot/bot.yaml"}))
		Expect(set.Spec.Template.Spec.Volumes).To(HaveLen(1), "the Bot file alone")
		Expect(absentIn(namespace, "local-connector-trust", &corev1.ConfigMap{})).To(BeTrue())
		Expect(runningOf(namespace, "local").Reason).To(Equal(reasonStarting), "it waits for no connector's trust")

		// Control: a garam-owned Bot beside it, with the same unpublished trust, waits for it.
		garamOwned := createMira(namespace)
		reconcileBot(r, garamOwned)
		Expect(runningOf(namespace, "mira").Reason).To(Equal(reasonWaitingForTrust))
	})

	It("changes nothing when it reconciles the same Bot twice", func() {
		namespace := newBotNamespace(botRelease, nil)
		bot := createMira(namespace)
		r := newBotReconciler()
		reconcileBot(r, bot)
		versions := func() []string {
			return []string{
				getIn(namespace, "mira", &corev1.ServiceAccount{}).ResourceVersion,
				getIn(namespace, "mira-bot", &corev1.ConfigMap{}).ResourceVersion,
				getIn(namespace, "mira-connector-trust", &corev1.ConfigMap{}).ResourceVersion,
				getIn(namespace, "mira", &appsv1.StatefulSet{}).ResourceVersion,
				getIn(namespace, "mira", &botv1.Bot{}).ResourceVersion,
			}
		}
		first := versions()
		logged := &countingSink{}

		_, err := r.Reconcile(logf.IntoContext(ctx, logr.New(logged)),
			reconcile.Request{NamespacedName: client.ObjectKeyFromObject(bot)})

		Expect(err).NotTo(HaveOccurred())
		Expect(versions()).To(Equal(first))
		Expect(logged.count("Reconciled an object of the Bot")).To(BeZero(), "no write is sent: %v", logged.lines)
	})

	It("copies the connector's trust bundle again once it changes, and is woken by its change", func() {
		namespace := newBotNamespace(botRelease, nil)
		bot := createMira(namespace)
		r := newBotReconciler()
		reconcileBot(r, bot)
		replaced := botTrustBundle + botTrustBundle
		publishTrust(replaced)

		reconcileBot(r, bot)

		Expect(getIn(namespace, "mira-connector-trust", &corev1.ConfigMap{}).Data["ca.crt"]).To(Equal(replaced))
		source := getIn(botReleaseNS, botTrustConfigMap, &corev1.ConfigMap{})
		Expect(r.botsOfTrust(ctx, source)).To(ContainElement(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(bot)}),
			"a change of the connector's bundle wakes the Bot")
		other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "another", Namespace: botReleaseNS}}
		Expect(r.botsOfTrust(ctx, other)).To(BeEmpty(), "control: another ConfigMap of the namespace wakes nothing")
	})

	It("builds nothing in a namespace not claimed for its release, and says so once", func() {
		namespace := newBotNamespace("garam-system/another", nil)
		bot := createMira(namespace)
		r := newBotReconciler()
		logged := &countingSink{}
		logCtx := logf.IntoContext(ctx, logr.New(logged))

		for range 2 {
			_, err := r.Reconcile(logCtx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(bot)})
			Expect(err).NotTo(HaveOccurred())
		}

		for _, object := range []client.Object{&corev1.ServiceAccount{}, &appsv1.StatefulSet{}} {
			Expect(absentIn(namespace, "mira", object)).To(BeTrue(), "%T", object)
		}
		Expect(absentIn(namespace, "mira-bot", &corev1.ConfigMap{})).To(BeTrue())
		Expect(runningOf(namespace, "mira")).To(BeNil(), "nothing is written to the Bot")
		Expect(logged.count("its namespace is not claimed for this release")).To(Equal(1))

		// Control: the same Bot is built once the claim names this release.
		claimNamespace(namespace, botRelease)
		reconcileBot(r, bot)
		Expect(absentIn(namespace, "mira", &appsv1.StatefulSet{})).To(BeFalse())
		Expect(r.botsOfClaim(ctx, getIn(namespace, ClaimLeaseName, &coordinationv1.Lease{}))).
			To(ConsistOf(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(bot)}), "the claim's change wakes the Bot")
		another := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "another-lease", Namespace: namespace}}
		Expect(r.botsOfClaim(ctx, another)).To(BeEmpty(), "control: another Lease of the namespace wakes nothing")
	})

	It("builds no Pod and reports waiting while the connector has published no trust bundle", func() {
		namespace := newBotNamespace(botRelease, nil)
		bot := createMira(namespace)
		r := newBotReconciler()
		r.TrustConfigMap = "unpublished"

		reconcileBot(r, bot)

		Expect(absentIn(namespace, "mira", &appsv1.StatefulSet{})).To(BeTrue())
		running := runningOf(namespace, "mira")
		Expect(running).NotTo(BeNil())
		Expect(running.Status).To(Equal(metav1.ConditionFalse))
		Expect(running.Reason).To(Equal(reasonWaitingForTrust))
	})

	It("writes the Bot's Running condition from its Pod's runtime container", func() {
		namespace := newBotNamespace(botRelease, nil)
		bot := createMira(namespace)
		r := newBotReconciler()
		reconcileBot(r, bot)
		Expect(runningOf(namespace, "mira").Reason).To(Equal(reasonStarting), "no Pod yet")

		set := getIn(namespace, "mira", &appsv1.StatefulSet{})
		pod := botPodOf(set, namespace)
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: botContainerName, Ready: true, Image: botImage,
			ImageID: botImage, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		reconcileBot(r, bot)
		running := runningOf(namespace, "mira")
		Expect(running.Status).To(Equal(metav1.ConditionTrue))
		Expect(running.ObservedGeneration).To(Equal(int64(1)))

		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: botContainerName, Image: botImage, ImageID: botImage,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1, Reason: "Error", Message: "BotInvalid: /etc/garam-bot/bot/bot.yaml holds no Bot"}}}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		reconcileBot(r, bot)
		running = runningOf(namespace, "mira")
		Expect(running.Status).To(Equal(metav1.ConditionFalse))
		Expect(running.Reason).To(Equal("BotInvalid"), "the reason the runtime's termination message names")
		Expect(running.Message).To(Equal("/etc/garam-bot/bot/bot.yaml holds no Bot"))
	})

	It("reads no Pod its StatefulSet does not control", func() {
		namespace := newBotNamespace(botRelease, nil)
		bot := createMira(namespace)
		r := newBotReconciler()
		reconcileBot(r, bot)
		pod := botPodOf(nil, namespace)
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: botContainerName, Ready: true, Image: botImage,
			ImageID: botImage, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		reconcileBot(r, bot)

		Expect(runningOf(namespace, "mira").Status).To(Equal(metav1.ConditionFalse),
			"a ready Pod of the Bot's name that its StatefulSet does not control reports nothing")
	})

	It("builds a Pod a PodSecurity restricted namespace admits", func() {
		namespace := newBotNamespace(botRelease, map[string]string{"pod-security.kubernetes.io/enforce": "restricted"})
		bot := createMira(namespace)
		reconcileBot(newBotReconciler(), bot)
		set := getIn(namespace, "mira", &appsv1.StatefulSet{})

		admitted := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "admitted", Namespace: namespace},
			Spec: *set.Spec.Template.Spec.DeepCopy()}
		Expect(k8sClient.Create(ctx, admitted)).To(Succeed(), "the Bot's Pod is admitted")

		// Control: the same Pod without its container's restricted context is refused, so the namespace
		// enforces restricted.
		refused := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "refused", Namespace: namespace},
			Spec: *set.Spec.Template.Spec.DeepCopy()}
		refused.Spec.Containers[0].SecurityContext = nil
		err := k8sClient.Create(ctx, refused)
		Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("restricted"))
	})
})

// botPodOf creates the Pod mira-0 of namespace from the Bot's StatefulSet's template, as the StatefulSet
// controller would, controlled by set where it is not nil. envtest runs no StatefulSet controller.
func botPodOf(set *appsv1.StatefulSet, namespace string) *corev1.Pod {
	GinkgoHelper()
	template := getIn(namespace, "mira", &appsv1.StatefulSet{}).Spec.Template
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mira-0", Namespace: namespace, Labels: template.Labels},
		Spec: template.Spec}
	if set != nil {
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: set.Name,
			UID: set.UID, Controller: ptr.To(true)}}
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	return pod
}

// countingSink counts the log lines whose message contains a text.
type countingSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *countingSink) count(text string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, line := range s.lines {
		if strings.Contains(line, text) {
			n++
		}
	}
	return n
}

func (s *countingSink) Init(logr.RuntimeInfo)                   {}
func (s *countingSink) Enabled(int) bool                        { return true }
func (s *countingSink) Error(_ error, message string, _ ...any) { s.add(message) }
func (s *countingSink) Info(_ int, message string, _ ...any)    { s.add(message) }
func (s *countingSink) WithValues(...any) logr.LogSink          { return s }
func (s *countingSink) WithName(string) logr.LogSink            { return s }
func (s *countingSink) add(message string) {
	s.mu.Lock()
	s.lines = append(s.lines, message)
	s.mu.Unlock()
}
