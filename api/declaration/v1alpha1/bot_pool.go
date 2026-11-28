package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BotPool is the set of pods the Shared bots that name it run in, one garam-bot per pod and a
// process per bot.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories=garam
// +kubebuilder:printcolumn:name="Max bots per pod",type=integer,JSONPath=`.spec.maxBotsPerPod`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:metadata:annotations="bot.garam.sh/spec-release.v1alpha1=0.1.1"
type BotPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec BotPoolSpec `json:"spec"`
}

// BotPoolSpec is the declared state of a BotPool.
type BotPoolSpec struct {
	// MaxBotsPerPod is the most bots one pod of the pool runs.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	MaxBotsPerPod int32 `json:"maxBotsPerPod"`
}

// BotPoolList is a list of BotPools.
//
// +kubebuilder:object:root=true
type BotPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BotPool `json:"items"`
}
