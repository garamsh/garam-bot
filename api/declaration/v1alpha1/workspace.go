package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Workspace is one workspace that outlives a session: an isolated computer a bot's agent works in,
// made from a WorkspaceClass and attached to bots by reference.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories=garam
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.class`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:metadata:annotations="bot.garam.sh/spec-release=0.1.0"
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WorkspaceSpec `json:"spec"`
	// +optional
	Status WorkspaceStatus `json:"status,omitzero"`
}

// WorkspaceSpec is the declared state of a Workspace.
type WorkspaceSpec struct {
	// Class names the WorkspaceClass, in the workspace's namespace, the workspace is made from.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Class string `json:"class"`
}

// WorkspaceStatus is the observed state of a Workspace, written by what provides it.
type WorkspaceStatus struct {
	// ObservedGeneration is the generation of the spec the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// WorkspaceList is a list of Workspaces.
//
// +kubebuilder:object:root=true
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}
