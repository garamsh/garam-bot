package podworkspace

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

const (
	// labelWorkspace marks every object of a workspace and holds the workspace's name. A claim that
	// lacks it is not the provider's, whatever its name.
	labelWorkspace = "garam-bot.garam.sh/workspace"
	// annotationSpec holds the spec a workspace was created with, as JSON, on its claim: the claim is
	// what outlives a stop, and Kubernetes answers an env value to whoever reads a Pod.
	annotationSpec = "garam-bot.garam.sh/spec"
	// annotationDesired is running or stopped: what the last Create, Start or Stop asked for.
	annotationDesired = "garam-bot.garam.sh/desired"

	desiredRunning = "running"
	desiredStopped = "stopped"

	containerName = "workspace"
	helperDir     = "/.garam-bot/bin"
	helperPath    = helperDir + "/workspacefile"
	helperVolume  = "helper"
	dataVolume    = "data"
)

func workspaceLabels(name v1alpha1.Name) map[string]string {
	return map[string]string{labelWorkspace: string(name)}
}

// objectLabels are a workspace object's labels: the caller's, and labelWorkspace, which the caller may
// not set (LABEL-1).
func objectLabels(name v1alpha1.Name, spec v1alpha1.Spec) map[string]string {
	labels := maps.Clone(spec.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelWorkspace] = string(name)
	return labels
}

// checkLabels refuses a spec whose labels set the key the provider marks its objects with.
func checkLabels(spec v1alpha1.Spec) error {
	if _, ok := spec.Labels[labelWorkspace]; ok {
		return fmt.Errorf("%w: label %s is the provider's own", v1alpha1.ErrInvalid, labelWorkspace)
	}
	return nil
}

func claimFor(name v1alpha1.Name, spec v1alpha1.Spec, ns string) (*corev1.PersistentVolumeClaim, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode spec: %w", err)
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        string(name),
			Namespace:   ns,
			Labels:      objectLabels(name, spec),
			Annotations: map[string]string{annotationSpec: string(raw), annotationDesired: desiredRunning},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: *resource.NewQuantity(int64(spec.Resources.DiskBytes), resource.BinarySI)},
			},
		},
	}
	if spec.FromSnapshot != "" {
		claim.Spec.DataSourceRef = &corev1.TypedObjectReference{
			APIGroup: ptr.To(snapshotGroup),
			Kind:     "VolumeSnapshot",
			Name:     string(spec.FromSnapshot),
		}
	}

	return claim, nil
}

func specOf(claim *corev1.PersistentVolumeClaim) (v1alpha1.Spec, error) {
	var spec v1alpha1.Spec
	if err := json.Unmarshal([]byte(claim.Annotations[annotationSpec]), &spec); err != nil {
		return v1alpha1.Spec{}, fmt.Errorf("claim %s: decode the spec it holds: %w", claim.Name, err)
	}

	return spec, nil
}

func podFor(name v1alpha1.Name, spec v1alpha1.Spec, ns, helperImage string) *corev1.Pod {
	env := make([]corev1.EnvVar, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })
	ports := make([]corev1.ContainerPort, 0, len(spec.Ports))
	for _, p := range spec.Ports {
		ports = append(ports, corev1.ContainerPort{ContainerPort: int32(p), Protocol: corev1.ProtocolTCP})
	}
	resources := corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(int64(spec.Resources.CPU), resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(int64(spec.Resources.MemoryBytes), resource.BinarySI),
	}
	locked := &corev1.SecurityContext{
		RunAsNonRoot:             ptr.To(true),
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: string(name), Namespace: ns, Labels: objectLabels(name, spec)},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyAlways,
			AutomountServiceAccountToken: ptr.To(false),
			EnableServiceLinks:           ptr.To(false),
			SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			InitContainers: []corev1.Container{{
				Name:            "helper",
				Image:           helperImage,
				Command:         []string{"/workspacefile", "install", helperDir},
				VolumeMounts:    []corev1.VolumeMount{{Name: helperVolume, MountPath: helperDir}},
				SecurityContext: locked,
			}},
			Containers: []corev1.Container{{
				Name:      containerName,
				Image:     spec.Image,
				Env:       env,
				Ports:     ports,
				Resources: corev1.ResourceRequirements{Requests: resources, Limits: resources},
				VolumeMounts: []corev1.VolumeMount{
					{Name: dataVolume, MountPath: v1alpha1.DataPath},
					{Name: helperVolume, MountPath: helperDir, ReadOnly: true},
				},
				SecurityContext: locked,
			}},
			Volumes: []corev1.Volume{
				{Name: dataVolume, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: string(name)}}},
				{Name: helperVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

// policyFor holds the workspace to its spec's egress rules: it admits nothing in, because Dial and
// Exec reach the Pod through the API server and not through the Pod network, and nothing out but
// what a rule names.
func policyFor(name v1alpha1.Name, spec v1alpha1.Spec, ns string) *networkingv1.NetworkPolicy {
	var egress []networkingv1.NetworkPolicyEgressRule
	for _, r := range spec.Egress {
		proto := corev1.ProtocolTCP
		if r.Protocol == v1alpha1.ProtocolUDP {
			proto = corev1.ProtocolUDP
		}
		var ports []networkingv1.NetworkPolicyPort
		for _, pr := range r.Ports {
			port := networkingv1.NetworkPolicyPort{Protocol: &proto, Port: ptr.To(intstr.FromInt32(int32(pr.First)))}
			if pr.Last > pr.First {
				port.EndPort = ptr.To(int32(pr.Last))
			}
			ports = append(ports, port)
		}
		if len(ports) == 0 {
			ports = []networkingv1.NetworkPolicyPort{{Protocol: &proto}}
		}
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: r.Prefix.String()}}},
			Ports: ports,
		})
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: string(name), Namespace: ns, Labels: objectLabels(name, spec)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: workspaceLabels(name)},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Egress:      egress,
		},
	}
}
