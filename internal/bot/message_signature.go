package bot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// The contract of a message's signature (garam ADR-0101).
const (
	signatureType = "agent-message+jwt"
	// signingKeysPath is where garam's machine listener serves the keys it signs messages with.
	signingKeysPath = "/message-signing-keys"
	// signatureSkew is the whole skew allowance garam's validity assumes.
	signatureSkew = 60 * time.Second
	// keysLife is how long a read of the keys stands, its Cache-Control max-age; keysGrace how long
	// more they are used while a read fails; and keysRefetch how often a kid not held reads them again.
	keysLife    = time.Hour
	keysGrace   = 24 * time.Hour
	keysRefetch = time.Minute
)

// errSignature is a delivery whose signature garam's contract refuses.
var errSignature = errors.New("the delivery's signature is refused")

// messageKeys verifies the signature of each message garam hands the bot against the keys garam
// publishes, read from its machine listener and held for an hour (ADR-0101 §What a verifier checks).
type messageKeys struct {
	url    string // where the keys are read
	issuer string // the one issuer trusted, garam's machine origin
	client func() (*http.Client, error)
	now    func() time.Time

	mu      sync.Mutex
	set     jose.JSONWebKeySet
	readAt  time.Time // when the set held was read
	triedAt time.Time // when a read was last tried
}

// signedMessage is what a verified signature says of its message.
type signedMessage struct {
	// Sender is the GRN garam vouches for as the message's sender, its sub.
	Sender string
}

// verify checks signature over content, of the message id addressed to audience, as ADR-0101's steps
// 1 to 6 do. The replay of step 7 is the task's identifier's (TASK-1).
func (k *messageKeys) verify(ctx context.Context, signature, content, id, audience string) (signedMessage, error) {
	parsed, err := jose.ParseSignedCompact(signature, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(parsed.Signatures) != 1 {
		return signedMessage{}, fmt.Errorf("%w: not one compact ES256 JWS", errSignature)
	}
	// Step 1: the issuer, read unverified only to choose the keys, before anything is fetched.
	var unverified struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(parsed.UnsafePayloadWithoutVerification(), &unverified); err != nil || unverified.Issuer != k.issuer {
		return signedMessage{}, fmt.Errorf("%w: an issuer not trusted", errSignature)
	}
	// Step 2: the header.
	header := parsed.Signatures[0].Protected
	if header.Algorithm != string(jose.ES256) || header.ExtraHeaders[jose.HeaderType] != signatureType || header.KeyID == "" {
		return signedMessage{}, fmt.Errorf("%w: a header other than ES256 %s with a kid", errSignature, signatureType)
	}
	key, err := k.key(ctx, header.KeyID)
	if err != nil {
		return signedMessage{}, err
	}
	// Step 3: the signature.
	payload, err := parsed.Verify(key)
	if err != nil {
		return signedMessage{}, fmt.Errorf("%w: it does not verify", errSignature)
	}
	var claims struct {
		Issuer        string `json:"iss"`
		Subject       string `json:"sub"`
		Audience      any    `json:"aud"`
		MessageID     string `json:"message_id"`
		ContentSHA256 string `json:"content_sha256"`
		IssuedAt      *int64 `json:"iat"`
		Expires       *int64 `json:"exp"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&claims); err != nil || claims.Issuer != k.issuer || claims.Subject == "" ||
		claims.IssuedAt == nil || claims.Expires == nil {
		return signedMessage{}, fmt.Errorf("%w: claims other than the contract's", errSignature)
	}
	// Step 4: the audience, one string.
	if aud, ok := claims.Audience.(string); !ok || aud != audience {
		return signedMessage{}, fmt.Errorf("%w: addressed to another audience", errSignature)
	}
	// Step 5: the time.
	now := k.now()
	if !time.Unix(*claims.Expires, 0).After(now.Add(-signatureSkew)) || !time.Unix(*claims.IssuedAt, 0).Before(now.Add(signatureSkew)) {
		return signedMessage{}, fmt.Errorf("%w: expired or issued ahead", errSignature)
	}
	// Step 6: the content and the identifier.
	digest := sha256.Sum256([]byte(content))
	if claims.ContentSHA256 != base64.RawURLEncoding.EncodeToString(digest[:]) || claims.MessageID != id {
		return signedMessage{}, fmt.Errorf("%w: another content or message", errSignature)
	}
	return signedMessage{Sender: claims.Subject}, nil
}

// key answers the key kid names, reading the set again where the one held is an hour old, or holds no
// such key, and no read was tried in the last minute. A set that cannot be read is used for 24 hours
// more, and none after.
func (k *messageKeys) key(ctx context.Context, kid string) (*jose.JSONWebKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	if (k.readAt.IsZero() || !now.Before(k.readAt.Add(keysLife))) && now.Sub(k.triedAt) >= keysRefetch {
		k.read(ctx, now)
	}
	if found := k.set.Key(kid); len(found) == 0 && now.Sub(k.triedAt) >= keysRefetch {
		k.read(ctx, now)
	}
	if k.readAt.IsZero() || !now.Before(k.readAt.Add(keysLife+keysGrace)) {
		return nil, errors.New("garam's message signing keys cannot be read")
	}
	found := k.set.Key(kid)
	if len(found) != 1 || !found[0].Valid() || found[0].Algorithm != string(jose.ES256) || !found[0].IsPublic() {
		return nil, fmt.Errorf("%w: signed with a key garam does not publish", errSignature)
	}
	return &found[0], nil
}

// read reads the set, keeping the one held where the read fails. k.mu is held.
func (k *messageKeys) read(ctx context.Context, now time.Time) {
	k.triedAt = now
	set, err := k.fetch(ctx)
	if err != nil {
		return
	}
	k.set, k.readAt = set, now
}

func (k *messageKeys) fetch(ctx context.Context) (jose.JSONWebKeySet, error) {
	client, err := k.client()
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(k.url, "/")+signingKeysPath, nil)
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	if err != nil || response.StatusCode != http.StatusOK {
		return jose.JSONWebKeySet{}, fmt.Errorf("read garam's message signing keys: %d", response.StatusCode)
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	return set, nil
}
