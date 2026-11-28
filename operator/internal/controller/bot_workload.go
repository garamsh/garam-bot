package controller

import (
	"path"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/garamsh/garam-bot/operator/internal/workload"
)

// The recommended labels every workload object carries: what it is, and which object it was built for.
const (
	labelName     = "app.kubernetes.io/name"
	labelInstance = "app.kubernetes.io/instance"
)

const (
	// botContainerName is the one container of a Bot's Pod, the garam-bot runtime.
	botContainerName = "runtime"

	// botAppName labels every object built for a Bot, and the Pod the Bot's StatefulSet makes, so the
	// Pod maps back to its Bot.
	botAppName = "garam-bot"

	// The runtime's user and group, named by number in its image (garam-bot runtime.md §What the image
	// asks of the Pod). Every file mounted into the Pod is readable by that group.
	botRunAsUser = 65532
	botFSGroup   = 65532

	// The files mounted into the runtime, read-only, and where.
	botFileDir     = "/etc/garam-bot/bot"
	botFileName    = "bot.yaml"
	trustDir       = "/etc/garam-bot/connector"
	credentialDir  = "/var/run/secrets/garam-connector"
	credentialName = "token"
	// workspaceCredentialName is interface 7's credential, beside interface 6's in the same volume.
	workspaceCredentialName = "workspace-token"
	botFileVolume           = "bot"
	trustVolume             = "connector-trust"
	credentialVolume        = "connector-credential"

	// credentialLifetime is the projected ServiceAccount token's expirationSeconds: the life the
	// connector's bound refuses beyond is an hour, and the kubelet replaces it at 80 % (ADR 0097).
	credentialLifetime = 600

	// garamRootName is garam's root in the trust copy, beside the connector's trust, for a Bot that
	// polls its garam mailbox.
	garamRootName = "garam-root.crt"

	// botFileMode lets the Pod's group read a mounted file and nobody write it.
	botFileMode = 0o440
)

// botLabels are the labels of every object built for bot, and of its Pod.
func botLabels(bot *botv1.Bot) map[string]string {
	return map[string]string{labelName: botAppName, labelInstance: bot.Name}
}

// botFileConfigMapName and trustCopyName are the ConfigMaps of bot's namespace that hold its Bot file
// and its copy of the connector's trust bundle. Each is the Bot's alone, so each is owned by it.
func botFileConfigMapName(bot *botv1.Bot) string { return bot.Name + "-bot" }
func trustCopyName(bot *botv1.Bot) string        { return bot.Name + "-connector-trust" }

// botFile is the Bot in interface 1's file form, as the runtime reads it with manifest.Parse: the
// kind, the name, namespace and generation the status reports, and the spec. Nothing of the object's
// status or other metadata is written.
func botFile(bot *botv1.Bot) ([]byte, error) {
	return yaml.Marshal(map[string]any{
		"apiVersion": botv1.GroupVersion.String(),
		"kind":       "Bot",
		"metadata": map[string]any{
			"name": bot.Name, "namespace": bot.Namespace, "generation": bot.Generation,
		},
		"spec": bot.Spec,
	})
}

// applyServiceAccount makes account the Bot's ServiceAccount: it holds no rights, and no token of it
// is mounted but the one projected for the connector.
func applyServiceAccount(bot *botv1.Bot, account *corev1.ServiceAccount) {
	account.Labels = botLabels(bot)
	account.AutomountServiceAccountToken = ptr.To(false)
}

// applyStatefulSet makes set the Bot's StatefulSet of one, running the runtime on the Bot file and,
// where connector is set, on the trust copy and interface 7's projected credential, which reach the
// Bot's workspace through the connector, and where garamOwned is set also on interface 6's, which
// obtains the bot's garam token (ADR 0090 decision 3, ADR 0097, ADR 0099 decision 12). Where mailbox is
// given, the runtime also long-polls the bot's garam mailbox for its tasks, through the release's
// proxy where it names one (garam-bot ADR 0043 decision 7).
func (r *BotReconciler) applyStatefulSet(bot *botv1.Bot, set *appsv1.StatefulSet, connector, garamOwned bool,
	mailbox *botMailbox,
) {
	labels := botLabels(bot)
	set.Labels = labels
	if set.CreationTimestamp.IsZero() {
		set.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	}
	set.Spec.Replicas = ptr.To[int32](1)
	template := &set.Spec.Template
	template.Labels = labels
	spec := &template.Spec
	spec.ServiceAccountName = bot.Name
	spec.AutomountServiceAccountToken = ptr.To(false)
	workload.RestrictPod(spec, botRunAsUser, botFSGroup)
	spec.Volumes = []corev1.Volume{
		{Name: botFileVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: botFileConfigMapName(bot)},
			DefaultMode:          ptr.To[int32](botFileMode),
		}}},
	}
	args := []string{"run", "--bot-file", path.Join(botFileDir, botFileName)}
	mounts := []corev1.VolumeMount{{Name: botFileVolume, MountPath: botFileDir, ReadOnly: true}}
	if connector {
		spec.Volumes = append(spec.Volumes, r.connectorVolumes(bot, garamOwned)...)
		args = append(args,
			"--connector-url", r.ConnectorURL,
			"--connector-trust-file", path.Join(trustDir, r.TrustKey),
			"--workspace-credential-file", path.Join(credentialDir, workspaceCredentialName))
		if garamOwned {
			args = append(args, "--credential-file", path.Join(credentialDir, credentialName))
		}
		if mailbox != nil {
			args = append(args,
				"--mailbox-url", r.Mailbox.URL,
				"--mailbox-origin", r.Mailbox.Origin,
				"--garam-root-file", path.Join(trustDir, garamRootName),
				"--bot-grn", mailbox.grn)
		}
		mounts = append(mounts,
			corev1.VolumeMount{Name: trustVolume, MountPath: trustDir, ReadOnly: true},
			corev1.VolumeMount{Name: credentialVolume, MountPath: credentialDir, ReadOnly: true})
	}
	// Every field the API server would default is named, so a pass that changes nothing sends nothing.
	probe := &corev1.Probe{
		ProbeHandler:   corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{runtimeBinary, "probe"}}},
		TimeoutSeconds: 1, PeriodSeconds: 10, SuccessThreshold: 1, FailureThreshold: 3,
	}
	spec.Containers = []corev1.Container{{
		Name:            botContainerName,
		Image:           r.Image,
		ImagePullPolicy: workload.PullPolicy,
		Args:            args,
		ReadinessProbe:  probe,
		LivenessProbe:   probe.DeepCopy(),
		SecurityContext: workload.ContainerSecurityContext(),
		VolumeMounts:    mounts,
		Env:             r.proxyEnv(mailbox),
		// Where the runtime writes a failed start's reason, which the Bot's condition reads.
		TerminationMessagePath:   corev1.TerminationMessagePathDefault,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}}
}

// proxyEnv is the proxy a Bot's Pod reaches its garam mailbox through, where the release names one, and
// what it reaches directly: the connector's Service and the cluster's own names. The runtime's clients
// of the connector go through no proxy whatever the environment says.
func (r *BotReconciler) proxyEnv(mailbox *botMailbox) []corev1.EnvVar {
	if mailbox == nil || r.Mailbox.Proxy == "" {
		return nil
	}
	return []corev1.EnvVar{{Name: "HTTPS_PROXY", Value: r.Mailbox.Proxy}, {Name: "NO_PROXY", Value: r.Mailbox.NoProxy}}
}

// connectorVolumes are the trust copy and the ServiceAccount tokens projected for the connector:
// interface 7's for every Bot, and interface 6's for a garam-owned one.
func (r *BotReconciler) connectorVolumes(bot *botv1.Bot, garamOwned bool) []corev1.Volume {
	tokens := []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
		Audience: r.WorkspaceAudience, ExpirationSeconds: ptr.To[int64](credentialLifetime), Path: workspaceCredentialName,
	}}}
	if garamOwned {
		tokens = append(tokens, corev1.VolumeProjection{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
			Audience: r.Audience, ExpirationSeconds: ptr.To[int64](credentialLifetime), Path: credentialName,
		}})
	}
	return []corev1.Volume{
		{Name: trustVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: trustCopyName(bot)},
			DefaultMode:          ptr.To[int32](botFileMode),
		}}},
		{Name: credentialVolume, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: tokens, DefaultMode: ptr.To[int32](botFileMode),
		}}},
	}
}

// runtimeBinary is the runtime image's entrypoint (garam-bot build/runtime.Dockerfile), which the
// probes run as its probe subcommand.
const runtimeBinary = "/garam-bot"
