// Package kube is the bot token domain's Keys, Bots and TrustStore over a Kubernetes API server.
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// Bots reads Bots through reader, the connector's cache of the watched namespaces.
type Bots struct {
	Reader client.Reader
}

var _ bottoken.Bots = Bots{}

func (b Bots) Management(ctx context.Context, bot bottoken.Bot) (botv1.Management, bool, error) {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(botv1.GroupVersion.WithKind("Bot"))
	err := b.Reader.Get(ctx, client.ObjectKey{Namespace: bot.Namespace, Name: bot.Name}, object)
	switch {
	case apierrors.IsNotFound(err):
		return botv1.Management{}, false, nil
	case err != nil:
		return botv1.Management{}, false, fmt.Errorf("read the Bot %s: %w", bot, err)
	}
	management, err := botv1.ManagementOf(object)
	return management, true, err
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
