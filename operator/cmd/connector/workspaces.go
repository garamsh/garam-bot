package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/operator/internal/workspaceaccess"
	"github.com/garamsh/garam-bot/pkg/podworkspace"
)

// workspaceOptions configure interface 7 and the providers it runs (ADR 0099). With namespace empty it
// is off, and every other option must be too.
type workspaceOptions struct {
	namespace         string
	audience          string
	agentImages       stringList
	helperImage       string
	podEnforcesEgress bool
	podModelURL       string
	podToolProxyURL   string
	podEgress         string
	naruModelURL      string
	naruToolProxyURL  string
	naruEgress        string
	maxDials          int
}

// stringList is a flag given once per value.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func (o *workspaceOptions) register(fs *flag.FlagSet) {
	fs.StringVar(&o.namespace, "workspace-namespace", "",
		"The one namespace every Pod workspace lives in, never a watched one. Empty, interface 7 is off.")
	fs.StringVar(&o.audience, "workspace-audience", workspaceaccess.DefaultAudience,
		"The audience a Bot's projected ServiceAccount token names for interface 7.")
	fs.Var(&o.agentImages, "agent-image",
		"A harness's workspace image, <harness>=<repository>@sha256:<digest>, once per harness.")
	fs.StringVar(&o.helperImage, "workspace-helper-image", "",
		"The Pod provider's file helper image, <repository>@sha256:<digest>.")
	fs.BoolVar(&o.podEnforcesEgress, "pod-workspace-enforces-egress", true,
		"Whether the cluster's network plugin enforces NetworkPolicy egress. "+
			"False, a Pod workspace with egress rules is refused.")
	fs.StringVar(&o.podModelURL, "pod-model-gateway-url", "", "The model gateway a Pod workspace reaches.")
	fs.StringVar(&o.podToolProxyURL, "pod-tool-proxy-url", "", "The tool proxy a Pod workspace reaches.")
	fs.StringVar(&o.podEgress, "pod-workspace-egress", "",
		"Comma-separated egress rules of every Pod workspace, each <cidr>/<protocol>/<port>[-<port>].")
	fs.StringVar(&o.naruModelURL, "naru-model-gateway-url", "", "The model gateway a naru Machine reaches.")
	fs.StringVar(&o.naruToolProxyURL, "naru-tool-proxy-url", "", "The tool proxy a naru Machine reaches.")
	fs.StringVar(&o.naruEgress, "naru-workspace-egress", "",
		"Comma-separated egress rules of every naru Machine, each <cidr>/<protocol>/<port>[-<port>].")
	fs.IntVar(&o.maxDials, "max-dials", workspaceaccess.DefaultMaxDials, "The most dials open in all (DIAL-3).")
}

// set reports whether any option but the defaults is given.
func (o workspaceOptions) set() bool {
	return o.namespace != "" || len(o.agentImages) > 0 || o.helperImage != "" || o.podModelURL != "" ||
		o.podToolProxyURL != "" || o.podEgress != "" || o.naruModelURL != "" || o.naruToolProxyURL != "" ||
		o.naruEgress != ""
}

// workspaceEnv is what interface 7 is built from beside its options.
type workspaceEnv struct {
	rest       *rest.Config
	kube       kubernetes.Interface
	release    workspaceaccess.Release
	namespaces []string
	keys       workspaceaccess.Keys
	bots       workspaceaccess.Bots
	pods       workspaceaccess.Pods
	naru       workspace.Provider
	// service and address are the bot token server's Service and listen address, whose names and port
	// are the origins a proof names.
	service string
	address string
	log     *slog.Logger
}

// workspaceAccess builds interface 7 from o, or answers nil where it is off. A partial configuration
// is refused, so the connector does not start on one.
func workspaceAccess(ctx context.Context, o workspaceOptions, env workspaceEnv) (workspaceaccess.Service, error) {
	if o.namespace == "" {
		if o.set() {
			return nil, errors.New("a workspace option is set without --workspace-namespace")
		}
		return nil, nil
	}
	if slices.Contains(append([]string{env.release.Namespace}, env.namespaces...), o.namespace) {
		return nil, fmt.Errorf("--workspace-namespace %s is the release namespace or a watched one", o.namespace)
	}
	if o.helperImage == "" || o.podModelURL == "" || o.podToolProxyURL == "" {
		return nil, errors.New("with --workspace-namespace, --workspace-helper-image, --pod-model-gateway-url and " +
			"--pod-tool-proxy-url are all required")
	}
	images, err := agentImages(o.agentImages)
	if err != nil {
		return nil, err
	}
	podEgress, err := egressRules(o.podEgress)
	if err != nil {
		return nil, fmt.Errorf("--pod-workspace-egress: %w", err)
	}
	pod, err := podworkspace.New(podworkspace.Config{
		Rest: env.rest, Namespace: o.namespace, HelperImage: o.helperImage, Egress: o.podEnforcesEgress,
	})
	if err != nil {
		return nil, fmt.Errorf("build the Pod provider: %w", err)
	}
	places := map[string]workspaceaccess.Place{
		"pod": {Provider: pod, ModelURL: o.podModelURL, ToolProxyURL: o.podToolProxyURL, Egress: podEgress},
	}
	if env.naru != nil {
		if o.naruModelURL == "" || o.naruToolProxyURL == "" {
			return nil, errors.New("with the naru Machine provider, --naru-model-gateway-url and " +
				"--naru-tool-proxy-url are required")
		}
		naruEgress, err := egressRules(o.naruEgress)
		if err != nil {
			return nil, fmt.Errorf("--naru-workspace-egress: %w", err)
		}
		places["naru"] = workspaceaccess.Place{
			Provider: env.naru, ModelURL: o.naruModelURL, ToolProxyURL: o.naruToolProxyURL, Egress: naruEgress,
		}
	} else if o.naruModelURL != "" || o.naruToolProxyURL != "" || o.naruEgress != "" {
		return nil, errors.New("a --naru-*-url or --naru-workspace-egress option is set without --naru-url")
	}
	origins, err := originsOf(env.service, env.release.Namespace, env.address)
	if err != nil {
		return nil, err
	}
	if err := unmountDefaultToken(ctx, env.kube, o.namespace); err != nil {
		return nil, err
	}
	return workspaceaccess.NewService(workspaceaccess.Config{
		Release: env.release, Namespaces: env.namespaces, Audience: o.audience, Origins: origins,
		Keys: env.keys, Pods: env.pods, Bots: env.bots, Places: places, Images: images,
		MaxDials: o.maxDials, Log: env.log,
	})
}

// unmountDefaultToken sets automountServiceAccountToken false on the workspace namespace's default
// ServiceAccount, before any workspace is served (ADR 0099 decision 4).
func unmountDefaultToken(ctx context.Context, kube kubernetes.Interface, namespace string) error {
	accounts := kube.CoreV1().ServiceAccounts(namespace)
	account, err := accounts.Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read the default ServiceAccount of %s: %w", namespace, err)
	}
	if account.AutomountServiceAccountToken != nil && !*account.AutomountServiceAccountToken {
		return nil
	}
	off := false
	account.AutomountServiceAccountToken = &off
	if _, err := accounts.Update(ctx, account, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("stop the default ServiceAccount of %s mounting its token: %w", namespace, err)
	}
	return nil
}

// agentImages reads --agent-image: each harness once, each image by digest.
func agentImages(values []string) (map[string]string, error) {
	images := make(map[string]string, len(values))
	for _, value := range values {
		harness, image, ok := strings.Cut(value, "=")
		_, digest, byDigest := strings.Cut(image, "@sha256:")
		if !ok || harness == "" || !byDigest || len(digest) != 64 {
			return nil, fmt.Errorf("--agent-image %q is not <harness>=<repository>@sha256:<digest>", value)
		}
		if _, held := images[harness]; held {
			return nil, fmt.Errorf("--agent-image names the harness %q twice", harness)
		}
		images[harness] = image
	}
	return images, nil
}

// egressRules reads a comma-separated list of <cidr>/<protocol>/<port>[-<port>].
func egressRules(value string) ([]workspace.EgressRule, error) {
	var rules []workspace.EgressRule
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		parts := strings.Split(item, "/")
		if len(parts) != 4 {
			return nil, fmt.Errorf("the rule %q is not <cidr>/<protocol>/<port>", item)
		}
		prefix, err := netip.ParsePrefix(parts[0] + "/" + parts[1])
		if err != nil || prefix != prefix.Masked() {
			return nil, fmt.Errorf("the rule %q names no canonical prefix", item)
		}
		protocol := workspace.Protocol(parts[2])
		if protocol != workspace.ProtocolTCP && protocol != workspace.ProtocolUDP {
			return nil, fmt.Errorf("the rule %q names no protocol of tcp or udp", item)
		}
		first, last, ranged := strings.Cut(parts[3], "-")
		if !ranged {
			last = first
		}
		from, errFirst := strconv.ParseUint(first, 10, 16)
		to, errLast := strconv.ParseUint(last, 10, 16)
		if errFirst != nil || errLast != nil || from == 0 || to < from {
			return nil, fmt.Errorf("the rule %q names no port or port range", item)
		}
		rules = append(rules, workspace.EgressRule{
			Prefix: prefix, Protocol: protocol, Ports: []workspace.PortRange{{First: uint16(from), Last: uint16(to)}},
		})
	}
	return rules, nil
}

// originsOf are the https origins a process reaches the server at: the Service's names, at the port
// the server listens on.
func originsOf(service, namespace, address string) ([]string, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return nil, fmt.Errorf("--bot-token-address %q names no port", address)
	}
	var origins []string
	for _, name := range serviceNames(service, namespace) {
		origins = append(origins, "https://"+net.JoinHostPort(name, port))
		if port == "443" {
			origins = append(origins, "https://"+name)
		}
	}
	return origins, nil
}
