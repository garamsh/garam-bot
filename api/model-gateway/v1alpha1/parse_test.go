package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

func TestParseTokenRequest_TakesOneObjectOfItsMembers(t *testing.T) {
	request, err := v1alpha1.ParseTokenRequest([]byte(" {\"uid\":\"u\", \"namespace\":\"ns\",\"name\":\"bot\"}\n"))
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.TokenRequest{Namespace: "ns", Name: "bot", UID: "u"}, request)
}

func TestParseTokenRequest_RefusesWhatT3Refuses(t *testing.T) {
	for name, body := range map[string]string{
		"an unknown member": `{"namespace":"ns","name":"bot","uid":"u","model":"x"}`,
		"another case":      `{"Namespace":"ns","name":"bot","uid":"u"}`,
		"a member twice":    `{"namespace":"ns","name":"bot","uid":"u","name":"other"}`,
		"trailing data":     `{"namespace":"ns","name":"bot","uid":"u"}x`,
		"two objects":       `{"namespace":"ns","name":"bot","uid":"u"}{}`,
		"not a string":      `{"namespace":"ns","name":"bot","uid":1}`,
		"an array":          `[{"namespace":"ns","name":"bot","uid":"u"}]`,
		"cut short":         `{"namespace":"ns","name":"bot"`,
		"empty":             ``,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := v1alpha1.ParseTokenRequest([]byte(body))
			assert.Error(t, err)
		})
	}
}

func TestBearerToken_TakesTheSchemeInAnyCase(t *testing.T) {
	for value, want := range map[string]string{
		"Bearer abc": "abc", "bearer abc": "abc", "BEARER abc": "abc",
		"Basic abc": "", "Bearer": "", "": "", "Bearerabc": "",
	} {
		assert.Equal(t, want, v1alpha1.BearerToken(value), "%q", value)
	}
}
