package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspaceClass is how workspaces of one kind are made: which workspace provider makes them, the
// resources each gets, and when an idle one sleeps.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories=garam
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:metadata:annotations="bot.garam.sh/spec-release=0.1.0"
type WorkspaceClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WorkspaceClassSpec `json:"spec"`
}

// WorkspaceClassSpec is the declared state of a WorkspaceClass.
type WorkspaceClassSpec struct {
	// Provider names an implementation of garam-bot's workspace provider interface, such as mvmd or
	// container.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Provider string `json:"provider"`

	Resources WorkspaceResources `json:"resources"`

	// +optional
	Idle *Idle `json:"idle,omitempty"`
}

// WorkspaceResources are the resources one workspace of the class gets, each more than zero.
//
// +kubebuilder:validation:XValidation:rule="quantity(string(self.cpu)).isGreaterThan(quantity('0')) && quantity(string(self.memory)).isGreaterThan(quantity('0')) && quantity(string(self.disk)).isGreaterThan(quantity('0'))",message="cpu, memory and disk are each more than zero"
type WorkspaceResources struct {
	CPU    resource.Quantity `json:"cpu"`
	Memory resource.Quantity `json:"memory"`
	Disk   resource.Quantity `json:"disk"`
}

// Idle is when an idle workspace sleeps.
//
// +kubebuilder:validation:XValidation:rule="duration(self.sleepAfter) > duration('0s')",message="sleepAfter is a positive duration"
type Idle struct {
	// SleepAfter is how long a workspace runs no session before it sleeps.
	SleepAfter metav1.Duration `json:"sleepAfter"`
}

// WorkspaceClassList is a list of WorkspaceClasses.
//
// +kubebuilder:object:root=true
type WorkspaceClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceClass `json:"items"`
}
