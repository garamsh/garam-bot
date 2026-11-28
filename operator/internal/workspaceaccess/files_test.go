package workspaceaccess_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

func TestFiles_AnswersThePathAWriteFailedAtAndKeepsTheFilesBeforeIt(t *testing.T) {
	h := newHarness(t, options{})
	p := h.instance(h.bot())
	p.running()
	first, failing, last := access.TokenDir+"a", access.TokenDir+"b", access.TokenDir+"c"
	h.provider.set(func(p *provider) { p.failAt = failing })

	refused := p.write(
		access.File{Path: first, Content: []byte("1")},
		access.File{Path: failing, Content: []byte("2")},
		access.File{Path: last, Content: []byte("3")},
	)

	assert.Equal(t, http.StatusServiceUnavailable, refused.status, refused.raw)
	assert.Equal(t, access.CodeTemporarilyUnavailable, refused.code)
	assert.Equal(t, failing, refused.path)
	content, written := h.file(first)
	assert.True(t, written, "the file before the failure stays written")
	assert.Equal(t, "1", content)
	_, written = h.file(last)
	assert.False(t, written, "nothing after the failure is written")
	// Control: the same write is answered once the provider writes.
	h.provider.set(func(p *provider) { p.failAt = "" })
	require.Equal(t, http.StatusNoContent, p.write(access.File{Path: last, Content: []byte("3")}).status)
}
