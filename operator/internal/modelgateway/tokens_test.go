package modelgateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// writer is the bot the tests issue tokens for.
var writer = gateway.TokenRequest{Namespace: "ns", Name: "writer", UID: "4f1c-writer"}

func TestTokens_RefuseATokenAtTheSecondOfItsExp(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0)
	signed, err := newTokens(key, func() time.Time { return now })
	require.NoError(t, err)
	raw, err := signed.issue(writer, time.Hour)
	require.NoError(t, err)
	now = now.Add(time.Hour - time.Second)
	_, err = signed.verify(raw)
	require.NoError(t, err, "control: a second before its exp")
	now = now.Add(time.Second)
	_, err = signed.verify(raw)
	assert.Error(t, err, "a token is taken at the second of its exp")
}

func TestTokens_NameTheirKeyAndRefuseATokenNamingNone(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	signed, err := newTokens(key, func() time.Time { return now })
	require.NoError(t, err)
	raw, err := signed.issue(writer, time.Hour)
	require.NoError(t, err)
	parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	assert.Equal(t, signed.kid, parsed.Headers[0].KeyID)
	assert.NotEmpty(t, signed.kid)
	// The same key, signing without a kid.
	bare, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	unnamed, err := jwt.Signed(bare).Claims(tokenClaims{
		Subject: writer.UID, Audience: jwt.Audience{TokenAudience},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Hour)),
		Namespace: writer.Namespace, Name: writer.Name,
	}).Serialize()
	require.NoError(t, err)
	_, err = signed.verify(unnamed)
	assert.Error(t, err, "a token naming no key is taken")
}

func TestTokens_RefuseATokenOfTheGatewaysThatLivesPastAnHour(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	signed, err := newTokens(key, func() time.Time { return now })
	require.NoError(t, err)
	hour, err := signed.issue(writer, time.Hour)
	require.NoError(t, err)
	_, err = signed.verify(hour)
	require.NoError(t, err, "control: a token of an hour")
	longer, err := signed.issue(writer, time.Hour+time.Second)
	require.NoError(t, err)
	_, err = signed.verify(longer)
	assert.Error(t, err, "a token that lives past an hour is taken, though the gateway's own key signed it")
}
