package farhelper

import (
	"testing"
	"time"
)

// The redial schedule is the reversible decision the plan records: one second,
// doubling, capped at thirty, and no attempt limit while the transport is
// connected. The loop stops attempting when the transport leaves connected, so
// "no limit" is the loop's rule and the cap is this function's.
func TestTheRedialSchedule(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 30 * time.Second},
		{6, 30 * time.Second},
		{100, 30 * time.Second},
	} {
		if got := redialDelay(tc.attempt); got != tc.want {
			t.Fatalf("redialDelay(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}
