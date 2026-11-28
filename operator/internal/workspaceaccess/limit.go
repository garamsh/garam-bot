package workspaceaccess

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// The limits of ADR 0099 decision 10.
const (
	// botBurst calls of each Bot reach a provider or an issuer at once, and one more every botEvery
	// after them (LIMIT-1). A renewal is two calls, and a reconnect one.
	botBurst = 20
	botEvery = 3 * time.Second
	// instanceBurst requests of each workload instance are taken at once, refused ones included, and
	// one more every instanceEvery after them (LIMIT-2).
	instanceBurst = 30
	instanceEvery = time.Second
)

// limits holds one bucket per key under one rate.
type limits struct {
	what  string
	every time.Duration
	burst int
	now   func() time.Time

	mu   sync.Mutex
	each map[string]*rate.Limiter
}

func newLimits(what string, every time.Duration, burst int, now func() time.Time) *limits {
	return &limits{what: what, every: every, burst: burst, now: now, each: map[string]*rate.Limiter{}}
}

// take takes one call of key's bucket, and answers a limitedError with the wait where it is spent.
func (l *limits) take(key string) error {
	l.mu.Lock()
	limiter, ok := l.each[key]
	if !ok {
		limiter = rate.NewLimiter(rate.Every(l.every), l.burst)
		l.each[key] = limiter
	}
	l.mu.Unlock()
	now := l.now()
	reservation := limiter.ReserveN(now, 1)
	wait := reservation.DelayFrom(now)
	if wait == 0 {
		return nil
	}
	reservation.CancelAt(now)
	return limitedError{what: l.what, wait: wait}
}

// forgetFull drops every bucket that is full again, which a new one would be too, so the keys of
// callers that stopped calling are not held for ever.
func (l *limits) forgetFull() {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, limiter := range l.each {
		if limiter.TokensAt(now) >= float64(l.burst) {
			delete(l.each, key)
		}
	}
}

// forget drops key's bucket, for a Bot or an instance that is gone.
func (l *limits) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.each, key)
}
