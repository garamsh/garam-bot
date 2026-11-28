//go:build chart

package chart

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// workspaceNamespace is the workspace namespace workspaceValues name (ADR 0099 decision 4).
const workspaceNamespace = "garam-workspaces"

// helperImage and agentImage are workspaceValues's file helper and claude-code image.
var (
	helperImage = "example.com/garam-bot/workspace-helper:0123456789ab@sha256:" + strings.Repeat("4", 64)
	agentImage  = "example.com/garam-bot/agent-claude-code:0123456789ab@sha256:" + strings.Repeat("5", 64)
)

// workspaceValues turn the bots' workspaces on, with every value they require.
var workspaceValues = []string{
	"--set=workspaces.namespace=" + workspaceNamespace,
	"--set=images.workspaceHelper=" + helperImage,
	"--set=images.agents.claude-code=" + agentImage,
	"--set=workspaces.pod.modelGatewayURL=https://gateway.example",
	"--set=workspaces.pod.toolProxyURL=https://tools.example",
	`--set-json=workspaces.pod.egress=[{"cidr":"10.96.0.10/32","protocol":"udp","port":53},` +
		`{"cidr":"10.30.0.0/24","protocol":"tcp","port":8443,"endPort":8444}]`,
}

// naruWorkspaceValues add the naru Machine provider's workspaces to workspaceValues and naruValues.
var naruWorkspaceValues = append(append(slices.Clone(workspaceValues), naruValues...),
	"--set=workspaces.naru.modelGatewayURL=https://gateway.naru.example",
	"--set=workspaces.naru.toolProxyURL=https://tools.naru.example",
	`--set-json=workspaces.naru.egress=[{"cidr":"10.40.0.53/32","protocol":"udp","port":53},`+
		`{"cidr":"10.40.1.0/24","protocol":"tcp","port":443}]`,
)

// workspaceFlags are the connector's flags of interface 7 (cmd/connector/workspaces.go).
var workspaceFlags = []string{
	"--workspace-", "--agent-image", "--max-dials", "--pod-", "--naru-model-", "--naru-tool-", "--naru-workspace-",
}

// workspaceArgs are the connector's interface 7 arguments in a render.
func workspaceArgs(t *testing.T, objects []unstructured.Unstructured) []string {
	t.Helper()
	connector := named[appsv1.Deployment](t, objects, "Deployment", releaseNamespace, releaseName+"-connector")
	var found []string
	for _, arg := range connector.Spec.Template.Spec.Containers[0].Args {
		if slices.ContainsFunc(workspaceFlags, func(prefix string) bool { return strings.HasPrefix(arg, prefix) }) {
			found = append(found, arg)
		}
	}
	return found
}

// providerRights are the Pod provider's rights, as the Role its conformance suite runs under grants
// them (pkg/podworkspace/testdata/rbac.yaml).
func providerRights(t *testing.T) map[right]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "pkg", "podworkspace", "testdata", "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := decode(bytes.TrimSpace(data))
	if err != nil {
		t.Fatal(err)
	}
	all := map[right]bool{}
	for _, role := range typed[rbacv1.Role](t, objects, "Role") {
		for _, r := range rights(role.Rules) {
			all[r] = true
		}
	}
	if len(all) == 0 {
		t.Fatal("the Pod provider's Role grants nothing")
	}
	return all
}

// liveBotGet is the right the deletion sweep reads a Bot live with (ADR 0099 decision 6).
var liveBotGet = right{"bot.garam.sh", "bots", readVerbs[0]}

func TestChart_ServesTheBotsWorkspacesOnlyWhereANamespaceIsSet(t *testing.T) {
	off := render(t)
	if args := workspaceArgs(t, off); len(args) != 0 {
		t.Errorf("with workspaces.namespace unset the connector is given %v", args)
	}
	for _, object := range off {
		if object.GetKind() == "Namespace" || object.GetNamespace() == workspaceNamespace {
			t.Errorf("with workspaces.namespace unset %s %s/%s is rendered",
				object.GetKind(), object.GetNamespace(), object.GetName())
		}
	}
	for _, namespace := range watched {
		if slices.Contains(rights(roles(t, off)[namespace+"/"+releaseName+"-connector-watched"].Rules), liveBotGet) {
			t.Errorf("with workspaces.namespace unset the connector may get Bots in %s", namespace)
		}
	}

	on := render(t, workspaceValues...)
	want := []string{
		"--workspace-namespace=" + workspaceNamespace,
		"--workspace-audience=garam-workspace",
		"--agent-image=claude-code=" + agentImage,
		"--workspace-helper-image=" + helperImage,
		"--max-dials=256",
		"--pod-workspace-enforces-egress=true",
		"--pod-model-gateway-url=https://gateway.example",
		"--pod-tool-proxy-url=https://tools.example",
		"--pod-workspace-egress=10.96.0.10/32/udp/53,10.30.0.0/24/tcp/8443-8444",
	}
	if got := workspaceArgs(t, on); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the connector's workspace arguments = %v, want %v", got, want)
	}
	for _, namespace := range watched {
		if !slices.Contains(rights(roles(t, on)[namespace+"/"+releaseName+"-connector-watched"].Rules), liveBotGet) {
			t.Errorf("the connector may not get Bots in %s, which the deletion sweep reads live", namespace)
		}
	}
	if got := workspaceArgs(t, render(t, append(slices.Clone(workspaceValues),
		"--set=workspaces.pod.enforcesNetworkPolicy=false")...)); !slices.Contains(got,
		"--pod-workspace-enforces-egress=false") {
		t.Errorf("with workspaces.pod.enforcesNetworkPolicy false the connector is given %v", got)
	}
}

func TestChart_RendersTheWorkspaceNamespaceForItsReleaseEnforcingPodSecurityRestricted(t *testing.T) {
	namespaces := typed[corev1.Namespace](t, render(t, workspaceValues...), "Namespace")
	if len(namespaces) != 1 || namespaces[0].Name != workspaceNamespace {
		t.Fatalf("the chart renders the Namespaces %+v, want %s alone", namespaces, workspaceNamespace)
	}
	labels := namespaces[0].Labels
	if got := labels["bot.garam.sh/workspace-release"]; got != releaseNamespace+"."+releaseName {
		t.Errorf("the workspace namespace's release label = %q, want %s.%s", got, releaseNamespace, releaseName)
	}
	if got := labels["pod-security.kubernetes.io/enforce"]; got != "restricted" {
		t.Errorf("the workspace namespace enforces PodSecurity %q, want restricted", got)
	}
}

func TestChart_GrantsTheConnectorThePodProvidersRightsInTheWorkspaceNamespaceAlone(t *testing.T) {
	objects := render(t, workspaceValues...)
	want := providerRights(t)
	var found []rbacv1.Role
	for key, role := range roles(t, objects) {
		if role.Namespace == workspaceNamespace {
			found = append(found, role)
			continue
		}
		if !strings.HasPrefix(role.Name, releaseName+"-connector") {
			continue
		}
		// The connector reads a Pod in a watched namespace, to bind a credential to it, and holds
		// none of the provider's other rights there: no exec, port-forward or Pod creation that reaches
		// a bot's process.
		for _, r := range rights(role.Rules) {
			if want[r] && r != (right{"", "pods", readVerbs[0]}) {
				t.Errorf("Role %s grants the Pod provider's %+v outside the workspace namespace", key, r)
			}
		}
	}
	if len(found) != 1 || found[0].Name != releaseName+"-connector-workspaces" {
		t.Fatalf("the workspace namespace holds the Roles %+v, want %s-connector-workspaces alone", found, releaseName)
	}
	granted := map[right]bool{}
	for _, rule := range found[0].Rules {
		for _, r := range rights([]rbacv1.PolicyRule{rule}) {
			if r.resource == "serviceaccounts" {
				if strings.Join(rule.ResourceNames, ",") != "default" || (r.verb != readVerbs[0] && r.verb != "update") {
					t.Errorf("the workspace Role grants %v on ServiceAccounts %v, want get and update on default alone",
						rule.Verbs, rule.ResourceNames)
				}
				continue
			}
			granted[r] = true
			if !want[r] {
				t.Errorf("the workspace Role grants %+v, which the Pod provider does not use", r)
			}
		}
	}
	for r := range want {
		if !granted[r] {
			t.Errorf("the workspace Role lacks the Pod provider's %+v", r)
		}
	}
	for _, binding := range typed[rbacv1.RoleBinding](t, objects, "RoleBinding") {
		if binding.Namespace == workspaceNamespace && binding.RoleRef.Name != found[0].Name {
			t.Errorf("RoleBinding %s binds %s in the workspace namespace", binding.Name, binding.RoleRef.Name)
		}
	}
}

func TestChart_GivesTheNaruMachinesTheirWorkspaceValuesWhereNaruIsOn(t *testing.T) {
	got := workspaceArgs(t, render(t, naruWorkspaceValues...))
	for _, arg := range []string{
		"--naru-model-gateway-url=https://gateway.naru.example",
		"--naru-tool-proxy-url=https://tools.naru.example",
		"--naru-workspace-egress=10.40.0.53/32/udp/53,10.40.1.0/24/tcp/443",
	} {
		if !slices.Contains(got, arg) {
			t.Errorf("the connector's workspace arguments %v lack %s", got, arg)
		}
	}
}

func TestChart_RefusesAWorkspaceConfigurationTheConnectorWouldRefuse(t *testing.T) {
	for name, args := range map[string][]string{
		"pod workspaces": workspaceValues, "naru workspaces": naruWorkspaceValues,
	} {
		if _, err := template(t, args...); err != nil {
			t.Fatalf("control: the whole %s configuration renders: %v", name, err)
		}
	}
	notByDigest := "example.com/garam-bot/workspace-helper@sha256:" + strings.Repeat("4", 64)
	for name, test := range map[string]struct {
		base []string
		set  []string
		want string
	}{
		"the release namespace": {workspaceValues, []string{"--set=workspaces.namespace=" + releaseNamespace},
			"is the release namespace or a watched one"},
		"a watched namespace": {workspaceValues, []string{"--set=workspaces.namespace=" + watched[1]},
			"is the release namespace or a watched one"},
		"no helper image": {workspaceValues, []string{"--set=images.workspaceHelper="},
			"images.workspaceHelper must be"},
		"a helper image not by tag and digest": {workspaceValues, []string{"--set=images.workspaceHelper=" + notByDigest},
			"images.workspaceHelper must be"},
		"an agent image not by digest": {workspaceValues, []string{"--set=images.agents.claude-code=example.com/a:1"},
			"images.agents.claude-code must be"},
		"no model gateway": {workspaceValues, []string{"--set=workspaces.pod.modelGatewayURL="},
			"workspaces.pod.modelGatewayURL is required"},
		"no tool proxy": {workspaceValues, []string{"--set=workspaces.pod.toolProxyURL="},
			"workspaces.pod.toolProxyURL is required"},
		"no egress": {workspaceValues, []string{"--set-json=workspaces.pod.egress=[]"},
			"workspaces.pod.egress must include a rule to port 53"},
		"egress without DNS": {workspaceValues, []string{
			`--set-json=workspaces.pod.egress=[{"cidr":"10.30.0.0/24","protocol":"tcp","port":54,"endPort":443}]`,
		}, "workspaces.pod.egress must include a rule to port 53"},
		"a rule with no cidr": {workspaceValues, []string{
			`--set-json=workspaces.pod.egress=[{"protocol":"udp","port":53}]`,
		}, "workspaces.pod.egress: each rule names a cidr"},
		"naru workspaces with naru off": {workspaceValues, []string{"--set=workspaces.naru.toolProxyURL=https://t.example"},
			"workspaces.naru is set where connector.naru is off"},
		"naru on with no naru model gateway": {naruWorkspaceValues, []string{"--set=workspaces.naru.modelGatewayURL="},
			"workspaces.naru.modelGatewayURL is required"},
		"naru on with no naru egress": {naruWorkspaceValues, []string{"--set-json=workspaces.naru.egress=[]"},
			"workspaces.naru.egress must include a rule to port 53"},
		"workspace values without a namespace": {workspaceValues, []string{"--set=workspaces.namespace="},
			"is set without workspaces.namespace"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := template(t, append(slices.Clone(test.base), test.set...)...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("a render with %v = %v, want refused with %q", test.set, err, test.want)
			}
		})
	}
}

// managerConnectorArgs are the manager's arguments naming the connector, its trust and both audiences.
func managerConnectorArgs(t *testing.T, objects []unstructured.Unstructured) []string {
	t.Helper()
	manager := named[appsv1.Deployment](t, objects, "Deployment", releaseNamespace, releaseName+"-manager")
	var found []string
	for _, arg := range manager.Spec.Template.Spec.Containers[0].Args {
		for _, flag := range []string{
			"--connector-url=", "--trust-configmap=", "--bot-token-audience=", "--workspace-audience=",
		} {
			if strings.HasPrefix(arg, flag) {
				found = append(found, arg)
			}
		}
	}
	return found
}

func TestChart_GivesTheManagerTheConnectorAndTheWorkspaceAudienceTheConnectorChecks(t *testing.T) {
	objects := render(t, workspaceValues...)
	want := []string{
		"--connector-url=https://garam-connector." + releaseNamespace + ".svc:8443",
		"--trust-configmap=garam-connector-trust",
		"--bot-token-audience=garam-connector",
		"--workspace-audience=garam-workspace",
	}
	if got := managerConnectorArgs(t, objects); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the manager's connector arguments = %v, want %v", got, want)
	}
	if !slices.Contains(workspaceArgs(t, objects), want[3]) {
		t.Errorf("the connector checks another audience than the manager projects: %v", workspaceArgs(t, objects))
	}

	off := render(t, "--set=connector.enabled=false")
	if got := managerConnectorArgs(t, off); len(got) != 0 {
		t.Errorf("with the connector off the manager is given %v, so every Bot would wait for its trust", got)
	}
}
