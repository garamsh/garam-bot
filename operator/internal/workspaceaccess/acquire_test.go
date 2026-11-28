package workspaceaccess_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

func TestAcquire_AnswersAProvidersErrorAsInterface7MapsIt(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   access.Code
		retry  bool
	}{
		{"ErrInvalid", fmt.Errorf("%w: the class's memory", workspace.ErrInvalid), http.StatusUnprocessableEntity, access.CodeUnsupported, false},
		{"ErrUnsupported", fmt.Errorf("%w: egress", workspace.ErrUnsupported), http.StatusUnprocessableEntity, access.CodeUnsupported, false},
		{"ErrConflict", fmt.Errorf("%w: the name", workspace.ErrConflict), http.StatusServiceUnavailable, access.CodeTemporarilyUnavailable, false},
		{"ErrDeleting", fmt.Errorf("%w: the earlier incarnation", workspace.ErrDeleting), http.StatusConflict, access.CodeDeleting, true},
		{"any other failure", errors.New("the API server is down at 10.0.0.1"), http.StatusServiceUnavailable, access.CodeTemporarilyUnavailable, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, options{})
			p := h.instance(h.bot())
			h.provider.set(func(p *provider) { p.failWith["Create"] = tt.err })

			refused := p.acquire()

			assert.Equal(t, tt.status, refused.status, refused.raw)
			assert.Equal(t, tt.code, refused.code)
			assert.Equal(t, tt.retry, refused.header.Get(access.HeaderRetryAfter) != "", "Retry-After")
			if tt.status >= 500 {
				assert.NotContains(t, refused.raw, "10.0.0.1", "a 5xx names nothing of the server's own failure")
			}
			// Control: the same call is answered once the provider answers.
			h.provider.set(func(p *provider) { delete(p.failWith, "Create") })
			assert.Equal(t, http.StatusOK, p.acquire().status)
		})
	}
}
