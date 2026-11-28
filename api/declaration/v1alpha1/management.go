package v1alpha1

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The ownership marker an object of a declared kind carries in its metadata.
const (
	// LabelManagedBy is the label naming the object's owner, OwnerGaram or OwnerCluster. An object
	// without it is the execution place's own, at a cluster, a PC or a naru-host place alike.
	LabelManagedBy = "bot.garam.sh/managed-by"
	// AnnotationCluster is the GRN of the cluster record that applied a garam-owned object.
	AnnotationCluster = "bot.garam.sh/cluster"
	// AnnotationDeclaration is the identifier of the garam declaration a garam-owned object is applied from.
	AnnotationDeclaration = "bot.garam.sh/declaration"
	// AnnotationDeclarationGeneration is the generation of that declaration, a decimal integer from 1.
	AnnotationDeclarationGeneration = "bot.garam.sh/declaration-generation"
)

// Owner is the one side that changes an object.
type Owner string

const (
	// OwnerGaram is garam: the object is applied from a garam declaration.
	OwnerGaram Owner = "garam"
	// OwnerCluster is the execution place itself, whatever kind of place it is: the object is written
	// there, by hand, from a file or by GitOps. The value stays "cluster" at every kind of place.
	OwnerCluster Owner = "cluster"
)

// ErrInvalidMarker is returned for an object whose ownership marker is malformed.
var ErrInvalidMarker = errors.New("invalid ownership marker")

// maxDeclarationIDLength bounds a declaration identifier.
const maxDeclarationIDLength = 256

// GaramDeclaration is where a garam-owned object comes from.
type GaramDeclaration struct {
	// Cluster is the GRN of the cluster record that applied the object, grn:<org>/iam/cluster/<id>.
	Cluster string
	// ID is the declaration's identifier, opaque to the cluster.
	ID string
	// Generation is the declaration's generation, from 1.
	Generation int64
}

// Management is an object's parsed ownership marker.
type Management struct {
	garam *GaramDeclaration
}

// Owner returns the object's owner.
func (m Management) Owner() Owner {
	if m.garam != nil {
		return OwnerGaram
	}

	return OwnerCluster
}

// Garam returns the declaration a garam-owned object comes from, and false for a cluster-owned one.
func (m Management) Garam() (GaramDeclaration, bool) {
	if m.garam == nil {
		return GaramDeclaration{}, false
	}

	return *m.garam, true
}

// ManagementOf parses obj's ownership marker. A malformed marker returns an error wrapping
// ErrInvalidMarker.
func ManagementOf(obj metav1.Object) (Management, error) {
	annotations := obj.GetAnnotations()
	switch owner, labelled := obj.GetLabels()[LabelManagedBy]; {
	case !labelled || Owner(owner) == OwnerCluster:
		for _, key := range []string{AnnotationCluster, AnnotationDeclaration, AnnotationDeclarationGeneration} {
			if _, ok := annotations[key]; ok {
				return Management{}, fmt.Errorf("%w: a cluster-owned object carries %s", ErrInvalidMarker, key)
			}
		}

		return Management{}, nil
	case Owner(owner) == OwnerGaram:
		declaration, err := parseGaramDeclaration(annotations)
		if err != nil {
			return Management{}, err
		}

		return Management{garam: &declaration}, nil
	default:
		return Management{}, fmt.Errorf("%w: %s is %q, not %q or %q", ErrInvalidMarker, LabelManagedBy, owner, OwnerGaram, OwnerCluster)
	}
}

// MarkGaramOwned sets obj's ownership marker to garam and d, replacing any marker it carried.
func MarkGaramOwned(obj metav1.Object, d GaramDeclaration) error {
	if err := d.validate(); err != nil {
		return err
	}
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[LabelManagedBy] = string(OwnerGaram)
	obj.SetLabels(labels)
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationCluster] = d.Cluster
	annotations[AnnotationDeclaration] = d.ID
	annotations[AnnotationDeclarationGeneration] = strconv.FormatInt(d.Generation, 10)
	obj.SetAnnotations(annotations)

	return nil
}

func parseGaramDeclaration(annotations map[string]string) (GaramDeclaration, error) {
	values := make(map[string]string, 3)
	for _, key := range []string{AnnotationCluster, AnnotationDeclaration, AnnotationDeclarationGeneration} {
		value, ok := annotations[key]
		if !ok {
			return GaramDeclaration{}, fmt.Errorf("%w: a garam-owned object lacks %s", ErrInvalidMarker, key)
		}
		values[key] = value
	}
	generation, err := strconv.ParseInt(values[AnnotationDeclarationGeneration], 10, 64)
	// The round trip refuses a sign or leading zeros, so one generation has one spelling.
	if err != nil || strconv.FormatInt(generation, 10) != values[AnnotationDeclarationGeneration] {
		return GaramDeclaration{}, fmt.Errorf("%w: %s is %q, not a decimal integer", ErrInvalidMarker, AnnotationDeclarationGeneration, values[AnnotationDeclarationGeneration])
	}
	d := GaramDeclaration{Cluster: values[AnnotationCluster], ID: values[AnnotationDeclaration], Generation: generation}
	if err := d.validate(); err != nil {
		return GaramDeclaration{}, err
	}

	return d, nil
}

func (d GaramDeclaration) validate() error {
	if !isClusterGRN(d.Cluster) {
		return fmt.Errorf("%w: %s is %q, not grn:<org>/iam/cluster/<id>", ErrInvalidMarker, AnnotationCluster, d.Cluster)
	}
	if d.ID == "" || len(d.ID) > maxDeclarationIDLength || strings.ContainsFunc(d.ID, notVisibleASCII) {
		return fmt.Errorf("%w: %s is %q, not 1 to %d visible ASCII characters", ErrInvalidMarker, AnnotationDeclaration, d.ID, maxDeclarationIDLength)
	}
	if d.Generation < 1 {
		return fmt.Errorf("%w: %s is %d, not at least 1", ErrInvalidMarker, AnnotationDeclarationGeneration, d.Generation)
	}

	return nil
}

// isClusterGRN reports whether grn is grn:<org>/iam/cluster/<id>, each part non-empty visible ASCII
// with no "/".
func isClusterGRN(grn string) bool {
	rest, ok := strings.CutPrefix(grn, "grn:")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[1] != "iam" || parts[2] != "cluster" {
		return false
	}
	for _, part := range []string{parts[0], parts[3]} {
		if part == "" || strings.ContainsFunc(part, notVisibleASCII) {
			return false
		}
	}

	return true
}

func notVisibleASCII(r rune) bool {
	return r <= ' ' || r > '~'
}
