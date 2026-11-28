//go:build chart

package chart

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	naruCallerSecret = "garam-connector-naru-caller"
	createVerb       = "create"
)

// The rights the naru Machine provider calls with, and one it does not. A create on a Secret is
// TestChart_NarrowsEveryRule's to refuse.
var (
	callerSecretGet    = right{"", "secrets", "get"}
	callerSecretUpdate = right{"", callerSecretGet.resource, "update"}
	eventCreate        = right{"", "events", createVerb}
	eventPatch         = right{"", eventCreate.resource, "patch"}
)

// naruArgs are the connector's --naru-* and --connector-deployment arguments in a render.
func naruArgs(t *testing.T, objects []unstructured.Unstructured) []string {
	t.Helper()
	connector := named[appsv1.Deployment](t, objects, "Deployment", releaseNamespace, releaseName+"-connector")
	var found []string
	for _, arg := range connector.Spec.Template.Spec.Containers[0].Args {
		if strings.HasPrefix(arg, "--naru-") || strings.HasPrefix(arg, "--connector-deployment") {
			found = append(found, arg)
		}
	}
	return found
}

// connectorRules are the rules of the connector's Role in the release namespace.
func connectorRules(t *testing.T, objects []unstructured.Unstructured) []rbacv1.PolicyRule {
	t.Helper()
	return roles(t, objects)[releaseNamespace+"/"+releaseName+"-connector"].Rules
}

func hasSecret(objects []unstructured.Unstructured, name string) bool {
	return slices.ContainsFunc(objects, func(o unstructured.Unstructured) bool {
		return o.GetKind() == "Secret" && o.GetNamespace() == releaseNamespace && o.GetName() == name
	})
}

func TestChart_RunsTheNaruProviderOnlyWhereItIsOn(t *testing.T) {
	off := render(t)
	if args := naruArgs(t, off); len(args) != 0 {
		t.Errorf("with connector.naru off the connector is given %v", args)
	}
	if hasSecret(off, naruCallerSecret) {
		t.Errorf("with connector.naru off the caller Secret is rendered")
	}
	for _, rule := range connectorRules(t, off) {
		if slices.Contains(rule.Resources, "events") || slices.Contains(rule.ResourceNames, naruCallerSecret) {
			t.Errorf("with connector.naru off the connector is granted %+v", rule)
		}
	}

	on := render(t, naruValues...)
	want := []string{
		"--naru-url=https://orch.example:7600",
		"--naru-root-file=/etc/garam-connector/naru-root/ca.crt",
		"--naru-registration-token-file=/etc/garam-connector/naru-registration/token",
		"--naru-caller-secret=" + naruCallerSecret,
		"--naru-proxy-address=proxy.example:443",
		"--naru-root-disk-bytes=4294967296",
		"--connector-deployment=" + releaseName + "-connector",
	}
	if got := naruArgs(t, on); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the connector's naru arguments = %v, want %v", got, want)
	}
	if !hasSecret(on, naruCallerSecret) {
		t.Errorf("no caller Secret %s rendered", naruCallerSecret)
	}
	granted := map[right]bool{}
	for _, rule := range connectorRules(t, on) {
		for _, r := range rights([]rbacv1.PolicyRule{rule}) {
			if r.resource == callerSecretGet.resource && !slices.Contains(rule.ResourceNames, naruCallerSecret) {
				continue
			}
			granted[r] = true
		}
	}
	for _, r := range []right{callerSecretGet, callerSecretUpdate, eventCreate} {
		if !granted[r] {
			t.Errorf("the connector's Role lacks %+v", r)
		}
	}
	if granted[eventPatch] {
		t.Errorf("the connector's Role grants more than the naru provider calls: %v", granted)
	}
	got := egress(policy(t, on, "connector").Spec.Egress)
	for _, rule := range []string{"10.20.0.0/24[] TCP/7600", "10.20.1.0/24[] TCP/443"} {
		if !slices.Contains(got, rule) {
			t.Errorf("the connector's egress %q lacks %q", got, rule)
		}
	}
}

func TestChart_RefusesAnIncompleteNaruConfiguration(t *testing.T) {
	if _, err := template(t, naruValues...); err != nil {
		t.Fatalf("control: the whole naru configuration renders: %v", err)
	}
	for name, unset := range map[string]string{
		"url":           "--set=connector.naru.url=",
		"root":          "--set=connector.naru.root.configMap=",
		"proxy address": "--set=connector.naru.proxyAddress=",
		"root disk":     "--set=connector.naru.rootDiskBytes=0",
		"egress":        "--set-json=connector.naru.egress=[]",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := template(t, append(slices.Clone(naruValues), unset)...); err == nil ||
				!strings.Contains(err.Error(), "connector.naru.") {
				t.Errorf("a render without the %s = %v, want refused naming it", name, err)
			}
		})
	}
}
