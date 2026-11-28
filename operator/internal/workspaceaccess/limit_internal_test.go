package workspaceaccess

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimits_ForgetsABucketFullAgainAndKeepsOneInUse(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := newLimits("the bot's limit", time.Second, 2, func() time.Time { return now })
	require.NoError(t, l.take("idle"))
	now = now.Add(time.Second)
	require.NoError(t, l.take("busy"))

	l.forgetFull()

	assert.NotContains(t, l.each, "idle", "a bucket full again is forgotten")
	assert.Contains(t, l.each, "busy", "a bucket in use is kept")
}
