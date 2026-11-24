package v1alpha1

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// SecretKeyRef names one data key of a Secret in the referring object's own namespace, as
// "<secret-name>/<key>". The name is a DNS subdomain, as Kubernetes requires of a Secret's name, and
// the key is a Secret data key: [-._a-zA-Z0-9]+, neither "." nor "..". Neither holds a "/", so the
// split is unambiguous.
//
// +kubebuilder:validation:MaxLength=507
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[-._a-zA-Z0-9]+$`
// +kubebuilder:validation:XValidation:rule="self.indexOf('/') <= 253 && self.size() - self.indexOf('/') - 1 <= 253",message="the secret name and the key are each at most 253 characters"
// +kubebuilder:validation:XValidation:rule="!self.endsWith('/.') && !self.endsWith('/..')",message="the key is neither . nor .."
type SecretKeyRef string

// ErrInvalidSecretKeyRef is returned for a reference that is not "<secret-name>/<key>".
var ErrInvalidSecretKeyRef = errors.New("secret reference is not <secret-name>/<key>")

// ParseSecretKeyRef parses ref and returns the Secret's name and the data key it names.
func ParseSecretKeyRef(ref string) (name, key string, err error) {
	// A reference with no "/" leaves the key empty, which IsConfigMapKey refuses.
	name, key, _ = strings.Cut(ref, "/")
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return "", "", fmt.Errorf("%w: secret name %q: %s", ErrInvalidSecretKeyRef, name, strings.Join(problems, "; "))
	}
	if problems := validation.IsConfigMapKey(key); len(problems) > 0 {
		return "", "", fmt.Errorf("%w: key %q: %s", ErrInvalidSecretKeyRef, key, strings.Join(problems, "; "))
	}

	return name, key, nil
}
