package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// SpecRelease is the release of this version's specification the package and its
// CustomResourceDefinitions state, and the tag of the api module carrying it is api/v<SpecRelease>.
// Each CustomResourceDefinition carries it as the annotation AnnotationSpecRelease.
const SpecRelease = "0.1.0"

// AnnotationSpecRelease is the CustomResourceDefinition annotation naming its SpecRelease.
const AnnotationSpecRelease = "bot.garam.sh/spec-release"

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
