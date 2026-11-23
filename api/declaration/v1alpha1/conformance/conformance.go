// Package conformance is the admission suite of interface 1, the declaration spec: the cases an
// implementation that admits declarations must admit or refuse, each named for the requirement in
// ../README.md it tests. An implementation plugs in as an Admitter and runs the suite with Run.
package conformance

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// Admitter is an implementation under test.
type Admitter interface {
	// Admit admits manifest, one object of a declared kind as a YAML document, into namespace, and
	// returns the object's spec as admitted, defaults applied, as JSON. It returns an error when it
	// refuses the object.
	Admit(ctx context.Context, namespace string, manifest []byte) (spec []byte, err error)
}

// Namespace is the namespace the suite admits every object into.
const Namespace = "conformance"

// Run runs every case against admitter, each as a subtest named <requirement>/<case>.
func Run(t *testing.T, admitter Admitter) {
	t.Helper()
	for _, c := range Cases() {
		t.Run(c.Requirement+"/"+c.Name, func(t *testing.T) {
			if c.Pending != "" {
				t.Skip("pending: " + c.Pending)
			}
			spec, err := admitter.Admit(t.Context(), Namespace, []byte(c.Manifest))
			switch {
			case c.Admit && err != nil:
				t.Fatalf("refused an object the spec admits: %v", err)
			case !c.Admit && err == nil:
				t.Fatalf("admitted an object the spec refuses; admitted spec %s", spec)
			case c.Admit && c.WantSpec != "":
				assertSameJSON(t, c.WantSpec, spec)
			}
		})
	}
}

func assertSameJSON(t *testing.T, want string, got []byte) {
	t.Helper()
	var wantValue, gotValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("the case's WantSpec is not JSON: %v", err)
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("the admitted spec is not JSON: %v: %s", err, got)
	}
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("admitted spec\n%s\nwant\n%s", got, want)
	}
}
