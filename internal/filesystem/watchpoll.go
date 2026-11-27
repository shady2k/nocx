package filesystem

import "time"

// The change-detection ladder: how often a directory is re-listed and compared
// by digest while nothing is happening, and how far that wait stretches when
// the listing fails.
//
// # Why it lives here and not in one of the two parties that use it
//
// Two parties poll a directory by digest. The transport does it when a
// binding's provider cannot watch (ws_files.go), and the far helper does it
// when a coordinator asks the HOST to watch (hostsvc/watch.go) so that a
// remote panel costs no wire round trip at all. They must ask the same
// question at the same rate — a host polling once a second while the panel
// back-off says ten would be a second cadence nobody chose — so the ladder is
// declared once here and both call it. A second copy is the defect AD-8 names:
// the two agree everywhere anybody looks and disagree on the host that is
// slow, or old, or simply wrong for one of them.
const (
	// WatchPollBase is the ladder's unit: the responsive rung. A visible
	// panel refreshes within two of these, which is why it is half a second
	// and not a whole one.
	WatchPollBase = 500 * time.Millisecond
	// WatchPollMaxIdleStep caps the quiet ladder at 20×base — ten seconds.
	WatchPollMaxIdleStep = 4
	// WatchPollMaxFailureStep caps the failure ladder at 120×base — a
	// minute — so one unreachable directory costs a bounded trickle.
	WatchPollMaxFailureStep = 4
)

// WatchPollWait answers the delay before the next digest poll of one path.
//
// The idle and failure ladders are independent: one unreachable directory must
// not slow healthy paths, and one noisy directory must not reset quiet ones.
// Idle waits are 1, 2, 5, 10 seconds; a failing path waits 10, 20, 40, 60
// seconds and returns to the quiet ladder only after a poll succeeds.
//
// There is no jitter here. Spreading simultaneous polls belongs to whoever
// shares a network, and that party adds it (ws_files.go does, for remote
// bindings); a host polling its own disk has nobody to collide with.
func WatchPollWait(base time.Duration, idleStep, failureStep int) time.Duration {
	if base <= 0 {
		base = WatchPollBase
	}
	if idleStep > WatchPollMaxIdleStep {
		idleStep = WatchPollMaxIdleStep
	}
	if failureStep > WatchPollMaxFailureStep {
		failureStep = WatchPollMaxFailureStep
	}
	if failureStep > 0 {
		multiplier := 20
		switch failureStep {
		case 2:
			multiplier = 40
		case 3:
			multiplier = 80
		case 4:
			multiplier = 120
		}
		return time.Duration(multiplier) * base
	}
	multiplier := 2
	switch idleStep {
	case 2:
		multiplier = 4
	case 3:
		multiplier = 10
	case 4:
		multiplier = 20
	}
	return time.Duration(multiplier) * base
}
