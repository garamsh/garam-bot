package manifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// TestRules_EveryRuleOfTheDefinitionsHasAnImplementation holds the file form to the API server: a
// rule added to a definition without a Go counterpart fails here rather than refusing every object.
func TestRules_EveryRuleOfTheDefinitionsHasAnImplementation(t *testing.T) {
	found := map[string]bool{}
	for _, schema := range kindSchemas {
		collectRules(schema, found)
	}
	// The control: the walk finds the rules the definitions are known to carry.
	assert.True(t, found["has(self.ref) != has(self.template)"])
	assert.True(t, found["duration(self.sleepAfter) > duration('0s')"])
	for rule := range found {
		assert.Contains(t, rules, rule)
	}
	for rule := range rules {
		assert.True(t, found[rule], "rules holds %q, which no definition carries", rule)
	}
}

func collectRules(schema *spec.Schema, found map[string]bool) {
	validations, _ := schema.Extensions["x-kubernetes-validations"].([]any)
	for _, validation := range validations {
		entry, _ := validation.(map[string]any)
		rule, _ := entry["rule"].(string)
		found[rule] = true
	}
	for _, property := range schema.Properties {
		collectRules(&property, found)
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		collectRules(schema.Items.Schema, found)
	}
}
