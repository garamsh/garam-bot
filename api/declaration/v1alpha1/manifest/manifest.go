// Package manifest reads one object of a declared kind from a file, admitting what an API server
// serving the kinds' CustomResourceDefinitions admits: the file form of interface 1.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	goyaml "go.yaml.in/yaml/v2"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"
	sigsjson "sigs.k8s.io/json"
	"sigs.k8s.io/yaml"

	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
)

// Object is one object of a declared kind: a *v1alpha1.Bot, *v1alpha1.Workspace,
// *v1alpha1.WorkspaceClass or *v1alpha1.BotPool.
type Object interface {
	metav1.Object
	runtime.Object
}

// ErrInvalid is returned for a document that is not one admissible object of a declared kind.
var ErrInvalid = errors.New("invalid declaration")

// Parse reads data, one YAML or JSON document holding one object of a declared kind, and returns
// the object with the schema's defaults applied. It refuses what the API server serving the
// CustomResourceDefinitions in v1alpha1.CRDs refuses on a create under strict field validation. A
// status the document carries, for a kind that has one, is checked for unknown fields and then
// dropped, as the API server drops it from a create.
func Parse(data []byte) (Object, error) {
	if count, err := documents(data); err != nil || count != 1 {
		return nil, fmt.Errorf("%w: the file is not one YAML or JSON document", ErrInvalid)
	}
	document, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var object map[string]any
	if err := json.Unmarshal(document, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: the document is not one object", ErrInvalid)
	}
	kind, _ := object["kind"].(string)
	apiVersion, _ := object["apiVersion"].(string)
	schema := kindSchemas[kind]
	if apiVersion != v1alpha1.GroupVersion.String() || schema == nil || newObject(kind) == nil {
		return nil, fmt.Errorf("%w: %s %s is not a kind of %s", ErrInvalid, apiVersion, kind, v1alpha1.GroupVersion)
	}
	// The server's strict decode refuses a status field its schema does not name, a status on a kind
	// with none among them, and then drops the status unvalidated. So the status is checked for
	// field names alone; the rest of the document is decoded strictly below.
	if status, present := object["status"]; present {
		statusSchema, declared := schema.Properties["status"]
		if !declared {
			return nil, fmt.Errorf("%w: unknown field \"status\"", ErrInvalid)
		}
		if unknown := unknownFields(&statusSchema, status, "status"); len(unknown) > 0 {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(unknown...))
		}
		delete(object, "status")
	}

	applyDefaults(schema, object)
	problems := validate.NewSchemaValidator(schema, nil, "", strfmt.Default).Validate(object).Errors
	problems = append(problems, checkLists(schema, object, "")...)
	problems = append(problems, checkRules(schema, object, "")...)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(problems...))
	}

	defaulted, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	typed, err := decodeStrict(defaulted, kind)
	if err != nil {
		return nil, err
	}
	if err := checkMetadata(typed); err != nil {
		return nil, err
	}

	return typed, nil
}

func decodeStrict(document []byte, kind string) (Object, error) {
	typed := newObject(kind)
	strict, err := sigsjson.UnmarshalStrict(document, typed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(strict) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(strict...))
	}

	return typed, nil
}

func newObject(kind string) Object {
	switch kind {
	case "Bot":
		return &v1alpha1.Bot{}
	case "Workspace":
		return &v1alpha1.Workspace{}
	case "WorkspaceClass":
		return &v1alpha1.WorkspaceClass{}
	case "BotPool":
		return &v1alpha1.BotPool{}
	default:
		return nil
	}
}

// checkMetadata refuses metadata the API server refuses on a create: an invalid name, namespace,
// label, annotation or finalizer, and a resourceVersion, which only the server sets. A file needs no
// namespace, so one is checked only where it is set.
func checkMetadata(object Object) error {
	problems := apivalidation.ValidateObjectMetaAccessor(object, object.GetNamespace() != "",
		apivalidation.NameIsDNSSubdomain, field.NewPath("metadata"))
	if object.GetResourceVersion() != "" {
		problems = append(problems, field.Forbidden(field.NewPath("metadata", "resourceVersion"), "may not be set on a create"))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, problems.ToAggregate())
	}

	return nil
}

// documents counts the YAML documents in data that are not empty, or returns the error that stopped
// reading them.
func documents(data []byte) (int, error) {
	decoder := goyaml.NewDecoder(bytes.NewReader(data))
	count := 0
	for {
		var document any
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		if document != nil {
			count++
		}
	}
}
