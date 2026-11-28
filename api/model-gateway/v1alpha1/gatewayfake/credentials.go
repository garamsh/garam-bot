package gatewayfake

import (
	"crypto/ecdsa"
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// Credentials issues and checks the place's credentials naming an issuer, as a cluster's projected
// ServiceAccount tokens do (ISS-1): an ES256 JWT whose sub is the issuer.
type Credentials struct {
	Key *ecdsa.PrivateKey
}

// CredentialOptions shape an issued credential, so its refusals can be shown.
type CredentialOptions struct {
	// Audience is the credential's audience. Empty is v1alpha1.Audience.
	Audience string
	// IssuedAt and Life are when it is issued and how long it lives. Zero is now and ten minutes.
	IssuedAt time.Time
	Life     time.Duration
}

// Issue is a credential naming issuer.
func (c *Credentials) Issue(issuer string, now time.Time, o CredentialOptions) (string, error) {
	if o.Audience == "" {
		o.Audience = v1alpha1.Audience
	}
	if o.IssuedAt.IsZero() {
		o.IssuedAt = now
	}
	if o.Life == 0 {
		o.Life = 10 * time.Minute
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: c.Key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(jwt.Claims{
		Subject: issuer, Audience: jwt.Audience{o.Audience},
		IssuedAt: jwt.NewNumericDate(o.IssuedAt), Expiry: jwt.NewNumericDate(o.IssuedAt.Add(o.Life)),
	}).Serialize()
}

// Verify answers the issuer credential names, where it verifies, is of v1alpha1.Audience, has not
// expired and lives at most an hour (ISS-1).
func (c *Credentials) Verify(credential string, now time.Time) (string, error) {
	parsed, err := jwt.ParseSigned(credential, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return "", errors.New("the credential is missing or no JWT")
	}
	var claims jwt.Claims
	if err := parsed.Claims(&c.Key.PublicKey, &claims); err != nil {
		return "", errors.New("the credential does not verify")
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{AnyAudience: jwt.Audience{v1alpha1.Audience}, Time: now}, 0); err != nil {
		return "", errors.New("the credential has expired or names another audience")
	}
	if claims.IssuedAt == nil || claims.Expiry == nil || claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > v1alpha1.MaxTokenLife {
		return "", errors.New("the credential lives past an hour")
	}
	return claims.Subject, nil
}
