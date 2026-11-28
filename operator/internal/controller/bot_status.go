package controller

import (
	"regexp"
	"strings"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The reasons of a Bot's Running condition this reconciler writes itself. Where the runtime last
// terminated, the reason is the one its termination message names (garam-bot runtime.md §The status
// contract).
const (
	reasonBotRunning = "Running"
	reasonStarting   = "Starting"
	// reasonFailed is a termination whose message names no reason.
	reasonFailed = "Failed"
	// reasonWaitingForTrust is a Bot whose connector has published no trust bundle yet, so no Pod is
	// built for it.
	reasonWaitingForTrust = "WaitingForConnectorTrust"
)

// conditionReason is the form the API server accepts for a condition's reason.
var conditionReason = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)

// setRunning writes bot's Running condition from pod, the Pod of its StatefulSet, nil where there is
// none: True while the runtime's container is ready, and False otherwise (interface 1 STA-1).
func setRunning(bot *botv1.Bot, pod *corev1.Pod) {
	status, reason, message := metav1.ConditionFalse, reasonStarting, "The runtime has not reported ready yet."
	if container := runtimeStatus(pod); container != nil {
		switch terminated := lastTermination(container); {
		case container.Ready:
			status, reason, message = metav1.ConditionTrue, reasonBotRunning, "The runtime reports the bot running."
		case terminated != nil:
			reason, message = terminationReason(terminated)
		}
	}
	setBotCondition(bot, status, reason, message)
}

// setBotCondition writes bot's Running condition, for the generation it was built from.
func setBotCondition(bot *botv1.Bot, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&bot.Status.Conditions, metav1.Condition{
		Type: botv1.ConditionRunning, Status: status, Reason: reason, Message: message,
		ObservedGeneration: bot.Generation,
	})
	bot.Status.ObservedGeneration = bot.Generation
}

func runtimeStatus(pod *corev1.Pod) *corev1.ContainerStatus {
	if pod == nil {
		return nil
	}
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == botContainerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

// lastTermination is the runtime's termination now, or else its last one.
func lastTermination(container *corev1.ContainerStatus) *corev1.ContainerStateTerminated {
	if container.State.Terminated != nil {
		return container.State.Terminated
	}
	return container.LastTerminationState.Terminated
}

// terminationReason reads the runtime's termination message, `<Reason>: <message>`. A message in
// another form is reported whole under reasonFailed.
func terminationReason(terminated *corev1.ContainerStateTerminated) (reason, message string) {
	named, rest, found := strings.Cut(strings.TrimSpace(terminated.Message), ": ")
	if found && conditionReason.MatchString(named) {
		return named, rest
	}
	if terminated.Message == "" {
		return reasonFailed, "The runtime exited " + terminated.Reason + " with no termination message."
	}
	return reasonFailed, strings.TrimSpace(terminated.Message)
}
