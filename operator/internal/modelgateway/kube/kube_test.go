package kube_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
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

func keys(client *fake.Clientset) kube.SigningKeys {
	return kube.SigningKeys{Kube: client, Namespace: "garam-system", Name: "signing"}
}

func secretOf(t *testing.T, key any) *corev1.Secret {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return &corev1.Secret{
		Namespace: "garam-system", Name: "signing",
		Data: map[string][]byte{kube.SigningKeyData: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})},
	}
}

func TestLoad_WritesAKeyOnceAndEveryLaterLoadReadsIt(t *testing.T) {
	client := fake.NewClientset()
	first, err := keys(client).Load(context.Background())
	require.NoError(t, err)
	held, err := client.CoreV1().Secrets("garam-system").Get(context.Background(), "signing", metav1.GetOptions{})
	require.NoError(t, err, "the first load writes the Secret")
	assert.Equal(t, corev1.SecretTypeOpaque, held.Type)
	again, err := keys(client).Load(context.Background())
	require.NoError(t, err)
	assert.True(t, first.Equal(again), "a later load, as a restart or another replica makes, reads another key")
}

func TestLoad_TakesTheKeyAnotherReplicaWroteFirst(t *testing.T) {
	theirs, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	client := fake.NewClientset()
	// Another replica writes the Secret between this one's read and its write.
	client.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		require.NoError(t, client.Tracker().Add(secretOf(t, theirs)))
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, "signing")
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
