// Package kube is the bot token domain's Keys, Pods and TrustStore over a Kubernetes API server.
package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	"github.com/go-jose/go-jose/v4"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/garamsh/garam-bot/operator/internal/bottoken"
)

// The API server's ServiceAccount issuer discovery, which every ServiceAccount may read through the
// default system:service-account-issuer-discovery binding.
const (
	discoveryPath = "/.well-known/openid-configuration"
	keysPath      = "/openid/v1/jwks"
)

// Keys reads the API server's ServiceAccount issuer and its keys.
type Keys struct {
	Kube kubernetes.Interface
}

var _ bottoken.Keys = Keys{}

func (k Keys) Keys(ctx context.Context) (bottoken.Issuer, error) {
	rest := k.Kube.Discovery().RESTClient()
	read, err := rest.Get().AbsPath(discoveryPath).DoRaw(ctx)
	if err != nil {
		return bottoken.Issuer{}, fmt.Errorf("read %s: %w", discoveryPath, err)
	}
	var discovery struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(read, &discovery); err != nil || discovery.Issuer == "" {
		return bottoken.Issuer{}, fmt.Errorf("%s names no issuer", discoveryPath)
	}
	read, err = rest.Get().AbsPath(keysPath).DoRaw(ctx)
	if err != nil {
		return bottoken.Issuer{}, fmt.Errorf("read %s: %w", keysPath, err)
	}
	var keys jose.JSONWebKeySet
	if err := json.Unmarshal(read, &keys); err != nil {
		return bottoken.Issuer{}, fmt.Errorf("%s is no key set: %w", keysPath, err)
	}
	return bottoken.Issuer{URL: discovery.Issuer, Keys: keys}, nil
}

// Pods reads Pods, and the StatefulSets that control them, from the API server: a credential's Pod is
// read as it is now, not as a cache last saw it.
type Pods struct {
	Kube kubernetes.Interface
}

var _ bottoken.Pods = Pods{}

func (p Pods) Pod(ctx context.Context, namespace, name string) (bottoken.Pod, bool, error) {
	pod, err := p.Kube.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return bottoken.Pod{}, false, nil
	case err != nil:
		return bottoken.Pod{}, false, fmt.Errorf("read the Pod %s/%s: %w", namespace, name, err)
	}
	read := bottoken.Pod{UID: string(pod.UID), ServiceAccount: pod.Spec.ServiceAccountName}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.APIVersion != "apps/v1" || owner.Kind != "StatefulSet" {
		return read, true, nil
	}
	set, err := p.Kube.AppsV1().StatefulSets(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return read, true, nil
	case err != nil:
		return bottoken.Pod{}, false, fmt.Errorf("read the StatefulSet %s/%s: %w", namespace, owner.Name, err)
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

// TrustStore is the ConfigMap Name in Namespace, under Key. The chart creates it empty, and the
// connector may only get and update it, so it never creates one.
type TrustStore struct {
	Kube      kubernetes.Interface
	Namespace string
	Name      string
	Key       string
}

var _ bottoken.TrustStore = TrustStore{}

// ErrAnotherWriter is a Publish refused because the ConfigMap changed between its read and its write.
var ErrAnotherWriter = errors.New("another process wrote the trust bundle after this one read it")

func (t TrustStore) Publish(ctx context.Context, bundle []byte) error {
	configMaps := t.Kube.CoreV1().ConfigMaps(t.Namespace)
	held, err := configMaps.Get(ctx, t.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read the ConfigMap %s/%s: %w", t.Namespace, t.Name, err)
	}
	if held.Data == nil {
		held.Data = map[string]string{}
	}
	held.Data[t.Key] = string(bundle)
	// The update carries the resourceVersion read, so it never writes over another process.
	_, err = configMaps.Update(ctx, held, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return ErrAnotherWriter
	}
	if err != nil {
		return fmt.Errorf("write the ConfigMap %s/%s: %w", t.Namespace, t.Name, err)
	}
	return nil
}
