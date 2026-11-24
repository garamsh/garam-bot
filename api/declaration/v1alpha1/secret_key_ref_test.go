package v1alpha1_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
)

func TestParseSecretKeyRef_SEC1_SplitsANameAndAKey(t *testing.T) {
	name, key, err := v1alpha1.ParseSecretKeyRef("deploy.keys/id_ed25519.pub")
	require.NoError(t, err)
	assert.Equal(t, "deploy.keys", name)
	assert.Equal(t, "id_ed25519.pub", key)
}

func TestParseSecretKeyRef_SEC1_RefusesAnotherForm(t *testing.T) {
	for _, ref := range []string{
		"deploy-key",
		"deploy-key/",
		"/ssh",
		"other-team/deploy-key/ssh",
		"Deploy-Key/ssh",
		"deploy-key/.",
		"deploy-key/..",
		strings.Repeat("a", 254) + "/ssh",
		"deploy-key/" + strings.Repeat("k", 254),
	} {
		t.Run(ref, func(t *testing.T) {
			_, _, err := v1alpha1.ParseSecretKeyRef(ref)
			require.ErrorIs(t, err, v1alpha1.ErrInvalidSecretKeyRef)
		})
	}
}
