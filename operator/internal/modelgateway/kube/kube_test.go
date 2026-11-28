package kube_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/garamsh/garam-bot/operator/internal/modelgateway/kube"
)

const (
	namespace = "garam-system"
	name      = "signing"
)

func keys(client *fake.Clientset) kube.SigningKeys {
	return kube.SigningKeys{Kube: client, Namespace: namespace, Name: name}
}

func secretOf(t *testing.T, key any) *corev1.Secret {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return &corev1.Secret{
		Namespace: namespace, Name: name,
		Data: map[string][]byte{kube.SigningKeyData: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})},
	}
}

// empty is the Secret the chart creates, holding no key.
func empty() *corev1.Secret {
	return &corev1.Secret{Namespace: namespace, Name: name, Type: corev1.SecretTypeOpaque}
}

// verbs are the verbs of the actions client took.
func verbs(client *fake.Clientset) []string {
	verbs := make([]string, 0, len(client.Actions()))
	for _, action := range client.Actions() {
		verbs = append(verbs, action.GetVerb())
	}
	return verbs
}

func TestLoad_WritesAKeyIntoTheEmptySecretOnceAndEveryLaterLoadReadsIt(t *testing.T) {
	client := fake.NewClientset(empty())
	first, err := keys(client).Load(context.Background())
	require.NoError(t, err)
	held, err := client.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, held.Data[kube.SigningKeyData], "the first load writes no key into the Secret")
	again, err := keys(client).Load(context.Background())
	require.NoError(t, err)
	assert.True(t, first.Equal(again), "a later load, as a restart or another replica makes, reads another key")
	assert.Subset(t, []string{"get", "update"}, verbs(client), "the gateway takes another verb than get and update")
}

func TestLoad_RefusesASecretThatDoesNotExistAndCreatesNone(t *testing.T) {
	client := fake.NewClientset()
	_, err := keys(client).Load(context.Background())
	require.Error(t, err)
	assert.Equal(t, []string{"get"}, verbs(client), "the gateway creates the Secret the chart did not")
}

func TestLoad_TakesTheKeyAnotherReplicaWroteFirst(t *testing.T) {
	theirs, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	client := fake.NewClientset(empty())
	// Another replica writes the Secret between this one's read and its write.
	client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		require.NoError(t, client.Tracker().Update(schema.GroupVersionResource{Version: "v1", Resource: "secrets"},
			secretOf(t, theirs), namespace))
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, name, errors.New("changed"))
	})
	loaded, err := keys(client).Load(context.Background())
	require.NoError(t, err)
	assert.True(t, theirs.Equal(loaded), "the replica that lost the write signs with a key of its own")
}

func TestLoad_RefusesASecretThatHoldsNoP256Key(t *testing.T) {
	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	garbage := secretOf(t, other)
	garbage.Data[kube.SigningKeyData] = []byte("not a key")
	for name, secret := range map[string]*corev1.Secret{"no PEM": garbage, "P-384": secretOf(t, other)} {
		t.Run(name, func(t *testing.T) {
			_, err := keys(fake.NewClientset(secret)).Load(context.Background())
			assert.Error(t, err)
		})
	}
	own, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	loaded, err := keys(fake.NewClientset(secretOf(t, own))).Load(context.Background())
	require.NoError(t, err, "control: a P-256 key is taken")
	assert.True(t, own.Equal(loaded))
}
