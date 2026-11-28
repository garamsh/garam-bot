// Package workload is what every Pod the manager builds carries, whichever kind it builds it for: the
// fields PodSecurity restricted reads, and the pull policy (ADR 0090 decision 3, ADR 0098). It imports
// no domain.
package workload

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// PullPolicy is every container's. An image a reference names can be deleted from the repository
// holding it, and the default IfNotPresent turns a reference that stopped resolving into a per-node
// stale cache, so a node never runs what the registry no longer serves.
const PullPolicy = corev1.PullAlways

// ContainerSecurityContext is what every container carries. It holds the two fields PodSecurity
// restricted asks of a container and nothing else: readOnlyRootFilesystem is no part of that standard,
// and naming a user here would take the Pod's own away from whichever container named it.
func ContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// RestrictPod sets on spec the Pod-wide fields PodSecurity restricted reads, keeping the rest of its
// security context: every container runs as user, every volume is given group, and the RuntimeDefault
// seccomp profile applies.
func RestrictPod(spec *corev1.PodSpec, user, group int64) {
	if spec.SecurityContext == nil {
		spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	podSecurity := spec.SecurityContext
	podSecurity.FSGroup = ptr.To(group)
	podSecurity.RunAsUser = ptr.To(user)
	// Naming a user PodSecurity restricted would accept is not the same as asserting one: it reads
	// runAsNonRoot and refuses a Pod that leaves it unset. Both fields sit here rather than on the
	// containers because both are one value for the whole Pod.
	podSecurity.RunAsNonRoot = ptr.To(true)
	podSecurity.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
}
