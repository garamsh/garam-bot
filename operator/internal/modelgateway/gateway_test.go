package modelgateway_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
	"github.com/garamsh/garam-bot/api/model-gateway/v1alpha1/gatewayfake"

	"github.com/garamsh/garam-bot/operator/internal/modelgateway"
)

const (
	apiIssuer = "https://kubernetes.default.svc"
	issuer    = "garam-system/garam-connector"
)

// apiServer stands in for the API server's keys: it answers the keys it holds now, and counts the
// reads.
type apiServer struct {
	mu    sync.Mutex
	keys  map[string]*ecdsa.PrivateKey
	fail  bool
	reads int
}

func (a *apiServer) Keys(context.Context) (modelgateway.Issuer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reads++
	if a.fail {
		return modelgateway.Issuer{}, errors.New("the API server is unreachable")
	}
	set := jose.JSONWebKeySet{}
	for kid, key := range a.keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: &key.PublicKey, KeyID: kid, Algorithm: string(jose.ES256), Use: "sig"})
	}
	return modelgateway.Issuer{URL: apiIssuer, Keys: set}, nil
}

func (a *apiServer) set(kid string, key *ecdsa.PrivateKey, fail bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if key != nil {
		a.keys[kid] = key
	}
	a.fail = fail
}

func (a *apiServer) readCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reads
}

type staticKey struct{ key *ecdsa.PrivateKey }

func (s staticKey) Load(context.Context) (*ecdsa.PrivateKey, error) { return s.key, nil }

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}

// credential is a projected ServiceAccount token naming sub, signed by key under kid.
func credential(t *testing.T, key *ecdsa.PrivateKey, kid, iss, sub string, now time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), kid))
	require.NoError(t, err)
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: iss, Subject: sub, Audience: jwt.Audience{gateway.Audience},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}).Serialize()
	require.NoError(t, err)
	return raw
}

type rig struct {
	gateway *modelgateway.Gateway
	api     *apiServer
	now     time.Time
	mu      sync.Mutex
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
}

func newRig(t *testing.T, api *apiServer) *rig {
	t.Helper()
	provider := httptest.NewServer(&gatewayfake.Provider{})
	t.Cleanup(provider.Close)
	upstream, err := url.Parse(provider.URL)
	require.NoError(t, err)
	r := &rig{api: api, now: time.Now()}
	r.gateway, err = modelgateway.New(context.Background(), modelgateway.Config{
		Upstream: upstream, Key: "sk-test", SigningKeys: staticKey{newKey(t)}, Keys: api, Issuer: issuer,
		Record: func(modelgateway.Usage) {}, Now: r.clock,
	})
	require.NoError(t, err)
	return r
}

func (r *rig) issue(t *testing.T, cred string) (int, gateway.ErrorBody) {
	t.Helper()
	body, err := json.Marshal(gateway.TokenRequest{Namespace: "ns", Name: "bot", UID: "uid"})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, gateway.TokensPath, bytes.NewReader(body))
	request.Header.Set(gateway.HeaderAuthorization, "Bearer "+cred)
	answer := httptest.NewRecorder()
	r.gateway.ServeHTTP(answer, request)
	var refusal gateway.ErrorBody
	_ = json.Unmarshal(answer.Body.Bytes(), &refusal)
	return answer.Code, refusal
}

func TestIssue_ReadsTheAPIServersKeysAgainForAKeyItDoesNotHoldOnlyAfterAWhile(t *testing.T) {
	first, rotated := newKey(t), newKey(t)
	api := &apiServer{keys: map[string]*ecdsa.PrivateKey{"first": first}}
	r := newRig(t, api)
	const sub = "system:serviceaccount:garam-system:garam-connector"
	status, _ := r.issue(t, credential(t, first, "first", apiIssuer, sub, r.clock()))
	require.Equal(t, http.StatusOK, status, "control: a credential of a key the API server holds")
	reads := api.readCount()

	// The API server rotates to a key the gateway has not read.
	api.set("rotated", rotated, false)
	signed := credential(t, rotated, "rotated", apiIssuer, sub, r.clock())
	status, _ = r.issue(t, signed)
	assert.Equal(t, http.StatusUnauthorized, status, "within 10 s of a read, a key not held is refused")
	assert.Equal(t, reads, api.readCount(), "within 10 s of a read, a key not held reads the API server again")
	r.advance(11 * time.Second)
	status, refusal := r.issue(t, signed)
	assert.Equal(t, http.StatusOK, status, "a while later, the rotated key's credential is refused: %+v", refusal)
	assert.Equal(t, reads+1, api.readCount(), "a key not held is read once")

	unknown := credential(t, newKey(t), "unknown", apiIssuer, sub, r.clock())
	for range 3 {
		status, _ = r.issue(t, unknown)
		assert.Equal(t, http.StatusUnauthorized, status)
	}
	assert.Equal(t, reads+1, api.readCount(), "a key no one holds reads the API server on every call")
}

func TestIssue_AnswersUnreadableKeys503AndRefusesNoCredentialForThem(t *testing.T) {
	key := newKey(t)
	api := &apiServer{keys: map[string]*ecdsa.PrivateKey{"k": key}, fail: true}
	r := newRig(t, api)
	cred := credential(t, key, "k", apiIssuer, "system:serviceaccount:garam-system:garam-connector", r.clock())
	status, refusal := r.issue(t, cred)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, gateway.CodeTemporarilyUnavailable, refusal.Error)
	api.set("", nil, false)
	status, _ = r.issue(t, cred)
	assert.Equal(t, http.StatusOK, status, "control: once the keys are read, the same credential is taken")
}

func TestIssue_TakesTheConnectorsServiceAccountAlone(t *testing.T) {
	key := newKey(t)
	r := newRig(t, &apiServer{keys: map[string]*ecdsa.PrivateKey{"k": key}})
	for sub, want := range map[string]int{
		"system:serviceaccount:garam-system:garam-connector":   http.StatusOK,
		"system:serviceaccount:garam-system:another":           http.StatusForbidden,
		"system:serviceaccount:another-ns:garam-connector":     http.StatusForbidden,
		"system:serviceaccount:garam-system:garam-connector:x": http.StatusUnauthorized,
		"garam-system/garam-connector":                         http.StatusUnauthorized,
		"system:serviceaccount::garam-connector":               http.StatusUnauthorized,
	} {
		status, _ := r.issue(t, credential(t, key, "k", apiIssuer, sub, r.clock()))
		assert.Equal(t, want, status, "%s", sub)
	}
	status, _ := r.issue(t, credential(t, key, "k", "https://another.issuer", "system:serviceaccount:garam-system:garam-connector", r.clock()))
	assert.Equal(t, http.StatusUnauthorized, status, "a credential of another issuer than the API server's")
}

func TestNew_RefusesAGatewayWithNoKeyOrIssuer(t *testing.T) {
	upstream, err := url.Parse("https://api.anthropic.com")
	require.NoError(t, err)
	base := modelgateway.Config{
		Upstream: upstream, Key: "sk-test", SigningKeys: staticKey{newKey(t)}, Keys: &apiServer{}, Issuer: issuer,
		Record: func(modelgateway.Usage) {},
	}
	_, err = modelgateway.New(context.Background(), base)
	require.NoError(t, err, "control")
	for name, change := range map[string]func(*modelgateway.Config){
		"no key":           func(c *modelgateway.Config) { c.Key = "" },
		"no upstream":      func(c *modelgateway.Config) { c.Upstream = nil },
		"no issuer":        func(c *modelgateway.Config) { c.Issuer = "" },
		"issuer of no ns":  func(c *modelgateway.Config) { c.Issuer = "garam-connector" },
		"issuer with a :":  func(c *modelgateway.Config) { c.Issuer = "garam-system/garam:connector" },
		"no usage records": func(c *modelgateway.Config) { c.Record = nil },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			change(&cfg)
			_, err := modelgateway.New(context.Background(), cfg)
			assert.Error(t, err)
		})
	}
}
