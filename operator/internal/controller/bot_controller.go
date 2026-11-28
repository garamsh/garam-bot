package controller

import (
	"context"
	"fmt"
	"sync"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/garamsh/garam-bot/operator/internal/bottoken"
)

// ClaimLeaseName is the Lease that claims a watched namespace for one release of the connector chart,
// its holderIdentity `<release-namespace>/<release-name>` (connector.md §Declarations and ownership,
// CLAIM-2). The manager reads it and never writes it.
const ClaimLeaseName = "garam-connector-claim"

// BotReconciler runs each Bot of a namespace claimed for its release as one StatefulSet of one of the
// garam-bot runtime, which obtains the bot's token from the connector (ADR 0090, ADR 0097, ADR 0098).
type BotReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Image is the garam-bot runtime image, by digest.
	Image string
	// Release is the connector chart's release, `<namespace>/<name>`: a namespace is acted in only
	// while its claim names it (ADR 0091 decision 1).
	Release string
	// TrustNamespace, TrustConfigMap and TrustKey name the connector's published trust bundle, which is
	// copied into each Bot's namespace and mounted into its Pod.
	TrustNamespace string
	TrustConfigMap string
	TrustKey       string
	// WorkspaceAudience is the audience of the second token projected into every Bot's Pod, for
	// interface 7 (garam-bot-operator ADR 0099 decision 12).
	WorkspaceAudience string
	// ConnectorURL is the connector, as the runtime reaches it, and empty where the chart runs none, so
	// no Bot reaches one. Audience is the audience of interface 6's token, projected for a garam-owned
	// Bot alone.
	ConnectorURL string
	Audience     string
	// Mailbox is garam's mailbox a garam-owned Bot long-polls for its tasks, and nil where the release
	// configures none, so a garam-owned Bot is given no task. Reader reads its garam root, which the
	// manager's cache does not hold, and RootCache, holding that ConfigMap alone, wakes every Bot on its
	// change.
	Mailbox   *Mailbox
	Reader    client.Reader
	RootCache cache.Cache

	// unclaimed holds each Bot already logged as standing in a namespace not claimed for Release, so
	// it is logged once while that lasts.
	unclaimed sync.Map
}

// Mailbox is what a garam-owned Bot is given to long-poll its garam mailbox (garam's pollBotMailbox).
type Mailbox struct {
	// URL is garam's machine listener as a Bot's Pod dials it, and Origin garam's machine origin.
	URL    string
	Origin string
	// RootConfigMap and RootKey name, in TrustNamespace, the ConfigMap holding garam's root.
	RootConfigMap string
	RootKey       string
	// Proxy is the HTTPS proxy a Bot's Pod reaches garam through, empty for none, and NoProxy what it
	// reaches directly.
	Proxy   string
	NoProxy string
}

// botMailbox is what one Bot is given of the mailbox: its GRN, and garam's root as last read.
type botMailbox struct {
	grn  string
	root string
}

// +kubebuilder:rbac:groups=bot.garam.sh,resources=bots,verbs=get;list;watch
// +kubebuilder:rbac:groups=bot.garam.sh,resources=bots/status,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch

// Reconcile builds a Bot's ServiceAccount, Bot file, trust copy and StatefulSet, each owned by the Bot,
// and writes its Running condition from its Pod.
func (r *BotReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	bot := &botv1.Bot{}
	if err := r.Get(ctx, req.NamespacedName, bot); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !bot.DeletionTimestamp.IsZero() {
		// The garbage collector removes what the Bot owns.
		return ctrl.Result{}, nil
	}
	claimed, holder, err := r.claimed(ctx, bot.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !claimed {
		if _, logged := r.unclaimed.LoadOrStore(bot.UID, true); !logged {
			logf.FromContext(ctx).Info("Building nothing for the Bot: its namespace is not claimed for this release",
				"release", r.Release, "claim", holder)
		}
		return ctrl.Result{}, nil
	}
	r.unclaimed.Delete(bot.UID)

	// Every Bot reaches the connector for its workspace, so each waits for its trust; only a garam-owned
	// one also has a garam identity (garam-bot-operator ADR 0099 decision 12). With no connector, a Bot
	// reaches none.
	bundle := ""
	if r.ConnectorURL != "" {
		trust := &corev1.ConfigMap{}
		err = r.Get(ctx, client.ObjectKey{Namespace: r.TrustNamespace, Name: r.TrustConfigMap}, trust)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("read the connector's trust bundle: %w", err)
		}
		if bundle = trust.Data[r.TrustKey]; bundle == "" {
			setBotCondition(bot, metav1.ConditionFalse, reasonWaitingForTrust,
				"The connector has published no trust bundle in "+r.TrustNamespace+"/"+r.TrustConfigMap+" yet.")
			return ctrl.Result{}, r.writeBotStatus(ctx, bot)
		}
	}
	management, err := botv1.ManagementOf(bot)
	garamOwned := err == nil && management.Owner() == botv1.OwnerGaram
	var mailbox *botMailbox
	if garamOwned && r.Mailbox != nil && r.ConnectorURL != "" {
		if mailbox, err = r.mailboxOf(ctx, management); err != nil {
			return ctrl.Result{}, err
		}
		if mailbox == nil {
			setBotCondition(bot, metav1.ConditionFalse, reasonWaitingForGaramRoot,
				"The release has published no garam root in "+r.TrustNamespace+"/"+r.Mailbox.RootConfigMap+" yet.")
			return ctrl.Result{}, r.writeBotStatus(ctx, bot)
		}
	}
	set, err := r.reconcileObjects(ctx, bot, bundle, garamOwned, mailbox)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Only the Pod the Bot's own StatefulSet controls reports on the Bot.
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: bot.Namespace, Name: bot.Name + "-0"}, pod); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("read the Bot's Pod: %w", err)
		}
		pod = nil
	} else if !metav1.IsControlledBy(pod, set) {
		pod = nil
	}
	setRunning(bot, pod)
	return ctrl.Result{}, r.writeBotStatus(ctx, bot)
}

// mailboxOf is what the garam-owned Bot management names is given of the mailbox, and nil while
// garam's root is not published.
func (r *BotReconciler) mailboxOf(ctx context.Context, management botv1.Management) (*botMailbox, error) {
	declaration, _ := management.Garam()
	grn, err := bottoken.BotGRN(declaration.Cluster, declaration.ID)
	if err != nil {
		return nil, err
	}
	root := &corev1.ConfigMap{}
	err = r.Reader.Get(ctx, client.ObjectKey{Namespace: r.TrustNamespace, Name: r.Mailbox.RootConfigMap}, root)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read garam's root: %w", err)
	case root.Data[r.Mailbox.RootKey] == "":
		return nil, nil
	}
	return &botMailbox{grn: string(grn), root: root.Data[r.Mailbox.RootKey]}, nil
}

// claimed reports whether namespace's claim names Release, and the holder it names.
func (r *BotReconciler) claimed(ctx context.Context, namespace string) (bool, string, error) {
	lease := &coordinationv1.Lease{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ClaimLeaseName}, lease)
	switch {
	case apierrors.IsNotFound(err):
		return false, "", nil
	case err != nil:
		return false, "", fmt.Errorf("read the claim of %s: %w", namespace, err)
	case lease.Spec.HolderIdentity == nil:
		return false, "", nil
	}
	return *lease.Spec.HolderIdentity == r.Release, *lease.Spec.HolderIdentity, nil
}

// reconcileObjects creates or brings back each object the Bot runs on, every one controlled by the Bot,
// and answers the StatefulSet as the API server now holds it. bundle is the connector's trust bundle,
// and empty where the manager has no connector, so the Bot gets no trust copy. garamOwned gives the Bot
// interface 6's credential too, and mailbox its garam mailbox, with garam's root in the trust copy.
func (r *BotReconciler) reconcileObjects(ctx context.Context, bot *botv1.Bot, bundle string, garamOwned bool,
	mailbox *botMailbox,
) (
	*appsv1.StatefulSet, error,
) {
	file, err := botFile(bot)
	if err != nil {
		return nil, fmt.Errorf("render the Bot file: %w", err)
	}
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: bot.Namespace} }
	account := &corev1.ServiceAccount{ObjectMeta: meta(bot.Name)}
	botConfig := &corev1.ConfigMap{ObjectMeta: meta(botFileConfigMapName(bot))}
	trustCopy := &corev1.ConfigMap{ObjectMeta: meta(trustCopyName(bot))}
	set := &appsv1.StatefulSet{ObjectMeta: meta(bot.Name)}
	type owned struct {
		object client.Object
		apply  func()
	}
	objects := []owned{
		{account, func() { applyServiceAccount(bot, account) }},
		{botConfig, func() {
			botConfig.Labels = botLabels(bot)
			botConfig.Data = map[string]string{botFileName: string(file)}
		}},
	}
	if bundle != "" {
		objects = append(objects, owned{trustCopy, func() {
			trustCopy.Labels = botLabels(bot)
			trustCopy.Data = map[string]string{r.TrustKey: bundle}
			if mailbox != nil {
				trustCopy.Data[garamRootName] = mailbox.root
			}
		}})
	}
	objects = append(objects, owned{set, func() { r.applyStatefulSet(bot, set, bundle != "", garamOwned, mailbox) }})
	for _, object := range objects {
		operation, err := controllerutil.CreateOrUpdate(ctx, r.Client, object.object, func() error {
			object.apply()
			return controllerutil.SetControllerReference(bot, object.object, r.Scheme)
		})
		if err != nil {
			return nil, fmt.Errorf("create or update %T %s: %w", object.object, object.object.GetName(), err)
		}
		if operation != controllerutil.OperationResultNone {
			logf.FromContext(ctx).Info("Reconciled an object of the Bot", "kind", fmt.Sprintf("%T", object.object),
				"name", object.object.GetName(), "operation", operation)
		}
	}
	return set, nil
}

// writeBotStatus writes bot's status through the status subresource. An update that changes nothing
// is one the API server writes nothing for.
func (r *BotReconciler) writeBotStatus(ctx context.Context, bot *botv1.Bot) error {
	if err := r.Status().Update(ctx, bot); err != nil {
		return fmt.Errorf("write the Bot's status: %w", err)
	}
	return nil
}

// SetupWithManager sets the reconciler up with mgr. A Bot is woken by what it owns, by its Pod, by its
// namespace's claim, and by the connector's trust bundle and garam's root, whose every change is copied
// out.
func (r *BotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr)
	if r.RootCache != nil {
		builder = builder.WatchesRawSource(source.Kind(r.RootCache, client.Object(&corev1.ConfigMap{}),
			handler.EnqueueRequestsFromMapFunc(r.botsOfTrust)))
	}
	return builder.
		For(&botv1.Bot{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(botOfPod)).
		Watches(&coordinationv1.Lease{}, handler.EnqueueRequestsFromMapFunc(r.botsOfClaim)).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.botsOfTrust)).
		Named("bot").
		Complete(r)
}

// botOfPod maps a Bot's Pod to its Bot, by the labels its StatefulSet's template gives it.
func botOfPod(_ context.Context, pod client.Object) []reconcile.Request {
	labels := pod.GetLabels()
	if labels[labelName] != botAppName || labels[labelInstance] == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{
		Namespace: pod.GetNamespace(), Name: labels[labelInstance],
	}}}
}

// botsOfClaim maps a namespace's claim to every Bot of that namespace.
func (r *BotReconciler) botsOfClaim(ctx context.Context, lease client.Object) []reconcile.Request {
	if lease.GetName() != ClaimLeaseName {
		return nil
	}
	return r.botsIn(ctx, client.InNamespace(lease.GetNamespace()))
}

// botsOfTrust maps the connector's trust bundle, and garam's root where a mailbox is given, to every
// Bot, so a replaced certificate reaches each Bot's copy.
func (r *BotReconciler) botsOfTrust(ctx context.Context, configMap client.Object) []reconcile.Request {
	name := configMap.GetName()
	root := r.Mailbox != nil && name == r.Mailbox.RootConfigMap
	if configMap.GetNamespace() != r.TrustNamespace || (name != r.TrustConfigMap && !root) {
		return nil
	}
	return r.botsIn(ctx)
}

func (r *BotReconciler) botsIn(ctx context.Context, options ...client.ListOption) []reconcile.Request {
	bots := &botv1.BotList{}
	if err := r.List(ctx, bots, options...); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list the Bots to wake")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(bots.Items))
	for _, bot := range bots.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bot)})
	}
	return requests
}
