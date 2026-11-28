package main

import (
	"context"
	"errors"
	"flag"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/operator/internal/workspaceaccess"
)

const (
	digest = "0000000000000000000000000000000000000000000000000000000000000000"
	// watchedTeam is the release's watched namespace, and workspaces its workspace namespace; namespace
	// is its release namespace.
	watchedTeam = "game-team"
	workspaces  = "garam-workspaces"
	gatewayURL  = "https://gateway.invalid"
	// defaultAccount is the ServiceAccount every namespace is given.
	defaultAccount = "default"
)

func TestAgentImages_TakesEachHarnessOnceAndEachImageByDigest(t *testing.T) {
	images, err := agentImages([]string{"claude-code=registry.invalid/agent@sha256:" + digest})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"claude-code": "registry.invalid/agent@sha256:" + digest}, images)

	for _, refused := range [][]string{
		{"registry.invalid/agent@sha256:" + digest},
		{"=registry.invalid/agent@sha256:" + digest},
		{"claude-code=registry.invalid/agent:latest"},
		{"claude-code=registry.invalid/agent@sha256:" + digest[1:]},
		{"claude-code=registry.invalid/a@sha256:" + digest, "claude-code=registry.invalid/b@sha256:" + digest},
	} {
		_, err := agentImages(refused)
		assert.Error(t, err, "%v", refused)
	}
}

func TestEgressRules_ReadsCanonicalPrefixesProtocolsAndPorts(t *testing.T) {
	rules, err := egressRules(" 10.0.0.0/8/tcp/443, 2001:db8::/32/udp/53-54 ,")
	require.NoError(t, err)
	assert.Equal(t, []workspace.EgressRule{
		{
			Prefix: netip.MustParsePrefix("10.0.0.0/8"), Protocol: workspace.ProtocolTCP,
			Ports: []workspace.PortRange{{First: 443, Last: 443}},
		},
		{
			Prefix: netip.MustParsePrefix("2001:db8::/32"), Protocol: workspace.ProtocolUDP,
			Ports: []workspace.PortRange{{First: 53, Last: 54}},
		},
	}, rules)
	none, err := egressRules("")
	require.NoError(t, err)
	assert.Empty(t, none)

	for _, refused := range []string{
		"10.0.0.1/8/tcp/443", // not canonical
		"10.0.0.0/8/icmp/443",
		"10.0.0.0/8/tcp/0",
		"10.0.0.0/8/tcp/65536",
		"10.0.0.0/8/tcp/444-443",
		"10.0.0.0/8/tcp",
		"example.com/32/tcp/443",
	} {
		_, err := egressRules(refused)
		assert.Error(t, err, refused)
	}
}

func TestOriginsOf_NamesTheServicesNamesAtThePortTheServerListensOn(t *testing.T) {
	origins, err := originsOf("garam-connector", namespace, ":8443")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"https://garam-connector.garam-system.svc:8443", "https://garam-connector.garam-system.svc.cluster.local:8443",
	}, origins)

	origins, err = originsOf("garam-connector", namespace, "0.0.0.0:443")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"https://garam-connector.garam-system.svc:443",
		"https://garam-connector.garam-system.svc",
		"https://garam-connector.garam-system.svc.cluster.local:443",
		"https://garam-connector.garam-system.svc.cluster.local",
	}, origins, "the default port is named either way")

	_, err = originsOf("garam-connector", namespace, "8443")
	assert.Error(t, err, "an address with no port")
}

func TestWorkspaceAccess_IsOffWithoutTheNamespaceAndRefusesAPartialConfiguration(t *testing.T) {
	env := workspaceEnv{
		rest:    &rest.Config{Host: "https://kube.invalid"},
		release: workspaceaccess.Release{Namespace: namespace, Name: "garam"}, namespaces: []string{watchedTeam},
		keys: placeStub{}, pods: placeStub{}, bots: placeStub{}, liveBots: placeStub{},
		service: "garam-connector", address: ":8443",
	}
	complete := workspaceOptions{
		namespace: workspaces, helperImage: "registry.invalid/helper@sha256:" + digest,
		podModelURL: gatewayURL, podToolProxyURL: "https://tools.invalid",
	}
	// Every namespace has its default ServiceAccount, so a refusal is the option's alone.
	accounts := func() kubernetes.Interface {
		in := []string{workspaces, namespace, watchedTeam}
		all := make([]runtime.Object, 0, len(in))
		for _, in := range in {
			all = append(all, &corev1.ServiceAccount{Namespace: in, Name: defaultAccount})
		}
		return kubefake.NewClientset(all...)
	}

	off, err := workspaceAccess(context.Background(), workspaceOptions{}, env)
	require.NoError(t, err)
	assert.Nil(t, off)
	// Control: the complete configuration is served.
	kube := accounts()
	env.kube = kube
	served, err := workspaceAccess(context.Background(), complete, env)
	require.NoError(t, err)
	assert.NotNil(t, served)
	account, err := kube.CoreV1().ServiceAccounts(workspaces).Get(
		context.Background(), defaultAccount, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, account.AutomountServiceAccountToken, "the ServiceAccount is changed before a service is served")
	assert.False(t, *account.AutomountServiceAccountToken)

	tests := []struct {
		name   string
		change func(*workspaceOptions)
	}{
		{"an option without the namespace", func(o *workspaceOptions) {
			*o = workspaceOptions{given: []string{"--max-dials"}}
		}},
		{"the release namespace", func(o *workspaceOptions) { o.namespace = namespace }},
		{"a watched namespace", func(o *workspaceOptions) { o.namespace = watchedTeam }},
		{"no helper image", func(o *workspaceOptions) { o.helperImage = "" }},
		{"no model gateway", func(o *workspaceOptions) { o.podModelURL = "" }},
		{"no tool proxy", func(o *workspaceOptions) { o.podToolProxyURL = "" }},
		{"an agent image not by digest", func(o *workspaceOptions) { o.agentImages = stringList{"claude-code=agent"} }},
		{"egress that does not parse", func(o *workspaceOptions) { o.podEgress = "anywhere" }},
		{"a naru option without naru", func(o *workspaceOptions) { o.naruModelURL = gatewayURL }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := complete
			tt.change(&o)
			env.kube = accounts()
			_, err := workspaceAccess(context.Background(), o, env)
			assert.Error(t, err)
		})
	}
}

// placeStub is the place's keys, Pods and Bots, which building the service reads none of.
type placeStub struct{}

func (placeStub) Keys(context.Context) (workspaceaccess.Issuer, error) {
	return workspaceaccess.Issuer{}, nil
}
func (placeStub) Pod(context.Context, string, string) (workspaceaccess.Pod, bool, error) {
	return workspaceaccess.Pod{}, false, nil
}
func (placeStub) Served(context.Context, string) (bool, error) { return false, nil }
func (placeStub) Bot(context.Context, workspaceaccess.Bot) (workspaceaccess.Declared, bool, error) {
	return workspaceaccess.Declared{}, false, nil
}
func (placeStub) UID(context.Context, workspaceaccess.Bot) (string, bool, error) {
	return "", false, nil
}
func (placeStub) Class(context.Context, string, string) (workspaceaccess.Class, bool, error) {
	return workspaceaccess.Class{}, false, nil
}

func TestUnmountDefaultToken_StopsTheDefaultServiceAccountMountingItsToken(t *testing.T) {
	ctx := context.Background()
	kube := kubefake.NewClientset(&corev1.ServiceAccount{Namespace: workspaces, Name: defaultAccount})

	require.NoError(t, unmountDefaultToken(ctx, kube, workspaces))

	read, err := kube.CoreV1().ServiceAccounts(workspaces).Get(ctx, defaultAccount, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, read.AutomountServiceAccountToken)
	assert.False(t, *read.AutomountServiceAccountToken)
	waiting, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	assert.Error(t, unmountDefaultToken(waiting, kube, "elsewhere"), "a default ServiceAccount never made")
}

func TestUnmountDefaultToken_WaitsForTheServiceAccountAndThroughAConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	kube := kubefake.NewClientset()
	conflicts := 1
	kube.PrependReactor("update", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			return false, nil, nil
		}
		conflicts--
		return true, nil, apierrors.NewConflict(corev1.Resource("serviceaccounts"), "default", errors.New("changed"))
	})
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = kube.CoreV1().ServiceAccounts(workspaces).Create(context.Background(),
			&corev1.ServiceAccount{Namespace: workspaces, Name: defaultAccount}, metav1.CreateOptions{})
	}()

	require.NoError(t, unmountDefaultToken(ctx, kube, workspaces))

	read, err := kube.CoreV1().ServiceAccounts(workspaces).Get(ctx, defaultAccount, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, read.AutomountServiceAccountToken)
	assert.False(t, *read.AutomountServiceAccountToken)
	assert.Zero(t, conflicts, "the conflict was met")
}

func TestVisit_SeesEveryWorkspaceFlagGivenDefaultsAmongThem(t *testing.T) {
	fs := flag.NewFlagSet("connector", flag.ContinueOnError)
	var o workspaceOptions
	o.register(fs)
	fs.String("garam-url", "", "")
	require.NoError(t, fs.Parse([]string{
		"--max-dials=" + strconv.Itoa(workspaceaccess.DefaultMaxDials),
		"--workspace-audience", workspaceaccess.DefaultAudience, "--workspace-namespace", workspaces,
		"--garam-url", "https://garam.invalid",
	}))

	o.visit(fs)

	assert.ElementsMatch(t, []string{"--max-dials", "--workspace-audience"}, o.given,
		"--workspace-namespace is not counted, and another program's flag is not")
}
