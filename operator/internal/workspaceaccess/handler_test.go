package workspaceaccess_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	"github.com/garamsh/garam-bot/operator/internal/workspaceaccess"
)

func TestHandler_AnswersAPanicWith503(t *testing.T) {
	h := newHarness(t, options{})
	p := h.instance(h.bot())
	h.provider.set(func(p *provider) { p.panicOn = "Create" })

	refused := p.acquire()

	assert.Equal(t, http.StatusServiceUnavailable, refused.status, refused.raw)
	assert.Equal(t, access.CodeTemporarilyUnavailable, refused.code)
	// Control: the same call without the panic is answered.
	h.provider.set(func(p *provider) { p.panicOn = "" })
	assert.Equal(t, http.StatusOK, p.acquire().status)
}

func TestHandler_LogsNoCredentialProofTokenOrFileContent(t *testing.T) {
	h := newHarness(t, options{})
	p := h.instance(h.bot())
	p.running()
	content := "file-content-" + strings.Repeat("x", 12)
	require.Equal(t, http.StatusNoContent, p.write(access.File{Path: token, Content: []byte(content)}).status)
	require.Equal(t, http.StatusOK, p.call(http.MethodPost, mainPath+access.TokensSuffix, nil).status)
	proof := p.proof(http.MethodPut, mainPath)
	// A refused call: a second key for the instance, its proof logged nowhere either.
	stranger := *p
	stranger.key = h.key()
	require.Equal(t, http.StatusUnauthorized, stranger.acquire().status)

	logged := h.log.String()

	// Control: the calls are logged, with the Bot.
	assert.Contains(t, logged, "workspace call answered")
	assert.Contains(t, logged, "workspace call refused")
	assert.Contains(t, logged, mira)
	h.issuer.mu.Lock()
	secrets := append([]string{p.credential, strings.Split(p.credential, ".")[2], proof, content,
		base64.StdEncoding.EncodeToString([]byte(content))}, h.issuer.issued...)
	h.issuer.mu.Unlock()
	require.Len(t, secrets, 7) // setup: a model token and a tool token were issued
	for _, secret := range secrets {
		assert.NotContains(t, logged, secret)
	}
}

func TestHandler_RefusesACredentialNotBoundToARunningPodOfTheBot(t *testing.T) {
	tests := []struct {
		name   string
		change func(pod *workspaceaccess.Pod)
		status int
		code   access.Code
	}{
		{"another Pod under the name", func(pod *workspaceaccess.Pod) { pod.UID = "another" },
			http.StatusUnauthorized, access.CodeInvalidCredential},
		{"another ServiceAccount", func(pod *workspaceaccess.Pod) { pod.ServiceAccount = "stranger" },
			http.StatusUnauthorized, access.CodeInvalidCredential},
		{"a runtime container that does not run", func(pod *workspaceaccess.Pod) { pod.Container = "" },
			http.StatusUnauthorized, access.CodeInvalidCredential},
		{"no Bot controlling it", func(pod *workspaceaccess.Pod) { pod.BotUID = "" },
			http.StatusForbidden, access.CodeUnknownBot},
		{"the Bot's earlier incarnation controlling it", func(pod *workspaceaccess.Pod) { pod.BotUID = "earlier" },
			http.StatusForbidden, access.CodeUnknownBot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, options{bindingFresh: time.Nanosecond})
			p := h.instance(h.bot())
			// Control: the Pod as it was bound is served.
			require.Equal(t, http.StatusOK, p.acquire().status)
			pod := p.pod
			tt.change(&pod)
			h.api.setPod(p.podName, pod)

			refused := p.acquire()

			assert.Equal(t, tt.status, refused.status, refused.raw)
			assert.Equal(t, tt.code, refused.code)
		})
	}
}

func TestHandler_RefusesACredentialWhosePodEnded(t *testing.T) {
	h := newHarness(t, options{bindingFresh: time.Nanosecond})
	p := h.instance(h.bot())
	require.Equal(t, http.StatusOK, p.acquire().status) // control
	h.api.endPod(p.podName)

	refused := p.acquire()

	assert.Equal(t, http.StatusUnauthorized, refused.status)
	assert.Equal(t, access.CodeInvalidCredential, refused.code)
}

func TestHandler_ReadsAPodAgainBeforeRefusingANewKeyAsAnotherInstances(t *testing.T) {
	// The binding is fresh for an hour, so only the read before invalid_key sees the restart.
	h := newHarness(t, options{bindingFresh: time.Hour})
	p := h.instance(h.bot())
	require.Equal(t, http.StatusOK, p.acquire().status)
	restarted := *p
	restarted.key = h.key()
	// Control: the same container under a new key is another key for one instance.
	refused := restarted.acquire()
	require.Equal(t, http.StatusUnauthorized, refused.status)
	require.Equal(t, access.CodeInvalidKey, refused.code)

	pod := p.pod
	pod.Container = "containerd://restarted"
	h.api.setPod(p.podName, pod)

	assert.Equal(t, http.StatusOK, restarted.acquire().status, "the restarted container pins its new key")
	assert.Equal(t, access.CodeInvalidKey, p.acquire().code, "the earlier container's key is no longer the instance's")
}

func TestHandler_ReadsABoundPodOnceWhileItsBindingIsFresh(t *testing.T) {
	h := newHarness(t, options{bindingFresh: time.Hour})
	p := h.instance(h.bot())
	p.running()
	h.api.mu.Lock()
	before := h.api.read
	h.api.mu.Unlock()

	for range 5 {
		require.Equal(t, http.StatusOK, p.call(http.MethodGet, mainPath, nil).status)
	}

	h.api.mu.Lock()
	defer h.api.mu.Unlock()
	assert.Equal(t, before, h.api.read)
	assert.Positive(t, before, "the first call read the Pod") // control
}
