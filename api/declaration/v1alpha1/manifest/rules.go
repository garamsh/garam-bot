package manifest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/kube-openapi/pkg/validation/spec"
)

// rules holds, for each CEL rule the CustomResourceDefinitions carry, a Go function that admits
// what the rule admits. A rule with no entry here refuses every object it applies to.
var rules = map[string]func(self any) bool{
	"has(self.ref) != has(self.template)": func(self any) bool {
		object, _ := self.(map[string]any)
		_, ref := object["ref"]
		_, template := object["template"]

		return ref != template
	},
	"self.indexOf('/') <= 253 && self.size() - self.indexOf('/') - 1 <= 253": func(self any) bool {
		ref, _ := self.(string)
		slash := strings.Index(ref, "/")

		return slash <= 253 && len(ref)-slash-1 <= 253
	},
	"!self.endsWith('/.') && !self.endsWith('/..')": func(self any) bool {
		ref, _ := self.(string)

		return !strings.HasSuffix(ref, "/.") && !strings.HasSuffix(ref, "/..")
	},
	"duration(self.sleepAfter) > duration('0s')": func(self any) bool {
		object, _ := self.(map[string]any)
		text, _ := object["sleepAfter"].(string)
		sleepAfter, err := time.ParseDuration(text)

		return err == nil && sleepAfter > 0
	},
}

// errNoRule is the refusal of a CEL rule rules does not hold.
var errNoRule = errors.New("the rule has no implementation in this reader")

// checkRules applies each CEL rule the schema attaches to a value present in value.
func checkRules(schema *spec.Schema, value any, path string) []error {
	var problems []error
	validations, _ := schema.Extensions["x-kubernetes-validations"].([]any)
	for _, validation := range validations {
		entry, _ := validation.(map[string]any)
		rule, _ := entry["rule"].(string)
		message, _ := entry["message"].(string)
		admit, known := rules[rule]
		switch {
		case !known:
			problems = append(problems, fmt.Errorf("%s: %q: %w", path, rule, errNoRule))
		case !admit(value):
			problems = append(problems, fmt.Errorf("%s: %s", path, message))
		}
	}
	switch value := value.(type) {
	case map[string]any:
		for name, property := range schema.Properties {
			if child, present := value[name]; present {
				problems = append(problems, checkRules(&property, child, join(path, name))...)
			}
		}
	case []any:
		if schema.Items != nil && schema.Items.Schema != nil {
			for i, item := range value {
				problems = append(problems, checkRules(schema.Items.Schema, item, path+"["+strconv.Itoa(i)+"]")...)
			}
		}
	}

	return problems
}
