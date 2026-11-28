package bot

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// The reference answers every pending read with Retry-After and every token with an hour to live, so
// the client's answers to a pending read without one and to a shorter token are read here.

func TestPendingWait_TakesRetryAfterAndBacksOffWhereThereIsNone(t *testing.T) {
	pending := backoff{}
	var none []time.Duration
	for range 8 {
		none = append(none, pendingWait(http.Header{}, &pending))
	}
	second := time.Second
	assert.Equal(t, []time.Duration{second, 2 * second, 4 * second, 8 * second, 16 * second, 32 * second,
		time.Minute, time.Minute}, none, "WS-3: no Retry-After backs off from 1 second, doubling to 60")
	// Control: a Retry-After is waited as given.
	assert.Equal(t, 90*time.Second, pendingWait(http.Header{"Retry-After": {"90"}}, &backoff{}))
}

func TestRenewal_WritesTheTokensAgainBeforeTwoThirdsOfTheShortestLife(t *testing.T) {
	assert.Equal(t, 400*time.Second, renewal(600), "TOK-2: two thirds of a 10-minute token")
	assert.Equal(t, 40*time.Minute, renewal(7200), "40 minutes at most, whatever the life")
}

func TestBackoff_WaitsARetryAfterAtLeastAndAtMost60Seconds(t *testing.T) {
	limited := &refusal7{Status: http.StatusTooManyRequests, retryAfter: 5 * time.Second}
	b := backoff{}
	assert.Equal(t, 5*time.Second, b.next(limited), "REN-1: at least the Retry-After")
	assert.Equal(t, time.Minute, (&backoff{}).next(&refusal7{retryAfter: 5 * time.Minute}), "REN-1: at most 60 seconds")
	// Control: with no Retry-After, the back-off.
	assert.Equal(t, time.Second, (&backoff{}).next(&refusal7{Status: http.StatusServiceUnavailable}))
}

func TestKnownPhase_ReadsAPhaseItDoesNotKnowAsUnknown(t *testing.T) {
	assert.Equal(t, workspace.PhaseUnknown, knownPhase("Hibernating"), "VER-2")
	// Control: a known phase is kept.
	assert.Equal(t, workspace.PhaseFailed, knownPhase(workspace.PhaseFailed))
}
