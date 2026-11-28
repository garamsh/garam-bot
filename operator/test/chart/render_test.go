//go:build chart

// Package chart checks the connector chart (deploy/charts/garam-connector) against ADR 0091 as ADR
// 0097 and ADR 0098 amend it: what it renders, with the pinned helm, and what an API server makes of
// installing it. `make chart-check` runs it with HELM and KUBEBUILDER_ASSETS set.
package chart

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// chartDir is the chart, from this package's directory.
var chartDir = filepath.Join("..", "..", "deploy", "charts", "garam-connector")

const (
	leaseKind        = "Lease"
	releaseName      = "garam"
	releaseNamespace = "garam-system"
	claimName        = "garam-connector-claim"
	botTokenPort     = 8443
)

// fullValues is the values file every render starts from: two watched namespaces, images by digest,
// and the connector on.
var fullValues = filepath.Join(chartDir, "ci", "full-values.yaml")

// watched are fullValues's watched namespaces.
var watched = []string{"game-team", "ops-team"}

// digestForm is the image reference ADR 0091 decision 6 requires.
var digestForm = regexp.MustCompile(`^[^@\s]+:[^@\s/]+@sha256:[0-9a-f]{64}$`)

// helm is the pinned helm binary, and KUBEBUILDER_ASSETS envtest's API server; `make chart-check` sets
// both, and the package refuses to run without them rather than skip.
var helm = os.Getenv("HELM")

func TestMain(m *testing.M) {
	for _, name := range []string{"HELM", "KUBEBUILDER_ASSETS"} {
		if os.Getenv(name) == "" {
			fmt.Fprintf(os.Stderr, "%s is not set; run `make chart-check`\n", name)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// template renders the chart as release garam in garam-system from fullValues and extra arguments,
// and returns helm's output and error.
func template(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(helm, append([]string{
		"template", releaseName, chartDir, "--namespace", releaseNamespace, "--values", fullValues,
	}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return out, nil
}

// render renders the chart as template does and decodes what it renders.
func render(t *testing.T, args ...string) []unstructured.Unstructured {
	t.Helper()
	out, err := template(t, args...)
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	objects, err := decode(out)
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

func decode(stream []byte) ([]unstructured.Unstructured, error) {
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(stream), 4096)
	var objects []unstructured.Unstructured
	for {
		var object map[string]any
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			return objects, nil
		} else if err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		if object != nil {
			objects = append(objects, unstructured.Unstructured{Object: object})
		}
	}
}

// typed returns the objects of kind, converted to T.
func typed[T any](t *testing.T, objects []unstructured.Unstructured, kind string) []T {
	t.Helper()
	var found []T
	for _, object := range objects {
		if object.GetKind() != kind {
			continue
		}
		var value T
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &value); err != nil {
			t.Fatalf("%s %s: %v", kind, object.GetName(), err)
		}
		found = append(found, value)
	}
	return found
}

func named[T any](t *testing.T, objects []unstructured.Unstructured, kind, namespace, name string) T {
	t.Helper()
	for _, object := range objects {
		if object.GetKind() == kind && object.GetNamespace() == namespace && object.GetName() == name {
			return typed[T](t, []unstructured.Unstructured{object}, kind)[0]
		}
	}
	t.Fatalf("no %s %s/%s rendered", kind, namespace, name)
	var zero T
	return zero
}

// forbidden names each object that opens an inbound path from outside the cluster, or a right beyond
// named namespaces (ADR 0091 decisions 1 and 4): a ClusterRole or ClusterRoleBinding, a webhook
// configuration, an Ingress, an APIService, and a Service of type LoadBalancer or NodePort or with
// external IPs.
func forbidden(objects []unstructured.Unstructured) []string {
	var found []string
	for _, object := range objects {
		label := object.GetKind() + " " + object.GetName()
		switch object.GetKind() {
		case "ClusterRole", "ClusterRoleBinding", "ValidatingWebhookConfiguration",
			"MutatingWebhookConfiguration", "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding",
			"Ingress", "IngressClass", "Gateway", "APIService":
			found = append(found, label)
		case "Service":
			kind, _, _ := unstructured.NestedString(object.Object, "spec", "type")
			external, _, _ := unstructured.NestedStringSlice(object.Object, "spec", "externalIPs")
			if kind == string(corev1.ServiceTypeLoadBalancer) || kind == string(corev1.ServiceTypeNodePort) ||
				len(external) > 0 {
				found = append(found, label)
			}
		}
	}
	return found
}

func TestForbidden_NamesEachClusterRightAndInboundPath(t *testing.T) {
	objects, err := decode([]byte(`
kind: ClusterRole
metadata: {name: a}
---
kind: ClusterRoleBinding
metadata: {name: b}
---
kind: ValidatingWebhookConfiguration
metadata: {name: c}
---
kind: MutatingWebhookConfiguration
metadata: {name: d}
---
kind: Ingress
metadata: {name: e}
---
kind: APIService
metadata: {name: f}
---
kind: Service
metadata: {name: g}
spec: {type: LoadBalancer}
---
kind: Service
metadata: {name: h}
spec: {type: NodePort}
---
kind: Service
metadata: {name: i}
spec: {externalIPs: [192.0.2.1]}
---
kind: Service
metadata: {name: allowed}
spec: {type: ClusterIP}
---
kind: Role
metadata: {name: allowed}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ClusterRole a", "ClusterRoleBinding b", "ValidatingWebhookConfiguration c",
		"MutatingWebhookConfiguration d", "Ingress e", "APIService f", "Service g", "Service h", "Service i",
	}
	if got := forbidden(objects); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("forbidden = %q, want %q", got, want)
	}
}

// valueSets are the renders every invariant is checked on: the full values, the connector off, a
// proxy, metrics on, the release namespace watched alone, naru, the bots' workspaces, and garam off.
var valueSets = map[string][]string{
	"full":          nil,
	"connector off": {"--set=connector.enabled=false"},
	"proxy":         {"--set=proxy.url=http://proxy.example:3128", "--set=proxy.cidr=10.9.0.0/24"},
	"metrics": {
		"--set=metrics.enabled=true",
		`--set-json=metrics.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]`,
	},
	"release namespace alone": {"--set-json=watchNamespaces=[]"},
	"naru":                    naruValues,
	"workspaces":              workspaceValues,
	"naru workspaces":         naruWorkspaceValues,
	"garam off":               garamOffValues,
	"garam off workspaces":    append(slices.Clone(garamOffValues), workspaceValues...),
}

// naruValues turn the naru Machine provider on, with every value it requires.
var naruValues = []string{
	"--set=connector.naru.enabled=true",
	"--set=connector.naru.url=https://orch.example:7600",
	"--set=connector.naru.root.configMap=naru-root",
	"--set=connector.naru.proxyAddress=proxy.example:443",
	"--set=connector.naru.rootDiskBytes=4294967296",
	`--set-json=connector.naru.egress=[{"cidr":"10.20.0.0/24","port":7600},{"cidr":"10.20.1.0/24","port":443}]`,
}

func TestChart_RendersNoClusterRightAndNoInboundPathFromOutside(t *testing.T) {
	for name, args := range valueSets {
		t.Run(name, func(t *testing.T) {
			if found := forbidden(render(t, args...)); len(found) > 0 {
				t.Fatalf("the chart renders %q", found)
			}
		})
	}
}

func TestChart_RendersEachObjectOnce(t *testing.T) {
	for name, args := range valueSets {
		t.Run(name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, object := range render(t, args...) {
				key := object.GetKind() + " " + object.GetNamespace() + "/" + object.GetName()
				if seen[key] {
					t.Errorf("%s rendered twice", key)
				}
				seen[key] = true
			}
		})
	}
}

func TestChart_PinsEveryImageByDigest(t *testing.T) {
	for name, args := range valueSets {
		t.Run(name, func(t *testing.T) {
			for _, deployment := range typed[appsv1.Deployment](t, render(t, args...), "Deployment") {
				for _, container := range deployment.Spec.Template.Spec.Containers {
					images := []string{container.Image}
					for _, arg := range container.Args {
						for _, flag := range []string{"--bot-image=", "--workspace-helper-image="} {
							if image, ok := strings.CutPrefix(arg, flag); ok {
								images = append(images, image)
							}
						}
						if value, ok := strings.CutPrefix(arg, "--agent-image="); ok {
							_, image, _ := strings.Cut(value, "=")
							images = append(images, image)
						}
					}
					for _, image := range images {
						if !digestForm.MatchString(image) {
							t.Errorf("%s/%s runs %q, not pinned by digest", deployment.Name, container.Name, image)
						}
					}
				}
			}
		})
	}
}

func TestChart_RefusesAnImageNotPinnedByDigest(t *testing.T) {
	// The control: fullValues's images, in digest form, render.
	if _, err := template(t); err != nil {
		t.Fatalf("helm template with images by digest: %v", err)
	}
	for _, image := range []string{
		"manager=example.com/garam-bot/operator-manager:0123456789ab",
		"connector=example.com/garam-bot/operator-connector@sha256:" + strings.Repeat("2", 64),
		"runtime=",
	} {
		t.Run(image, func(t *testing.T) {
			_, err := template(t, "--set=images."+image)
			if err == nil || !strings.Contains(err.Error(), "must be <repository>:<tag>@sha256:<digest>") {
				t.Fatalf("helm template = %v, want the digest refusal", err)
			}
		})
	}
}

func TestChart_RunsOneConnectorAndReplacesItWithNoOverlap(t *testing.T) {
	connector := named[appsv1.Deployment](t, render(t), "Deployment", releaseNamespace, releaseName+"-connector")
	if connector.Spec.Replicas == nil || *connector.Spec.Replicas != 1 {
		t.Errorf("replicas = %v, want 1", connector.Spec.Replicas)
	}
	if connector.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("strategy = %q, want Recreate", connector.Spec.Strategy.Type)
	}
}

func TestChart_LeavesTheManagerAloneWhereTheConnectorIsOff(t *testing.T) {
	// garam's URL is unset and its root is not, which a render that still reaches the connector's
	// templates refuses.
	objects := render(t, "--set=connector.enabled=false", "--set=connector.garamURL=")
	for _, object := range objects {
		if strings.Contains(object.GetName(), "connector") && object.GetName() != claimName {
			t.Errorf("%s %s/%s rendered with the connector off", object.GetKind(), object.GetNamespace(), object.GetName())
		}
	}
	named[appsv1.Deployment](t, objects, "Deployment", releaseNamespace, releaseName+"-manager")
	claims := 0
	for _, object := range objects {
		if object.GetKind() == leaseKind {
			claims++
		}
	}
	if claims != len(watched) {
		t.Errorf("%d claims rendered with the connector off, want %d", claims, len(watched))
	}
}

// policy returns the NetworkPolicy selecting a program's Pods, and fails unless it governs both
// directions.
func policy(t *testing.T, objects []unstructured.Unstructured, program string) networkingv1.NetworkPolicy {
	t.Helper()
	var found []networkingv1.NetworkPolicy
	for _, candidate := range typed[networkingv1.NetworkPolicy](t, objects, "NetworkPolicy") {
		if candidate.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] == program {
			found = append(found, candidate)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d NetworkPolicies select the %s, want 1", len(found), program)
	}
	types := fmt.Sprint(found[0].Spec.PolicyTypes)
	if types != "[Ingress Egress]" {
		t.Fatalf("the %s's NetworkPolicy governs %s, want [Ingress Egress]", program, types)
	}
	return found[0]
}

// egress describes each egress rule as "<peer> <protocol>/<port>,...".
func egress(rules []networkingv1.NetworkPolicyEgressRule) []string {
	described := make([]string, 0, len(rules))
	for _, rule := range rules {
		var peers []string
		for _, peer := range rule.To {
			switch {
			case peer.IPBlock != nil:
				peers = append(peers, peer.IPBlock.CIDR+fmt.Sprint(peer.IPBlock.Except))
			case peer.NamespaceSelector != nil && peer.PodSelector != nil:
				peers = append(peers, fmt.Sprintf("%v/%v", peer.NamespaceSelector.MatchLabels, peer.PodSelector.MatchLabels))
			default:
				peers = append(peers, "unnarrowed peer")
			}
		}
		var ports []string
		for _, port := range rule.Ports {
			ports = append(ports, fmt.Sprintf("%s/%s", *port.Protocol, port.Port.String()))
		}
		described = append(described, strings.Join(peers, "+")+" "+strings.Join(ports, ","))
	}
	return described
}

const (
	dnsEgress = "map[kubernetes.io/metadata.name:kube-system]/map[k8s-app:kube-dns] UDP/53,TCP/53"
	apiEgress = "10.0.0.1/32[] TCP/443"
)

func TestChart_AdmitsNoIngressToTheManagerAndSendsItToTheAPIServerAlone(t *testing.T) {
	manager := policy(t, render(t), "manager")
	if len(manager.Spec.Ingress) != 0 {
		t.Errorf("the manager admits %v, want no ingress", manager.Spec.Ingress)
	}
	if got := egress(manager.Spec.Egress); strings.Join(got, ";") != apiEgress {
		t.Errorf("the manager's egress = %q, want %q", got, apiEgress)
	}
}

func TestChart_AdmitsTheManagersMetricsOnlyFromTheNamedPeersWhereTheyAreOn(t *testing.T) {
	manager := policy(t, render(t, valueSets["metrics"]...), "manager")
	if len(manager.Spec.Ingress) != 1 || len(manager.Spec.Ingress[0].From) != 1 ||
		manager.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "monitoring" ||
		len(manager.Spec.Ingress[0].Ports) != 1 || manager.Spec.Ingress[0].Ports[0].Port.IntValue() != 8080 {
		t.Errorf("the manager admits %+v, want the monitoring namespace on 8080 alone", manager.Spec.Ingress)
	}
	if _, err := template(t, "--set=metrics.enabled=true"); err == nil ||
		!strings.Contains(err.Error(), "metrics.from is required") {
		t.Errorf("metrics on with no peers = %v, want refused", err)
	}
}

func TestChart_AdmitsTheWatchedNamespacesBotsToTheConnectorsBotTokenPortAlone(t *testing.T) {
	connector := policy(t, render(t), "connector")
	if len(connector.Spec.Ingress) != 1 {
		t.Fatalf("the connector has %d ingress rules, want 1", len(connector.Spec.Ingress))
	}
	rule := connector.Spec.Ingress[0]
	if len(rule.Ports) != 1 || *rule.Ports[0].Protocol != corev1.ProtocolTCP ||
		rule.Ports[0].Port.IntValue() != botTokenPort {
		t.Errorf("the connector admits ports %v, want TCP/%d alone", rule.Ports, botTokenPort)
	}
	if len(rule.From) != 1 {
		t.Fatalf("the connector admits %d peers, want 1", len(rule.From))
	}
	peer := rule.From[0]
	if peer.IPBlock != nil || peer.NamespaceSelector == nil || peer.PodSelector == nil {
		t.Fatalf("the connector admits %+v, want bots' Pods of named namespaces", peer)
	}
	if fmt.Sprint(peer.PodSelector.MatchLabels) != "map[app.kubernetes.io/name:garam-bot]" ||
		len(peer.PodSelector.MatchExpressions) != 0 {
		t.Errorf("the connector admits Pods %v, want the bots' processes", peer.PodSelector)
	}
	namespaces := peer.NamespaceSelector
	if len(namespaces.MatchLabels) != 0 || len(namespaces.MatchExpressions) != 1 ||
		namespaces.MatchExpressions[0].Key != corev1.LabelMetadataName ||
		namespaces.MatchExpressions[0].Operator != "In" ||
		strings.Join(namespaces.MatchExpressions[0].Values, ",") != strings.Join(watched, ",") {
		t.Errorf("the connector admits namespaces %+v, want %v", namespaces, watched)
	}
}

func TestChart_SendsTheConnectorToGaramThroughTheProxyWhereOneIsSet(t *testing.T) {
	for name, test := range map[string]struct {
		args  []string
		garam string
	}{
		"no proxy": {nil, "0.0.0.0/0[] TCP/443"},
		"proxy":    {valueSets["proxy"], "10.9.0.0/24[] TCP/3128"},
	} {
		t.Run(name, func(t *testing.T) {
			connector := policy(t, render(t, test.args...), "connector")
			want := []string{test.garam, dnsEgress, apiEgress}
			if got := egress(connector.Spec.Egress); strings.Join(got, ";") != strings.Join(want, ";") {
				t.Errorf("the connector's egress = %q, want %q", got, want)
			}
		})
	}
	if _, err := template(t, "--set=proxy.url=http://proxy.example:3128"); err == nil ||
		!strings.Contains(err.Error(), "proxy.cidr is required") {
		t.Errorf("a proxy with no address range = %v, want refused", err)
	}
}

func TestChart_ClaimsEachWatchedNamespaceForItsRelease(t *testing.T) {
	for name, test := range map[string]struct {
		args       []string
		namespaces []string
	}{
		"watched":                 {nil, watched},
		"release namespace alone": {valueSets["release namespace alone"], []string{releaseNamespace}},
	} {
		t.Run(name, func(t *testing.T) {
			var claimed []string
			for _, lease := range render(t, test.args...) {
				if lease.GetKind() != leaseKind {
					continue
				}
				holder, _, _ := unstructured.NestedString(lease.Object, "spec", "holderIdentity")
				if lease.GetName() != claimName || holder != releaseNamespace+"/"+releaseName {
					t.Errorf("Lease %s/%s held by %q, want %s held by %s/%s",
						lease.GetNamespace(), lease.GetName(), holder, claimName, releaseNamespace, releaseName)
				}
				claimed = append(claimed, lease.GetNamespace())
			}
			if strings.Join(claimed, ",") != strings.Join(test.namespaces, ",") {
				t.Errorf("claimed %v, want %v", claimed, test.namespaces)
			}
		})
	}
}
