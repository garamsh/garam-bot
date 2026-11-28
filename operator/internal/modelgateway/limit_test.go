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

func TestInFlight_BoundsEachBotsCallsAndTheGatewaysAndGivesThemBack(t *testing.T) {
	f := newInFlight(2, 3)
	releases := make([]func(), 0, 2)
	for range 2 {
		release, refused := f.take("a")
		require.Equal(t, notRefused, refused)
		releases = append(releases, release)
	}
	_, refused := f.take("a")
	assert.Equal(t, refusedBot, refused, "a bot holds more calls than its bound")
	release, refused := f.take("b")
	require.Equal(t, notRefused, refused, "another bot is refused for the first bot's calls")
	_, refused = f.take("c")
	assert.Equal(t, refusedGateway, refused, "the gateway holds more calls than its bound")
	release()
	release()
	_, refused = f.take("c")
	assert.Equal(t, notRefused, refused, "a released call is not given back, or given back twice")
	releases[0]()
	_, refused = f.take("a")
	assert.Equal(t, notRefused, refused, "a bot's released call is not given back to it")
	_, refused = f.take("d")
	assert.Equal(t, refusedGateway, refused, "control: the gateway is full again")
}
