package modelgateway

import (
	"crypto/ecdsa"
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// TokenAudience is the audience of the model tokens the gateway issues, and the only one it takes
// on the model surface (TOKEN-1).
const TokenAudience = "garam-model-gateway-token"

// maxToken bounds a model token read, which is a few hundred bytes.
const maxToken = 4 << 10

// tokens signs and checks the gateway's model tokens: an ES256 JWT naming the bot, opaque to everyone
// but the gateway (TOKEN-1).
type tokens struct {
	signer jose.Signer
	key    *ecdsa.PublicKey
	now    func() time.Time
}

type tokenClaims struct {
	jwt.Claims
	Namespace string `json:"ns"`
	Name      string `json:"name"`
}

func newTokens(key *ecdsa.PrivateKey, now func() time.Time) (*tokens, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return nil, err
	}
	return &tokens{signer: signer, key: &key.PublicKey, now: now}, nil
}

func (t *tokens) issue(bot gateway.TokenRequest, life time.Duration) (string, error) {
	now := t.now()
	return jwt.Signed(t.signer).Claims(tokenClaims{
		Subject: bot.UID, Audience: jwt.Audience{TokenAudience},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(life)),
		Namespace: bot.Namespace, Name: bot.Name,
	}).Serialize()
}

// verify answers the bot a token names, where the gateway's key signed it, it is of TokenAudience, it
// has not expired and it lives no longer than an hour (MOD-1).
func (t *tokens) verify(raw string) (gateway.TokenRequest, error) {
	if raw == "" || len(raw) > maxToken {
		return gateway.TokenRequest{}, errors.New("the token is missing or too long")
	}
	parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return gateway.TokenRequest{}, err
	}
	var claims tokenClaims
	if err := parsed.Claims(t.key, &claims); err != nil {
		return gateway.TokenRequest{}, err
	}
	now := t.now()
	if err := claims.ValidateWithLeeway(jwt.Expected{AnyAudience: jwt.Audience{TokenAudience}, Time: now}, 0); err != nil {
		return gateway.TokenRequest{}, err
	}
	if claims.Expiry == nil || claims.IssuedAt == nil || claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > gateway.MaxTokenLife {
		return gateway.TokenRequest{}, errors.New("the token lives longer than an hour")
	}
	bot := gateway.TokenRequest{Namespace: claims.Namespace, Name: claims.Name, UID: claims.Subject}
	return bot, bot.Check()
}
