//go:build chart

package chart

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// garamOffValues turn garam off: the connector registers nothing and serves interface 7 alone (ADR
// 0099 decision 3).
var garamOffValues = []string{"--set=connector.garamURL=", "--set=connector.garamRoot.configMap="}

// garamParts are what a render holds of garam: the connector's garam arguments, the identity Secret
// and the right on it, and the garam volumes.
func garamParts(t *testing.T, objects []unstructured.Unstructured) []string {
	t.Helper()
	var found []string
	connector := named[appsv1.Deployment](t, objects, "Deployment", releaseNamespace, releaseName+"-connector")
	for _, arg := range connector.Spec.Template.Spec.Containers[0].Args {
		for _, flag := range []string{
			"--garam-", "--registration-token-file", "--report-only", "--rotation-period", "--identity-secret",
		} {
			if strings.HasPrefix(arg, flag) {
				found = append(found, "argument "+arg)
			}
		}
	}
	for _, volume := range connector.Spec.Template.Spec.Volumes {
		if volume.Name == "garam-root" || volume.Name == "registration" {
			found = append(found, "volume "+volume.Name)
		}
	}
	if hasSecret(objects, "garam-connector-identity") {
		found = append(found, "the identity Secret")
	}
	for _, rule := range connectorRules(t, objects) {
		if slices.Contains(rule.ResourceNames, "garam-connector-identity") {
			found = append(found, "the right on the identity Secret")
		}
	}
	return found
}

// writes are the connector's writes of interface 1's kinds in a watched namespace.
func writes(t *testing.T, objects []unstructured.Unstructured) []right {
	t.Helper()
	var found []right
	for _, namespace := range watched {
		for _, r := range rights(roles(t, objects)[namespace+"/"+releaseName+"-connector-watched"].Rules) {
			if r.group == liveBotGet.group && !slices.Contains(readVerbs, r.verb) {
				found = append(found, r)
			}
		}
	}
	return found
}

func TestChart_RendersNothingOfGaramWithGaramOff(t *testing.T) {
	// Control: with garam on, each part is rendered.
	on := render(t)
	if got := garamParts(t, on); len(got) != 10 {
		t.Fatalf("with garam on the render holds %q, want the six arguments, two volumes, Secret and right", got)
	}
	if len(writes(t, on)) == 0 {
		t.Fatal("with garam on the connector writes no Bot")
	}

	off := render(t, garamOffValues...)
	if got := garamParts(t, off); len(got) != 0 {
		t.Errorf("with garam off the render holds %q", got)
	}
	if got := writes(t, off); len(got) != 0 {
		t.Errorf("with garam off the connector may still write %v, though it applies nothing", got)
	}
	want := []string{dnsEgress, apiEgress}
	if got := egress(policy(t, off, "connector").Spec.Egress); strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("with garam off the connector's egress = %q, want %q", got, want)
	}
	if args := workspaceArgs(t, render(t, append(slices.Clone(garamOffValues), workspaceValues...)...)); !slices.Contains(
		args, "--workspace-namespace="+workspaceNamespace) {
		t.Errorf("with garam off the connector is not given its workspaces: %v", args)
	}
}

func TestChart_RefusesGaramHalfOnAndAProxyWithGaramOff(t *testing.T) {
	if _, err := template(t, garamOffValues...); err != nil {
		t.Fatalf("control: garam off renders: %v", err)
	}
	for name, test := range map[string]struct {
		set  []string
		want string
	}{
		"a URL without a root": {[]string{"--set=connector.garamRoot.configMap="}, "set together"},
		"a root without a URL": {[]string{"--set=connector.garamURL="}, "set together"},
		"a proxy with garam off": {append(slices.Clone(garamOffValues), valueSets["proxy"]...),
			"proxy.url is set where garam is off"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := template(t, test.set...); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("a render with %v = %v, want refused with %q", test.set, err, test.want)
			}
		})
	}
}
