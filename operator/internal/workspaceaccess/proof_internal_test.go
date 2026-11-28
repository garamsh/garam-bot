package workspaceaccess

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

func TestProofs_ForgetsAJTIPastItsMemoryAndKeepsAYoungerOne(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	p, err := newProofs([]string{testOrigin}, func() time.Time { return now })
	require.NoError(t, err)
	p.seen["old"] = now.Add(-access.ProofMemory - time.Second)
	p.seen["young"] = now.Add(-access.ProofMemory + time.Second)

	p.forget()

	assert.NotContains(t, p.seen, "old")
	assert.Contains(t, p.seen, "young")
}
