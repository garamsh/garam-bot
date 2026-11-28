//go:build e2e

package workspaceaccess

import "context"

// The hooks below exist in the end-to-end suite's build alone, which no release build sets, so the
// suite's fixture sets and reads what a released connector takes from its flags (conformance.md, A
// cluster's fixture).

// SetMaxDials sets the cap on the dials open in all, and answers the cap it replaced.
func SetMaxDials(s Service, n int) int {
	ds := s.(*service).dials
	ds.mu.Lock()
	defer ds.mu.Unlock()
	previous := ds.max
	ds.max = n
	return previous
}

// Limits answers each Bot's and each instance's burst, and the seconds each takes to give a call back.
func Limits() (botBurstOf, botEverySeconds, instanceBurstOf, instanceEverySeconds int) {
	return botBurst, int(botEvery.Seconds()), instanceBurst, int(instanceEvery.Seconds())
}

// SweepPastGrace runs the deletion sweep twice, the second time with its clock past the grace, so a
// workspace whose Bot is gone now is deleted when it returns (conformance.md, remove and remake).
func SweepPastGrace(ctx context.Context, s Service) {
	svc := s.(*service)
	svc.sweep(ctx, 0)
	svc.sweep(ctx, sweepGrace)
}
