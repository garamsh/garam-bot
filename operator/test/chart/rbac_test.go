//go:build chart

package chart

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// right is one verb on one resource of one API group.
type right struct{ group, resource, verb string }

func rights(rules []rbacv1.PolicyRule) []right {
	var all []right
	for _, rule := range rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					all = append(all, right{group, resource, verb})
				}
			}
		}
	}
	return all
}

// generated are the manager's rights as the kubebuilder markers generate them: config/rbac's
// ClusterRole and its leader-election Role, which the chart narrows to Roles.
func generated(t *testing.T) map[right]bool {
	t.Helper()
	all := map[right]bool{}
	for _, file := range []string{"role.yaml", "leader_election_role.yaml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", file))
		if err != nil {
			t.Fatal(err)
		}
		objects, err := decode(bytes.TrimSpace(data))
		if err != nil {
			t.Fatal(err)
		}
		for _, role := range typed[rbacv1.ClusterRole](t, objects, "ClusterRole") {
			for _, r := range rights(role.Rules) {
				all[r] = true
			}
		}
		for _, role := range typed[rbacv1.Role](t, objects, "Role") {
			for _, r := range rights(role.Rules) {
				all[r] = true
			}
		}
	}
	return all
}

// connectorRights are the connector's rights in a watched namespace (connector.md §Rights in the
// user's cluster, ADR 0097
// decision 5): interface 1's four kinds, which it applies; the claim, which it reads; and the Pods and
// StatefulSets a bot's credential is bound to.
var connectorRights = func() map[right]bool {
	all := map[right]bool{}
	for _, kind := range []string{"bots", "workspaces", "workspaceclasses", "botpools"} {
		for _, verb := range []string{"list", "watch", "create", "patch", "delete"} {
			all[right{"bot.garam.sh", kind, verb}] = true
		}
	}
	for _, verb := range readVerbs {
		all[right{"coordination.k8s.io", "leases", verb}] = true
	}
	all[right{"", "pods", readVerbs[0]}] = true
	all[right{"apps", "statefulsets", readVerbs[0]}] = true
	return all
}()

// roles returns each rendered Role by "<namespace>/<name>".
func roles(t *testing.T, objects []unstructured.Unstructured) map[string]rbacv1.Role {
	t.Helper()
	all := map[string]rbacv1.Role{}
	for _, role := range typed[rbacv1.Role](t, objects, "Role") {
		all[role.Namespace+"/"+role.Name] = role
	}
	return all
}

func TestChart_GrantsTheManagerNoRightItsMarkersDoNot(t *testing.T) {
	allowed := generated(t)
	for name, args := range valueSets {
		t.Run(name, func(t *testing.T) {
			for key, role := range roles(t, render(t, args...)) {
				if !strings.HasPrefix(role.Name, releaseName+"-manager") {
					continue
				}
				for _, r := range rights(role.Rules) {
					if !allowed[r] {
						t.Errorf("Role %s grants %+v, which config/rbac does not", key, r)
					}
				}
			}
		})
	}
}

func TestChart_GrantsTheConnectorOnlyItsRightsInAWatchedNamespace(t *testing.T) {
	objects := render(t)
	for _, namespace := range watched {
		role, ok := roles(t, objects)[namespace+"/"+releaseName+"-connector-watched"]
		if !ok {
			t.Fatalf("no connector Role in %s", namespace)
		}
		granted := map[right]bool{}
		for _, r := range rights(role.Rules) {
			granted[r] = true
			if !connectorRights[r] {
				t.Errorf("the connector's Role in %s grants %+v", namespace, r)
			}
		}
		for r := range connectorRights {
			if !granted[r] {
				t.Errorf("the connector's Role in %s lacks %+v", namespace, r)
			}
		}
	}
}

// TestChart_NarrowsEveryRule checks what holds of both programs' Roles in every render: no wildcard;
// no right on a Secret outside the release namespace, and there by name alone; ConfigMaps by name
// alone outside the watched namespaces; and the claim read alone, by name.
func TestChart_NarrowsEveryRule(t *testing.T) {
	for name, args := range valueSets {
		t.Run(name, func(t *testing.T) {
			objects := render(t, args...)
			claimed := claimedNamespaces(objects)
			for key, role := range roles(t, objects) {
				for _, rule := range role.Rules {
					for _, field := range [][]string{rule.APIGroups, rule.Resources, rule.Verbs, rule.ResourceNames} {
						if slices.Contains(field, rbacv1.ResourceAll) {
							t.Errorf("Role %s has a wildcard rule %+v", key, rule)
						}
					}
					if len(rule.NonResourceURLs) > 0 {
						t.Errorf("Role %s names URLs %v", key, rule.NonResourceURLs)
					}
					checkRule(t, key, slices.Contains(claimed, role.Namespace), rule)
				}
			}
		})
	}
}

func checkRule(t *testing.T, key string, watchedNamespace bool, rule rbacv1.PolicyRule) {
	t.Helper()
	namespace, _, _ := strings.Cut(key, "/")
	for _, resource := range rule.Resources {
		switch resource {
		case "secrets":
			if namespace != releaseNamespace {
				t.Errorf("Role %s grants %v on Secrets outside the release namespace", key, rule.Verbs)
			} else if len(rule.ResourceNames) == 0 || slices.ContainsFunc(rule.Verbs, unnarrowable) {
				t.Errorf("Role %s grants %v on Secrets, not by name", key, rule.Verbs)
			}
		case "configmaps":
			if !watchedNamespace && len(rule.ResourceNames) == 0 {
				t.Errorf("Role %s grants %v on every ConfigMap of the release namespace", key, rule.Verbs)
			}
		case "leases":
			if namespace == releaseNamespace && rule.ResourceNames == nil {
				continue // the manager's leader election, which names its Lease at runtime
			}
			if fmt.Sprint(rule.ResourceNames) != "["+claimName+"]" ||
				slices.ContainsFunc(rule.Verbs, func(verb string) bool { return !slices.Contains(readVerbs, verb) }) {
				t.Errorf("Role %s grants %v on Leases %v, want the claim read alone", key, rule.Verbs, rule.ResourceNames)
			}
		}
	}
}

// readVerbs are the verbs that read, get first.
var readVerbs = []string{"get", "list", "watch"}

// unnarrowable are the verbs RBAC cannot narrow by resourceNames.
func unnarrowable(verb string) bool { return verb == "create" || verb == "deletecollection" }

// claimedNamespaces are the namespaces a render claims, which are the ones it watches.
func claimedNamespaces(objects []unstructured.Unstructured) []string {
	var claimed []string
	for _, object := range objects {
		if object.GetKind() == leaseKind && object.GetName() == claimName {
			claimed = append(claimed, object.GetNamespace())
		}
	}
	return claimed
}

func TestChart_BindsEachRoleToItsProgramAlone(t *testing.T) {
	objects := render(t, workspaceValues...)
	known := roles(t, objects)
	for _, binding := range typed[rbacv1.RoleBinding](t, objects, "RoleBinding") {
		key := binding.Namespace + "/" + binding.Name
		if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != binding.Name {
			t.Errorf("RoleBinding %s binds %s %s", key, binding.RoleRef.Kind, binding.RoleRef.Name)
		}
		if _, ok := known[binding.Namespace+"/"+binding.RoleRef.Name]; !ok {
			t.Errorf("RoleBinding %s binds no rendered Role", key)
		}
		account := strings.TrimSuffix(strings.TrimSuffix(binding.Name, "-watched"), "-workspaces")
		if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != rbacv1.ServiceAccountKind ||
			binding.Subjects[0].Name != account || binding.Subjects[0].Namespace != releaseNamespace {
			t.Errorf("RoleBinding %s binds %+v, want the ServiceAccount %s/%s alone",
				key, binding.Subjects, releaseNamespace, account)
		}
	}
}
