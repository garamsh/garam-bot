package manifest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"strconv"

	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/yaml"

	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
)

// kindSchemas is each declared kind's v1alpha1 schema, read out of the CustomResourceDefinitions.
var kindSchemas = loadSchemas()

// definition is the part of a CustomResourceDefinition this package reads.
type definition struct {
	Spec struct {
		Group string `json:"group"`
		Names struct {
			Kind string `json:"kind"`
		} `json:"names"`
		Versions []struct {
			Name   string `json:"name"`
			Schema struct {
				OpenAPIV3Schema json.RawMessage `json:"openAPIV3Schema"`
			} `json:"schema"`
		} `json:"versions"`
	} `json:"spec"`
}

// loadSchemas panics on a definition it cannot read: they are compiled in, so the failure is the
// build's, not the input's.
func loadSchemas() map[string]*spec.Schema {
	files, err := fs.Glob(v1alpha1.CRDs, "crds/*.yaml")
	if err != nil {
		panic(err)
	}
	schemas := map[string]*spec.Schema{}
	for _, file := range files {
		data, err := fs.ReadFile(v1alpha1.CRDs, file)
		if err != nil {
			panic(err)
		}
		var d definition
		if err := yaml.Unmarshal(data, &d); err != nil {
			panic(fmt.Sprintf("%s: %v", file, err))
		}
		for _, version := range d.Spec.Versions {
			if d.Spec.Group != v1alpha1.GroupVersion.Group || version.Name != v1alpha1.GroupVersion.Version {
				continue
			}
			schema := &spec.Schema{}
			if err := json.Unmarshal(version.Schema.OpenAPIV3Schema, schema); err != nil {
				panic(fmt.Sprintf("%s: %v", file, err))
			}
			schemas[d.Spec.Names.Kind] = schema
		}
	}

	return schemas
}

// applyDefaults sets each absent property that has a default, in every object present in value.
func applyDefaults(schema *spec.Schema, value any) {
	switch value := value.(type) {
	case map[string]any:
		for name, property := range schema.Properties {
			if _, present := value[name]; !present && property.Default != nil {
				value[name] = copyJSON(property.Default)
			}
			if child, present := value[name]; present {
				applyDefaults(&property, child)
			}
		}
	case []any:
		if schema.Items != nil && schema.Items.Schema != nil {
			for _, item := range value {
				applyDefaults(schema.Items.Schema, item)
			}
		}
	}
}

// checkLists refuses a duplicate item in a list of type set, and a duplicate key in a list of type map.
func checkLists(schema *spec.Schema, value any, path string) []error {
	var problems []error
	switch value := value.(type) {
	case map[string]any:
		for name, property := range schema.Properties {
			if child, present := value[name]; present {
				problems = append(problems, checkLists(&property, child, join(path, name))...)
			}
		}
	case []any:
		if schema.Items == nil || schema.Items.Schema == nil {
			return nil
		}
		listType, _ := schema.Extensions["x-kubernetes-list-type"].(string)
		mapKeys, _ := schema.Extensions["x-kubernetes-list-map-keys"].([]any)
		for i, item := range value {
			itemPath := path + "[" + strconv.Itoa(i) + "]"
			for _, earlier := range value[:i] {
				if listType == "set" && reflect.DeepEqual(item, earlier) {
					problems = append(problems, fmt.Errorf("%s: duplicate value in a set", itemPath))
				}
				if listType == "map" && sameKeys(item, earlier, mapKeys) {
					problems = append(problems, fmt.Errorf("%s: duplicate key in a map list", itemPath))
				}
			}
			problems = append(problems, checkLists(schema.Items.Schema, item, itemPath)...)
		}
	}

	return problems
}

func sameKeys(a, b any, keys []any) bool {
	left, _ := a.(map[string]any)
	right, _ := b.(map[string]any)
	for _, key := range keys {
		name, _ := key.(string)
		if !reflect.DeepEqual(left[name], right[name]) {
			return false
		}
	}

	return true
}

func copyJSON(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}

	return out
}

func join(path, name string) string {
	if path == "" {
		return name
	}

	return path + "." + name
}
