package v1alpha1

import "embed"

// CRDs holds the CustomResourceDefinitions of the four kinds, one YAML file each under crds/, as
// controller-gen generates them from this package.
//
//go:embed crds/*.yaml
var CRDs embed.FS
