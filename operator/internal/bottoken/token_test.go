package bottoken_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/operator/internal/bottoken"
)

func TestServe_AnswersATokenBoundToTheKeyThatSignedTheProof(t *testing.T) {
	s := start(t)
	p := newProcess(t)
	credential := s.api.credentialFor(t, watched, mira)

	asked := s.call(t, credential, "")
	require.Equal(t, http.StatusBadRequest, asked.status)
	assert.Equal(t, useNonce, asked.code())
	assert.Equal(t, s.mint.server.URL+"/oauth/token", asked.body["htu"], "the proof's htu is garam's token endpoint")
	assert.Empty(t, s.mint.read(), "no proof was given, so garam is not asked")

	relayed := s.call(t, credential, p.proof(t, s.mint.server.URL+"/oauth/token", ""))
	require.Equal(t, useNonce, relayed.code(), "garam's nonce refusal is relayed")
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
	first := s.obtain(t, s.api.credentialFor(t, watched, mira), newProcess(t))
	require.Equal(t, http.StatusOK, first.status, first.body)
	asked := len(s.mint.read())

	p := newProcess(t)
	again := s.call(t, s.api.credentialFor(t, watched, mira), p.proof(t, s.mint.server.URL+"/oauth/token", ""))

	assert.Equal(t, useNonce, again.code())
	assert.Equal(t, s.mint.nonce, again.nonce, "the nonce garam last answered is answered")
	assert.Len(t, s.mint.read(), asked, "garam is not asked for a nonce the server holds")
}

func TestServe_RefusesASpentCredentialWhateverKeySignsItsProof(t *testing.T) {
	s := start(t)
	credential := s.api.credentialFor(t, watched, mira)
	require.Equal(t, http.StatusOK, s.obtain(t, credential, newProcess(t)).status)

	other := newProcess(t)
	replayed := s.call(t, credential, other.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))
	assert.Equal(t, http.StatusUnauthorized, replayed.status)
	assert.Equal(t, "invalid_credential", replayed.code())

	// Control: a fresh credential of the same ServiceAccount, with the same other key, obtains a token.
	fresh := s.call(t, s.api.credentialFor(t, watched, mira), other.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))
	require.Equal(t, http.StatusOK, fresh.status, fresh.body)
	token, _ := fresh.body["access_token"].(string)
	assert.Equal(t, other.thumbprint(), s.mint.boundTo(token))
}

func TestServe_ACredentialTakenBeforeItsUseObtainsOneTokenBoundToTheTakersKeyAlone(t *testing.T) {
	s := start(t)
	credential := s.api.credentialFor(t, watched, mira)
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
	require.Equal(t, http.StatusOK, s.obtain(t, s.api.credentialFor(t, watched, mira), newProcess(t)).status)
	s.mint.set(nil, 200*time.Millisecond)
	credential := s.api.credentialFor(t, watched, mira)

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
		{name: "living longer than an hour", credential: s.api.sign(t, credential{subject: subject,
			expiry: time.Now().Add(61 * time.Minute)})},
		{name: "issued before the server started", credential: s.api.sign(t, credential{subject: subject,
			issuedAt: time.Now().Add(-2 * time.Minute)})},
		{name: "bound to nothing", credential: s.api.sign(t, credential{subject: subject, unbound: true})},
		{name: "bound to no Pod", credential: s.api.sign(t, credential{subject: subject, bound: &kubernetesIO{
			Namespace: watched, ServiceAccount: named{Name: mira}}})},
		{name: "bound in another namespace", credential: s.api.sign(t, credential{subject: subject, bound: &kubernetesIO{
			Namespace: otherTeam, Pod: &named{Name: workloadPod(mira), UID: podUID(mira)}, ServiceAccount: named{Name: mira}}})},
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
	assert.Equal(t, useNonce, accepted.code())
	assert.Empty(t, s.mint.read(), "garam is asked for no refused credential")
}

func TestServe_RefusesABotTheConnectorDidNotApply(t *testing.T) {
	s := start(t)
	// Each has its Pod, but none is a Bot the connector applied from a declaration it holds: a Bot
	// another writer made, cluster-owned or marked as garam's; an orphan a move left; a Bot in a
	// namespace not watched.
	s.place(watched, "local", "decl-local")
	s.bots.set(bottoken.Bot{Namespace: watched, Name: "local"}, bottoken.Applied{}, false)
	s.place("unwatched", mira, "decl-unwatched")
	tests := []struct {
		name, namespace, bot string
	}{
		{name: "not applied by the connector", namespace: watched, bot: "local"},
		{name: "of another namespace", namespace: otherTeam, bot: mira},
		{name: "in a namespace not watched", namespace: "unwatched", bot: mira},
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
	// Control: the Bot the connector applied, in the same namespace, obtains a token.
	assert.Equal(t, http.StatusOK, s.obtain(t, s.api.credentialFor(t, watched, mira), newProcess(t)).status)

	// The connector no longer holds mira's declaration, a tombstone or a move.
	s.bots.set(bottoken.Bot{Namespace: watched, Name: mira}, bottoken.Applied{}, false)
	refused := s.obtain(t, s.api.credentialFor(t, watched, mira), newProcess(t))
	assert.Equal(t, "unknown_bot", refused.code(), "a Bot whose declaration is no longer held")
}

func TestServe_RefusesACredentialWhosePodIsNotTheBotsWorkload(t *testing.T) {
	tests := []struct {
		name string
		pod  *bottoken.Pod
	}{
		{name: "the Pod is gone"},
		{name: "another Pod of the same name", pod: &bottoken.Pod{UID: "pod-uid-new", ServiceAccount: mira, BotUID: botUID(mira)}},
		{name: "under another ServiceAccount", pod: &bottoken.Pod{UID: podUID(mira), ServiceAccount: "default", BotUID: botUID(mira)}},
		{name: "of no Bot's StatefulSet", pod: &bottoken.Pod{UID: podUID(mira), ServiceAccount: mira}},
		// Another writer deleted the Bot the connector made and made one of the same name, marker and
		// spec, whose StatefulSet runs this Pod.
		{name: "of a Bot made in the place of the connector's", pod: &bottoken.Pod{UID: podUID(mira), ServiceAccount: mira,
			BotUID: "bot-uid-other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := start(t)
			require.Equal(t, http.StatusOK, s.obtain(t, s.api.credentialFor(t, watched, mira), newProcess(t)).status,
				"control: the credential of the Bot's own Pod obtains a token")
			if tt.pod == nil {
				s.pods.set(watched, workloadPod(mira), bottoken.Pod{}, false)
			} else {
				s.pods.set(watched, workloadPod(mira), *tt.pod, true)
			}
			exchanges := len(s.mint.read())

			refused := s.obtain(t, s.api.credentialFor(t, watched, mira), newProcess(t))

			assert.Equal(t, http.StatusUnauthorized, refused.status)
			assert.Equal(t, "invalid_credential", refused.code())
			assert.Len(t, s.mint.read(), exchanges, "garam is asked nothing for it")
		})
	}
}

func TestServe_LimitsEachBotsExchangesAloneBeforeGaram(t *testing.T) {
	s := start(t)
	s.place(watched, "nova", "decl-nova")
	p := newProcess(t)
	htu := s.mint.server.URL + "/oauth/token"
	s.mint.set(func(w http.ResponseWriter) bool {
		writeAnswer(w, http.StatusBadRequest, map[string]string{errorKey: invalidProof})
		return true
	}, 0)
	credential := s.api.credentialFor(t, watched, mira)

	// mira's process sends proof after failed proof; each reaches garam until mira's limit is spent.
	answered := make([]int, 0, 6)
	for range 6 {
		answered = append(answered, s.call(t, credential, p.proof(t, htu, s.mint.nonce)).status)
	}
	limited := s.call(t, credential, p.proof(t, htu, s.mint.nonce))

	assert.Equal(t, []int{400, 400, 400, 400, 400, 429}, answered)
	assert.Equal(t, http.StatusTooManyRequests, limited.status)
	assert.Equal(t, unavailable, limited.code())
	assert.NotEmpty(t, limited.retryAfter)
	assert.Len(t, s.mint.read(), 5, "the limited calls reach no garam")
	s.mint.set(nil, 0)
	assert.Equal(t, http.StatusOK, s.obtain(t, s.api.credentialFor(t, watched, "nova"), newProcess(t)).status,
		"another Bot's calls are not limited by mira's")
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
			writeAnswer(w, http.StatusBadRequest, map[string]string{errorKey: "invalid_request"})
			return true
		}},
		{name: "grant refused", status: http.StatusForbidden, code: "refused", refuse: func(w http.ResponseWriter) bool {
			writeAnswer(w, http.StatusBadRequest, map[string]string{errorKey: "invalid_grant"})
			return true
		}},
		{name: "another 400", status: http.StatusServiceUnavailable, code: unavailable, refuse: func(w http.ResponseWriter) bool {
			writeAnswer(w, http.StatusBadRequest, map[string]string{errorKey: "invalid_scope"})
			return true
		}},
		{name: "403", status: http.StatusServiceUnavailable, code: unavailable, refuse: func(w http.ResponseWriter) bool {
			writeAnswer(w, http.StatusForbidden, map[string]string{errorKey: "access_denied"})
			return true
		}},
		{name: "cluster's token refused", status: http.StatusServiceUnavailable, code: unavailable,
			refuse: func(w http.ResponseWriter) bool {
				writeAnswer(w, http.StatusUnauthorized, map[string]string{errorKey: "invalid_token"})
				return true
			}},
		{name: "proof refused", status: http.StatusBadRequest, code: invalidProof, refuse: func(w http.ResponseWriter) bool {
			writeAnswer(w, http.StatusBadRequest, map[string]string{errorKey: invalidProof})
			return true
		}},
		{name: "limited", status: http.StatusTooManyRequests, code: unavailable, retryAfter: "7",
			refuse: func(w http.ResponseWriter) bool {
				w.Header().Set("Retry-After", "7")
				writeAnswer(w, http.StatusTooManyRequests, map[string]string{errorKey: unavailable})
				return true
			}},
		{name: "failing", status: http.StatusServiceUnavailable, code: unavailable, refuse: func(w http.ResponseWriter) bool {
			writeAnswer(w, http.StatusInternalServerError, map[string]string{errorKey: "server_error"})
			return true
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := start(t)
			p := newProcess(t)
			credential := s.api.credentialFor(t, watched, mira)
			s.mint.set(tt.refuse, 0)

			refused := s.call(t, credential, p.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))
			assert.Equal(t, tt.status, refused.status)
			assert.Equal(t, tt.code, refused.code())
			if tt.code == invalidProof {
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

	refused := s.call(t, s.api.credentialFor(t, watched, mira), p.proof(t, s.mint.server.URL+"/oauth/token", "n"))

	assert.Equal(t, http.StatusServiceUnavailable, refused.status)
	assert.Equal(t, unavailable, refused.code())
	assert.Empty(t, s.mint.read())
}

func TestServe_ExchangesOnceMoreUnderAFreshClusterTokenOn401(t *testing.T) {
	s := start(t)
	p := newProcess(t)
	refusedOnce := false
	s.mint.set(func(w http.ResponseWriter) bool {
		if refusedOnce {
			return false
		}
		refusedOnce = true
		writeAnswer(w, http.StatusUnauthorized, map[string]string{errorKey: "invalid_token"})
		return true
	}, 0)

	obtained := s.call(t, s.api.credentialFor(t, watched, mira), p.proof(t, s.mint.server.URL+"/oauth/token", s.mint.nonce))

	assert.Equal(t, http.StatusOK, obtained.status)
	assert.Equal(t, []string{actor}, s.invalidated.all(), "the refused cluster token is dropped")
	exchanges := s.mint.read()
	require.Len(t, exchanges, 2)
	assert.Equal(t, exchanges[0].proof, exchanges[1].proof, "the same proof, which garam did not spend")
}

func TestServe_AnswersUnavailableToAPanic(t *testing.T) {
	s := start(t, func(c *bottoken.Config) { c.Pods = panicking{} })

	refused := s.obtain(t, s.api.credentialFor(t, watched, mira), newProcess(t))

	assert.Equal(t, http.StatusServiceUnavailable, refused.status)
	assert.Equal(t, unavailable, refused.code(), "an interface 6 code (T-3)")
}

type panicking struct{}

func (panicking) Pod(context.Context, string, string) (bottoken.Pod, bool, error) { panic("a bug") }

func TestServe_NamesTheTokenURLGaramNamesNow(t *testing.T) {
	var mu sync.Mutex
	tokenURL := "https://garam.example/oauth/token"
	s := start(t, func(c *bottoken.Config) {
		c.TokenURL = func() string { mu.Lock(); defer mu.Unlock(); return tokenURL }
	})
	first := s.call(t, s.api.credentialFor(t, watched, mira), "")
	mu.Lock()
	tokenURL = "https://garam.example/v2/oauth/token"
	mu.Unlock()

	second := s.call(t, s.api.credentialFor(t, watched, mira), "")

	assert.Equal(t, "https://garam.example/oauth/token", first.body["htu"])
	assert.Equal(t, "https://garam.example/v2/oauth/token", second.body["htu"], "read for each call, as a rotation may change it")
}

func TestServe_ReadsTheAPIServersKeysOnceForCredentialsArrivingAtOnce(t *testing.T) {
	counted := &countingKeys{}
	s := start(t, func(c *bottoken.Config) {
		counted.keys = c.Keys
		c.Keys = counted
	})
	credentials := make([]string, 8)
	for i := range credentials {
		credentials[i] = s.api.credentialFor(t, watched, mira)
	}

	var wg sync.WaitGroup
	for _, credential := range credentials {
		wg.Go(func() { s.call(t, credential, "") })
	}
	wg.Wait()

	assert.Equal(t, int32(1), counted.reads.Load(), "one read serves every credential that waited on it")
}

// countingKeys counts the reads of the API server's keys, each taking a while.
type countingKeys struct {
	keys  bottoken.Keys
	reads atomic.Int32
}

func (c *countingKeys) Keys(ctx context.Context) (bottoken.Issuer, error) {
	c.reads.Add(1)
	time.Sleep(100 * time.Millisecond)
	return c.keys.Keys(ctx)
}

func TestServe_RefusesAnAuthorizationWithoutTheBearerScheme(t *testing.T) {
	s := start(t)
	request, err := http.NewRequest(http.MethodPost, s.url, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", s.api.credentialFor(t, watched, mira))

	response, err := s.client.Do(request)
	require.NoError(t, err)
	_ = response.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}
