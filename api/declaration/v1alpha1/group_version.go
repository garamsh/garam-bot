package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the API group and version of every kind in this package.
var GroupVersion = schema.GroupVersion{Group: "bot.garam.sh", Version: "v1alpha1"}

var schemeBuilder = runtime.NewSchemeBuilder(func(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&Bot{}, &BotList{},
		&Workspace{}, &WorkspaceList{},
		&WorkspaceClass{}, &WorkspaceClassList{},
		&BotPool{}, &BotPoolList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)

	return nil
})

// AddToScheme registers the four kinds and their lists in scheme.
var AddToScheme = schemeBuilder.AddToScheme
