package bottoken_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/operator/internal/bottoken"
)

func TestServe_AnswersATokenBoundToTheKeyThatSignedTheProof(t *testing.T) {
	s := start(t)
	p := newProcess(t)
	credential := s.api.credentialFor(t, watched, "mira")

	asked := s.call(t, credential, "")
	require.Equal(t, http.StatusBadRequest, asked.status)
	assert.Equal(t, "use_dpop_nonce", asked.code())
	assert.Equal(t, s.mint.server.URL+"/oauth/token", asked.body["htu"], "the proof's htu is garam's token endpoint")
	assert.Empty(t, s.mint.read(), "no proof was given, so garam is not asked")

	relayed := s.call(t, credential, p.proof(t, s.mint.server.URL+"/oauth/token", ""))
	require.Equal(t, "use_dpop_nonce", relayed.code(), "garam's nonce refusal is relayed")
	assert.Equal(t, s.mint.nonce, relayed.nonce)
	assert.Equal(t, s.mint.server.URL+"/oauth/token", relayed.body["htu"])

	proof := p.proof(t, s.mint.server.URL+"/oauth/token", relayed.nonce)
	answered := s.call(t, credential, proof)
	require.Equal(t, http.StatusOK, answered.status, answered.body)
	token, _ := answered.body["access_token"].(string)
	assert.Equal(t, "DPoP", answered.body["token_type"])
	assert.EqualValues(t, 3600, answered.body["expires_in"])
	assert.Equal(t, p.thumbprint(), s.mint.boundTo(token), "the token is bound to the process's key")

	exchanges := s.mint.read()
	require.Len(t, exchanges, 2)
	last := exchanges[1]
	assert.Equal(t, proof, last.proof, "the proof reaches garam unchanged")
	assert.Equal(t, map[string]string{
		"grant_type":         "urn:ietf:params:oauth:grant-type:token-exchange",
		"subject_token":      "grn:acme/iam/bot/decl-mira",
		"subject_token_type": "urn:garam:params:oauth:token-type:bot",
		"actor_token":        actor,
		"actor_token_type":   "urn:ietf:params:oauth:token-type:access_token",
	}, last.form, "the exchange names the bot and the cluster's token, and no scope")

	logs := s.logs.String()
	require.Contains(t, logs, "bot token answered", "the log was written")
	for name, secret := range map[string]string{"credential": credential, "proof": proof, "token": token, "actor": actor} {
		assert.NotContains(t, logs, secret, "the %s is logged", name)
	}
}

func TestServe_AnswersTheNonceItHoldsWithoutAskingGaram(t *testing.T) {
	s := start(t)
	first := s.obtain(t, s.api.credentialFor(t, watched, "mira"), newProcess(t))
	require.Equal(t, http.StatusOK, first.status, first.body)
	asked := len(s.mint.read())

	p := newProcess(t)
	again := s.call(t, s.api.credentialFor(t, watched, "mira"), p.proof(t, s.mint.server.URL+"/oauth/token", ""))

	assert.Equal(t, "use_dpop_nonce", again.code())
	assert.Equal(t, s.mint.nonce, again.nonce, "the nonce garam last answered is answered")
	assert.Len(t, s.mint.read(), asked, "garam is not asked for a nonce the server holds")
}

func TestServe_RefusesASpentCredentialWhateverKeySignsItsProof(t *testing.T) {
	s := start(t)
	credential := s.api.credentialFor(t, watched, "mira")
	require.Equal(t, http.StatusOK, s.obtain(t, credential, newProcess(t)).status)

	other := newProcess(t)
	replayed := s.call(t, credential, other.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))
	assert.Equal(t, http.StatusUnauthorized, replayed.status)
	assert.Equal(t, "invalid_credential", replayed.code())

	// Control: a fresh credential of the same ServiceAccount, with the same other key, obtains a token.
	fresh := s.call(t, s.api.credentialFor(t, watched, "mira"), other.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))
	require.Equal(t, http.StatusOK, fresh.status, fresh.body)
	token, _ := fresh.body["access_token"].(string)
	assert.Equal(t, other.thumbprint(), s.mint.boundTo(token))
}

func TestServe_ACredentialTakenBeforeItsUseObtainsOneTokenBoundToTheTakersKeyAlone(t *testing.T) {
	s := start(t)
	credential := s.api.credentialFor(t, watched, "mira")
	taker, rightful := newProcess(t), newProcess(t)

	taken := s.obtain(t, credential, taker)
	require.Equal(t, http.StatusOK, taken.status, taken.body)
	token, _ := taken.body["access_token"].(string)
	assert.Equal(t, taker.thumbprint(), s.mint.boundTo(token), "the taker's token is bound to the taker's key")
	assert.NotEqual(t, rightful.thumbprint(), s.mint.boundTo(token), "the rightful process's key is not reached")

	refused := s.call(t, credential, rightful.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))
	assert.Equal(t, http.StatusUnauthorized, refused.status, "the rightful process's request is refused, which shows the theft")
}

func TestServe_AnswersOneTokenForACredentialSentTwiceAtOnce(t *testing.T) {
	s := start(t)
	require.Equal(t, http.StatusOK, s.obtain(t, s.api.credentialFor(t, watched, "mira"), newProcess(t)).status)
	s.mint.set(nil, 200*time.Millisecond)
	credential := s.api.credentialFor(t, watched, "mira")

	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Go(func() {
			p := newProcess(t)
			statuses[i] = s.call(t, credential, p.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce)).status
		})
	}
	wg.Wait()

	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusUnauthorized}, statuses)
}

func TestServe_RefusesACredentialThatDoesNotVerify(t *testing.T) {
	s := start(t)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	subject := "system:serviceaccount:" + watched + ":mira"
	tests := []struct {
		name       string
		credential string
	}{
		{name: "missing", credential: ""},
		{name: "not a JWT", credential: "garbage"},
		{name: "signed by another key", credential: s.api.sign(t, credential{subject: subject, key: otherKey})},
		{name: "of another issuer", credential: s.api.sign(t, credential{subject: subject, issuer: "https://elsewhere"})},
		{name: "for another audience", credential: s.api.sign(t, credential{subject: subject, audience: "https://kubernetes.default.svc"})},
		{name: "expired", credential: s.api.sign(t, credential{subject: subject, expiry: time.Now().Add(-time.Minute)})},
		{name: "naming no ServiceAccount", credential: s.api.sign(t, credential{subject: "system:node:worker-1"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refused := s.call(t, tt.credential, "")
			assert.Equal(t, http.StatusUnauthorized, refused.status)
			assert.Equal(t, "invalid_credential", refused.code())
		})
	}
	// Control: the same ServiceAccount's credential, signed as the API server signs, is taken.
	accepted := s.call(t, s.api.sign(t, credential{subject: subject}), "")
	assert.Equal(t, "use_dpop_nonce", accepted.code())
	assert.Empty(t, s.mint.read(), "garam is asked for no refused credential")
}

func TestServe_RefusesABotNotGaramOwnedByThisRecord(t *testing.T) {
	s := start(t)
	s.bots[bottoken.Bot{Namespace: watched, Name: "local"}] = clusterOwned(t)
	s.bots[bottoken.Bot{Namespace: watched, Name: "foreign"}] = garamOwned(t, "grn:acme/iam/cluster/c-2", "decl-foreign")
	s.bots[bottoken.Bot{Namespace: "unwatched", Name: "mira"}] = garamOwned(t, record, "decl-unwatched")
	tests := []struct {
		name, namespace, bot string
	}{
		{name: "cluster-owned", namespace: watched, bot: "local"},
		{name: "applied by another record", namespace: watched, bot: "foreign"},
		{name: "of another namespace", namespace: otherTeam, bot: "mira"},
		{name: "in a namespace not watched", namespace: "unwatched", bot: "mira"},
		{name: "absent", namespace: watched, bot: "nobody"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refused := s.obtain(t, s.api.credentialFor(t, tt.namespace, tt.bot), newProcess(t))
			assert.Equal(t, http.StatusForbidden, refused.status)
			assert.Equal(t, "unknown_bot", refused.code())
		})
	}
	assert.Empty(t, s.mint.read(), "garam is asked for no unknown bot")
	// Control: the garam-owned Bot this record applied, in the same namespace, obtains a token.
	assert.Equal(t, http.StatusOK, s.obtain(t, s.api.credentialFor(t, watched, "mira"), newProcess(t)).status)
}

func TestServe_RelaysGaramsRefusalsAndSpendsNoCredentialOnThem(t *testing.T) {
	tests := []struct {
		name       string
		refuse     func(w http.ResponseWriter) bool
		status     int
		code       string
		retryAfter string
	}{
		{name: "bot not placed", status: http.StatusForbidden, code: "refused", refuse: func(w http.ResponseWriter) bool {
			writeAnswer(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return true
		}},
		{name: "proof refused", status: http.StatusBadRequest, code: "invalid_dpop_proof", refuse: func(w http.ResponseWriter) bool {
			writeAnswer(w, http.StatusBadRequest, map[string]string{"error": "invalid_dpop_proof"})
			return true
		}},
		{name: "limited", status: http.StatusTooManyRequests, code: "temporarily_unavailable", retryAfter: "7",
			refuse: func(w http.ResponseWriter) bool {
				w.Header().Set("Retry-After", "7")
				writeAnswer(w, http.StatusTooManyRequests, map[string]string{"error": "temporarily_unavailable"})
				return true
			}},
		{name: "failing", status: http.StatusServiceUnavailable, code: "temporarily_unavailable", refuse: func(w http.ResponseWriter) bool {
			writeAnswer(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return true
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := start(t)
			p := newProcess(t)
			credential := s.api.credentialFor(t, watched, "mira")
			s.mint.set(tt.refuse, 0)

			refused := s.call(t, credential, p.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))
			assert.Equal(t, tt.status, refused.status)
			assert.Equal(t, tt.code, refused.code())
			if tt.code == "invalid_dpop_proof" {
				assert.Equal(t, s.mint.nonce, refused.nonce, "garam's nonce is relayed with its refusal of the proof")
			}
			assert.Equal(t, tt.retryAfter, refused.retryAfter)

			s.mint.set(nil, 0)
			again := s.call(t, credential, p.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))
			assert.Equal(t, http.StatusOK, again.status, "the credential was not spent by the refusal")
		})
	}
}

func TestServe_AnswersUnavailableWhileTheClustersTokenIsNotHeld(t *testing.T) {
	s := start(t, func(c *bottoken.Config) {
		c.Actor = func(context.Context) (string, error) { return "", errors.New("no garam token is held") }
	})
	p := newProcess(t)

	refused := s.call(t, s.api.credentialFor(t, watched, "mira"), p.proof(t, s.mint.server.URL+"/oauth/token", "n"))

	assert.Equal(t, http.StatusServiceUnavailable, refused.status)
	assert.Equal(t, "temporarily_unavailable", refused.code())
	assert.Empty(t, s.mint.read())
}
