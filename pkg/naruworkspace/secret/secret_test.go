package secret_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/garamsh/garam-bot/pkg/naruworkspace"
	"github.com/garamsh/garam-bot/pkg/naruworkspace/secret"
)

func emptySecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "release", Name: "naru-caller"}}
}

func TestStore_KeepsTheCredentialInTheSecret(t *testing.T) {
	client := fake.NewClientset(emptySecret())
	store := secret.New(client, "release", "naru-caller")
	_, err := store.Load(t.Context())
	require.ErrorIs(t, err, naruworkspace.ErrNoCredential, "the chart creates the Secret empty")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	attempt := naruworkspace.Credential{Key: key, Attempted: "digest"}
	require.NoError(t, store.Save(t.Context(), attempt))
	got, err := secret.New(client, "release", "naru-caller").Load(t.Context())
	require.NoError(t, err)
	assert.True(t, got.Key.Equal(key))
	assert.Equal(t, "digest", got.Attempted)
	assert.Empty(t, got.Certificate)

	registered := naruworkspace.Credential{Key: key, Certificate: []byte("der")}
	require.NoError(t, store.Save(t.Context(), registered))
	got, err = secret.New(client, "release", "naru-caller").Load(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []byte("der"), got.Certificate)
	assert.Empty(t, got.Attempted, "a saved credential replaces the attempt")
	for _, a := range client.Actions() {
		assert.NotEqual(t, "create", a.GetVerb(), "the Store never creates a Secret")
	}
}

func TestStore_RefusesAMissingSecret(t *testing.T) {
	_, err := secret.New(fake.NewClientset(), "release", "naru-caller").Load(t.Context())
	require.Error(t, err)
	require.NotErrorIs(t, err, naruworkspace.ErrNoCredential, "a missing Secret is not an empty one")
	// control: the Secret present and empty is ErrNoCredential
	_, err = secret.New(fake.NewClientset(emptySecret()), "release", "naru-caller").Load(t.Context())
	require.ErrorIs(t, err, naruworkspace.ErrNoCredential)
}
