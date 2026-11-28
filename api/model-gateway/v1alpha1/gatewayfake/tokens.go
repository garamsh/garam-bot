package gatewayfake

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// tokenAudience is the audience of the model tokens this gateway issues (TOKEN-1).
const tokenAudience = "garam-model-gateway-token"

// tokens signs and checks the gateway's model tokens: an ES256 JWT naming the bot, of the gateway's
// own audience, opaque to everyone else (TOKEN-1).
type tokens struct {
	key *ecdsa.PrivateKey
	now func() time.Time
}

type tokenClaims struct {
	jwt.Claims
	Namespace string `json:"ns"`
	Name      string `json:"name"`
}

func (t *tokens) issue(bot v1alpha1.TokenRequest, life time.Duration) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: t.key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	now := t.now()
	return jwt.Signed(signer).Claims(tokenClaims{
		Claims: jwt.Claims{
			Subject: bot.UID, Audience: jwt.Audience{tokenAudience},
			IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(life)),
		},
		Namespace: bot.Namespace, Name: bot.Name,
	}).Serialize()
}

func (t *tokens) verify(token string) (v1alpha1.TokenRequest, error) {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return v1alpha1.TokenRequest{}, err
	}
	var claims tokenClaims
	if err := parsed.Claims(&t.key.PublicKey, &claims); err != nil {
		return v1alpha1.TokenRequest{}, err
	}
	now := t.now()
	if err := claims.ValidateWithLeeway(jwt.Expected{AnyAudience: jwt.Audience{tokenAudience}, Time: now}, 0); err != nil {
		return v1alpha1.TokenRequest{}, err
	}
	if claims.Expiry == nil || claims.IssuedAt == nil || claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > v1alpha1.MaxTokenLife {
		return v1alpha1.TokenRequest{}, errors.New("the token lives past an hour")
	}
	bot := v1alpha1.TokenRequest{Namespace: claims.Namespace, Name: claims.Name, UID: claims.Subject}
	return bot, bot.Check()
}

// NewSigningKey returns a new key to sign model tokens with.
func NewSigningKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}
