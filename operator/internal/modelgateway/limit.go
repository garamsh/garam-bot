package modelgateway

import (
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limit is how many calls one caller may make at once, and how long each takes to come back.
type Limit struct {
	Burst int
	Every time.Duration
}

// Limits are each bot's limit on the model surface (LIMIT-1) and each issuer's on the issue surface
// (LIMIT-2). A zero Limit is 60 calls, one back a second.
type Limits struct {
	Bot, Issuer Limit
}

// maxCallers bounds the callers a limiter holds. Past it, a caller's limiter idle the longest is
// dropped, which gives that caller a full burst again and no more.
const maxCallers = 10000

// limiter holds one token bucket per caller, so no caller's calls take from another's.
type limiter struct {
	limit Limit
	now   func() time.Time

	mu      sync.Mutex
	callers map[string]*caller
}

type caller struct {
	bucket *rate.Limiter
	usedAt time.Time
}

func newLimiter(l Limit, now func() time.Time) *limiter {
	if l.Burst <= 0 || l.Every <= 0 {
		l = Limit{Burst: 60, Every: time.Second}
	}
	return &limiter{limit: l, now: now, callers: map[string]*caller{}}
}

// allow takes one call of key's, or answers the whole seconds until one comes back.
func (l *limiter) allow(key string) (int, bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.callers[key]
	if !ok {
		if len(l.callers) >= maxCallers {
			l.dropIdlest()
		}
		c = &caller{bucket: rate.NewLimiter(rate.Every(l.limit.Every), l.limit.Burst)}
		l.callers[key] = c
	}
	c.usedAt = now
	reservation := c.bucket.ReserveN(now, 1)
	if delay := reservation.DelayFrom(now); delay > 0 {
		reservation.CancelAt(now)
		return max(1, int(math.Ceil(delay.Seconds()))), false
	}
	return 0, true
}

func (l *limiter) dropIdlest() {
	var idlest string
	var at time.Time
	for key, c := range l.callers {
		if idlest == "" || c.usedAt.Before(at) {
			idlest, at = key, c.usedAt
		}
	}
	delete(l.callers, idlest)
}
