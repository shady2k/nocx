package hostconn

// The supervisor's clock, injected so that "fifteen seconds" is the test's to
// decide (design §10: "Time is an injected clock or a released hold: a test
// waits on an observable").
//
// Nothing in this package sleeps, and nothing reads the wall. The monotonic
// reading is monoclock's for the reason that package exists: an interval built
// on wall time can be defeated or tripped by NTP, a suspend or a person
// changing the clock, and every question the closing policy asks is an interval
// — how long has this connection been silent.

import (
	"time"

	"github.com/shady2k/nocx/internal/monoclock"
)

// Clock is the supervisor's only source of time.
type Clock interface {
	// Now reads the clock.
	Now() monoclock.Nanos
	// After answers a channel that receives one reading once d has passed on
	// this clock. The reading is the clock's value at delivery, never
	// Now()+d: a test that advances ten minutes at once must see the time it
	// advanced to, not the time it would have reached one tick at a time.
	After(d time.Duration) <-chan monoclock.Nanos
}

// RealClock is the process's clock: monoclock for readings, and a timer that
// Go keeps on the monotonic clock for waits.
type RealClock struct{}

// Now reads the monotonic clock.
func (RealClock) Now() monoclock.Nanos { return monoclock.Now() }

// After is a timer's channel, carrying the reading at which it fired.
func (RealClock) After(d time.Duration) <-chan monoclock.Nanos {
	ch := make(chan monoclock.Nanos, 1)
	if d <= 0 {
		ch <- monoclock.Now()
		return ch
	}
	timer := time.NewTimer(d)
	go func() {
		<-timer.C
		ch <- monoclock.Now()
	}()
	return ch
}
