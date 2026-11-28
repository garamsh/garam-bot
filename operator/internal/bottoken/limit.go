package bottoken

import (
	"math"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// limitBurst is how many exchanges one Bot's calls may make with garam at once, and limitEvery how
	// often one more is allowed after them. A process makes at most three in each renewal, about every
	// 40 minutes: one for garam's nonce, one with it, and one again where the nonce changed between.
	limitBurst = 5
	limitEvery = 30 * time.Second
)

// limits are the exchanges each Bot's calls may make with garam (LIMIT-1). A call answered without one,
// a nonce the server holds or a credential it refuses, takes nothing, and no Bot's calls take any of
// another's.
type limits struct {
	now func() time.Time

	mu sync.Mutex
	// each is held for a Bot the connector applied, as nothing reaches a limit before it is resolved to
	// one, so the set is no larger than the Bots of the watched namespaces.
	each map[Bot]*rate.Limiter
}

func newLimits(now func() time.Time) *limits {
	return &limits{now: now, each: map[Bot]*rate.Limiter{}}
}

// take answers nil where bot's calls may make one more exchange, and a limitedError with the seconds to
// wait otherwise.
func (l *limits) take(bot Bot) error {
	l.mu.Lock()
	limiter, ok := l.each[bot]
	if !ok {
		limiter = rate.NewLimiter(rate.Every(limitEvery), limitBurst)
		l.each[bot] = limiter
	}
	l.mu.Unlock()
	now := l.now()
	reservation := limiter.ReserveN(now, 1)
	wait := reservation.DelayFrom(now)
	if wait == 0 {
		return nil
	}
	reservation.CancelAt(now)
	return limitedError{retryAfter: formatSeconds(wait)}
}

// formatSeconds is wait in whole seconds, rounded up, as Retry-After carries it.
func formatSeconds(wait time.Duration) string {
	return strconv.Itoa(int(math.Ceil(wait.Seconds())))
}
