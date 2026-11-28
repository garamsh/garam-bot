package workspaceaccess

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fresh = "fresh"
	stale = "stale"
)

func TestBindings_ForgetsAReadNoLongerFreshAndKeepsAFreshOne(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	b := newBindings(pods{stale: {UID: stale}, fresh: {UID: fresh}}, 5*time.Second, func() time.Time { return now })
	_, _, err := b.pod(context.Background(), team, stale, false)
	require.NoError(t, err)
	now = now.Add(3 * time.Second)
	_, _, err = b.pod(context.Background(), team, fresh, false)
	require.NoError(t, err)
	now = now.Add(3 * time.Second)

	b.forget()

	assert.NotContains(t, b.held, podKey{team, stale})
	assert.Contains(t, b.held, podKey{team, fresh})
}
