package hostconn

import "time"

// How a round trip is graded, in ONE place.
//
// THE GRADE HAS HYSTERESIS AND THAT IS WHY IT IS STORED, NOT DERIVED. A link
// sitting exactly at one threshold would flap — probe, slow, probe, fine — an
// indicator blinking at the probe interval, which is worse than no indicator
// because it draws the eye and says nothing. So the grade ENTERS slow at one
// number and LEAVES it at a lower one, and the gap between them is what a
// steady link cannot cross by jitter alone.
//
// The numbers are chosen against what an SSH round trip normally is:
// single-digit milliseconds on a LAN, tens across a continent, and low hundreds
// on a satellite or a badly congested link. Half a second is past all of those —
// a host that takes that long to answer a request it does not even parse is
// busy, not far away.
//
// It lives here because the connection's ONE prober is here. The session's own
// liveness record carries the same two words, and it reads them from this
// function rather than thresholding the milliseconds a second time: two
// derivations of one concept agree everywhere anyone looks and disagree exactly
// at the boundary.
const (
	// SlowRoundTripEnter is the round trip at or above which a connection is
	// graded slow.
	SlowRoundTripEnter = 500 * time.Millisecond
	// SlowRoundTripLeave is the round trip at or below which a connection
	// stops being graded slow.
	SlowRoundTripLeave = 300 * time.Millisecond
)

// GradeRoundTrip folds one measurement into the grade, carrying the previous
// grade so the thresholds can differ by direction. A zero measurement is not a
// grade: nothing was measured, so the previous one stands.
func GradeRoundTrip(prevSlow bool, rtt time.Duration) bool {
	if rtt <= 0 {
		return prevSlow
	}
	if prevSlow {
		return rtt > SlowRoundTripLeave
	}
	return rtt >= SlowRoundTripEnter
}
