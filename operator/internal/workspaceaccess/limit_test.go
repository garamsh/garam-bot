package workspaceaccess_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

func TestLimit_ACallPastTheBotsLimitReachesNoProvider(t *testing.T) {
	h := newHarness(t, options{})
	p := h.instance(h.bot())
	before := h.provider.called()
	for range 20 { // the Bot's burst (LIMIT-1)
		require.Equal(t, http.StatusOK, p.acquire().status)
	}
	require.Greater(t, h.provider.called(), before, "control: an acquire within the limit reaches the provider")
	spent := h.provider.called()

	for name, call := range map[string]func() int{
		"acquire": func() int { return p.acquire().status },
		"dial": func() int {
			status, _ := p.dial(t)
			return status
		},
		"session tokens": func() int { return p.call(http.MethodPost, mainPath+access.TokensSuffix, nil).status },
	} {
		assert.Equal(t, http.StatusTooManyRequests, call(), name)
	}

	assert.Equal(t, spent, h.provider.called(), "no call past the limit reached the provider")
}
