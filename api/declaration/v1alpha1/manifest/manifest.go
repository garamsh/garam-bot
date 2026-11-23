// Package manifest reads one object of a declared kind from a file, admitting what an API server
// serving the kinds' CustomResourceDefinitions admits: the file form of interface 1.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	goyaml "go.yaml.in/yaml/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
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
// CustomResourceDefinitions in v1alpha1.CRDs refuses under strict field validation. A status the
// document carries is dropped, as the API server drops it from a create.
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
	schema, typed := kindSchemas[kind], newObject(kind)
	if apiVersion != v1alpha1.GroupVersion.String() || schema == nil || typed == nil {
		return nil, fmt.Errorf("%w: %s %s is not a kind of %s", ErrInvalid, apiVersion, kind, v1alpha1.GroupVersion)
	}
	delete(object, "status")
	if err := checkName(object); err != nil {
		return nil, err
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
	strict, err := sigsjson.UnmarshalStrict(defaulted, typed)
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

// checkName refuses an object without a name, or with a name or namespace the API server refuses.
func checkName(object map[string]any) error {
	metadata, _ := object["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return fmt.Errorf("%w: metadata.name %q: %s", ErrInvalid, name, strings.Join(problems, "; "))
	}
	if namespace, present := metadata["namespace"]; present {
		value, _ := namespace.(string)
		if problems := validation.IsDNS1123Label(value); len(problems) > 0 {
			return fmt.Errorf("%w: metadata.namespace %q: %s", ErrInvalid, value, strings.Join(problems, "; "))
		}
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
