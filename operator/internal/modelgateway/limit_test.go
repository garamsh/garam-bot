package modelgateway

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiter_HoldsNoMoreCallersThanItsBoundAndDropsTheIdlest(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	l := newLimiter(Limit{Burst: 1, Every: time.Hour}, clock)
	_, ok := l.allow("idlest")
	require.True(t, ok)
	_, ok = l.allow("idlest")
	require.False(t, ok, "control: a caller past its burst is refused")
	for i := range maxCallers {
		now = now.Add(time.Millisecond)
		l.allow(strconv.Itoa(i))
	}
	assert.Len(t, l.callers, maxCallers, "the limiter holds more callers than its bound")
	_, ok = l.allow("idlest")
	assert.True(t, ok, "the idlest caller was not the one dropped")
}

func TestLimiter_AnswersTheWholeSecondsUntilACallComesBack(t *testing.T) {
	now := time.Now()
	l := newLimiter(Limit{Burst: 1, Every: 2500 * time.Millisecond}, func() time.Time { return now })
	_, ok := l.allow("bot")
	require.True(t, ok)
	wait, ok := l.allow("bot")
	require.False(t, ok)
	assert.Equal(t, 3, wait, "Retry-After is not the wait rounded up to whole seconds")
	now = now.Add(2500 * time.Millisecond)
	_, ok = l.allow("bot")
	assert.True(t, ok, "control: once the call comes back it is taken, so the refusal took no call")
}
