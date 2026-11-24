package v1alpha1_test

import (
	"io/fs"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
)

type definition struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Group string `json:"group"`
		Names struct {
			Kind string `json:"kind"`
		} `json:"names"`
		Scope    string `json:"scope"`
		Versions []struct {
			Name   string `json:"name"`
			Schema struct {
				OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
			} `json:"schema"`
		} `json:"versions"`
	} `json:"spec"`
}

func definitions(t *testing.T) []definition {
	t.Helper()
	files, err := fs.Glob(v1alpha1.CRDs, "crds/*.yaml")
	require.NoError(t, err)
	var out []definition
	for _, file := range files {
		data, err := fs.ReadFile(v1alpha1.CRDs, file)
		require.NoError(t, err)
		var d definition
		require.NoError(t, yaml.Unmarshal(data, &d), file)
		out = append(out, d)
	}

	return out
}

func TestCRDs_SCH1_DefineTheFourKindsNamespacedInOneGroupVersion(t *testing.T) {
	var kinds []string
	for _, d := range definitions(t) {
		kinds = append(kinds, d.Spec.Names.Kind)
		assert.Equal(t, "bot.garam.sh", d.Spec.Group, d.Spec.Names.Kind)
		assert.Equal(t, "Namespaced", d.Spec.Scope, d.Spec.Names.Kind)
		require.Len(t, d.Spec.Versions, 1, d.Spec.Names.Kind)
		assert.Equal(t, "v1alpha1", d.Spec.Versions[0].Name, d.Spec.Names.Kind)
	}
	slices.Sort(kinds)
	assert.Equal(t, []string{"Bot", "BotPool", "Workspace", "WorkspaceClass"}, kinds)
}

func TestCRDs_VER1_EachNamesTheSpecRelease(t *testing.T) {
	for _, d := range definitions(t) {
		assert.Equal(t, v1alpha1.SpecRelease, d.Metadata.Annotations[v1alpha1.AnnotationSpecRelease], d.Spec.Names.Kind)
	}
}

// imageProperty matches a property name that names an image.
var imageProperty = regexp.MustCompile(`(?i)image`)

func TestCRDs_IMG1_NoPropertyNamesAnImage(t *testing.T) {
	// The control: the walk reaches a nested property it must see.
	var all []string
	for _, d := range definitions(t) {
		walkProperties(d.Spec.Versions[0].Schema.OpenAPIV3Schema, d.Spec.Names.Kind, func(path, name string) {
			all = append(all, path)
			assert.False(t, imageProperty.MatchString(name), "%s names an image", path)
		})
	}
	assert.Contains(t, all, "Bot.spec.workspaces[].ref.access")
}

func walkProperties(schema map[string]any, path string, visit func(path, name string)) {
	properties, _ := schema["properties"].(map[string]any)
	for name, property := range properties {
		child, _ := property.(map[string]any)
		visit(path+"."+name, name)
		walkProperties(child, path+"."+name, visit)
	}
	if items, ok := schema["items"].(map[string]any); ok {
		walkProperties(items, path+"[]", visit)
	}
}
