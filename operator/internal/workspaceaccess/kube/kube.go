// Package kube is the workspace access domain's Pods and Bots over a Kubernetes API server.
package kube

import (
	"context"
	"fmt"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/garamsh/garam-bot/operator/internal/workspaceaccess"
)

// RuntimeContainer is the container of a Bot's Pod its process runs in, whose ID names the workload
// instance (ADR 0099 decision 2).
const RuntimeContainer = "runtime"

// Pods reads Pods, and the StatefulSets that control them, from the API server: a credential's Pod is
// read as it is now, not as a cache last saw it.
type Pods struct {
	Kube kubernetes.Interface
}

var _ workspaceaccess.Pods = Pods{}

func (p Pods) Pod(ctx context.Context, namespace, name string) (workspaceaccess.Pod, bool, error) {
	pod, err := p.Kube.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return workspaceaccess.Pod{}, false, nil
	case err != nil:
		return workspaceaccess.Pod{}, false, fmt.Errorf("read the Pod %s/%s: %w", namespace, name, err)
	}
	read := workspaceaccess.Pod{UID: string(pod.UID), ServiceAccount: pod.Spec.ServiceAccountName}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == RuntimeContainer && status.State.Running != nil && pod.DeletionTimestamp == nil {
			read.Container = status.ContainerID
		}
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.APIVersion != "apps/v1" || owner.Kind != "StatefulSet" {
		return read, true, nil
	}
	set, err := p.Kube.AppsV1().StatefulSets(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return read, true, nil
	case err != nil:
		return workspaceaccess.Pod{}, false, fmt.Errorf("read the StatefulSet %s/%s: %w", namespace, owner.Name, err)
	}
	if set.UID != owner.UID {
		return read, true, nil
	}
	if bot := metav1.GetControllerOf(set); bot != nil && bot.APIVersion == botv1.GroupVersion.String() &&
		bot.Kind == "Bot" {
		read.BotUID = string(bot.UID)
	}
	return read, true, nil
}

// Claims reads a namespace's claim: the release it names, and whether it exists.
type Claims interface {
	Claim(ctx context.Context, namespace string) (string, bool, error)
}

// Bots reads Bots and WorkspaceClasses from the cache of the watched namespaces, and their claims.
type Bots struct {
	Reader client.Reader
	Claims Claims
	// Release is this release's <release-namespace>/<release-name>, which a claim names.
	Release string
}

var _ workspaceaccess.Bots = Bots{}

func (b Bots) Served(ctx context.Context, namespace string) (bool, error) {
	holder, exists, err := b.Claims.Claim(ctx, namespace)
	if err != nil {
		return false, err
	}
	return exists && holder == b.Release, nil
}

func (b Bots) Bot(ctx context.Context, bot workspaceaccess.Bot) (workspaceaccess.Declared, bool, error) {
	var read botv1.Bot
	err := b.Reader.Get(ctx, client.ObjectKey{Namespace: bot.Namespace, Name: bot.Name}, &read)
	switch {
	case apierrors.IsNotFound(err):
		return workspaceaccess.Declared{}, false, nil
	case err != nil:
		return workspaceaccess.Declared{}, false, fmt.Errorf("read the Bot %s: %w", bot, err)
	case read.DeletionTimestamp != nil:
		return workspaceaccess.Declared{}, false, nil
	}
	declared := workspaceaccess.Declared{
		UID: string(read.UID), Harness: read.Spec.Agent.Harness, Attachments: map[string]workspaceaccess.Attachment{},
	}
	for _, attachment := range read.Spec.Workspaces {
		declared.Attachments[attachment.Name] = attachmentOf(attachment)
	}
	for _, tool := range read.Spec.Permissions.Tools {
		declared.Tools = append(declared.Tools, tool.Name)
	}
	return declared, true, nil
}

// attachmentOf is an attachment as this release serves it: by template with the Bot lifetime alone
// (interface 7's open questions).
func attachmentOf(a botv1.WorkspaceAttachment) workspaceaccess.Attachment {
	switch {
	case a.Template == nil:
		return workspaceaccess.Attachment{Unsupported: "an attachment by ref is not served in this release"}
	case a.Template.Lifetime != botv1.LifetimeBot:
		return workspaceaccess.Attachment{Unsupported: fmt.Sprintf("the %s lifetime is not served in this release", a.Template.Lifetime)}
	}
	return workspaceaccess.Attachment{Class: a.Template.Class}
}

func (b Bots) Class(ctx context.Context, namespace, name string) (workspaceaccess.Class, bool, error) {
	var read botv1.WorkspaceClass
	err := b.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &read)
	switch {
	case apierrors.IsNotFound(err):
		return workspaceaccess.Class{}, false, nil
	case err != nil:
		return workspaceaccess.Class{}, false, fmt.Errorf("read the WorkspaceClass %s/%s: %w", namespace, name, err)
	}
	r := read.Spec.Resources
	return workspaceaccess.Class{Provider: read.Spec.Provider, Resources: workspace.Resources{
		CPU: uint32(wholeCPUs(r.CPU)), MemoryBytes: bytesOf(r.Memory), DiskBytes: bytesOf(r.Disk),
	}}, true, nil
}

// wholeCPUs is q rounded up to whole CPUs, as interface 3's Resources.CPU is.
func wholeCPUs(q resource.Quantity) int64 {
	return max(0, (q.MilliValue()+999)/1000)
}

// bytesOf is q in bytes, rounded up, and 0 where it is negative.
func bytesOf(q resource.Quantity) uint64 {
	return uint64(max(0, q.Value()))
}

// LiveBots reads Bots from the API server, through a reader no cache stands behind, so the deletion
// sweep never presumes a live Bot absent (ADR 0099 decision 6).
type LiveBots struct {
	Reader client.Reader
}

var _ workspaceaccess.LiveBots = LiveBots{}

// NewLiveBots reads Bots through a client of cfg that has no cache.
func NewLiveBots(cfg *rest.Config) (LiveBots, error) {
	scheme := runtime.NewScheme()
	if err := botv1.AddToScheme(scheme); err != nil {
		return LiveBots{}, err
	}
	reader, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return LiveBots{}, fmt.Errorf("build the live Bot client: %w", err)
	}
	return LiveBots{Reader: reader}, nil
}

func (l LiveBots) UID(ctx context.Context, bot workspaceaccess.Bot) (string, bool, error) {
	var read botv1.Bot
	err := l.Reader.Get(ctx, client.ObjectKey{Namespace: bot.Namespace, Name: bot.Name}, &read)
	switch {
	case apierrors.IsNotFound(err):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("read the Bot %s: %w", bot, err)
	}
	return string(read.UID), true, nil
}
