// Package monoclock reads the machine's monotonic clock as integer
// nanoseconds, and nothing else.
//
// It exists because internal/helper/session's owner and internal/sessionruntime
// need one clock that the coordinator and a LOCAL helper can be said to
// share (spec §7.2): a `commitBy` deadline the coordinator computes must be
// comparable against the value the owner reads at commit, on the same
// machine, without either side trusting the other's wall clock or the
// wire's own latency. time.Time and time.Now() are wrong for this
// deliberately: wall time can jump — NTP, a suspend/resume, a user
// adjusting the clock — and a deadline built on it can be defeated or
// tripped by an event that has nothing to do with how long the intent
// actually waited. A monotonic reading only ever moves forward, which is
// the one property a deadline needs.
//
// Never use time.Now() where a commitBy deadline is computed or checked;
// use this package. time.Now() remains correct everywhere else — logging,
// wall-clock timestamps handed to a person — because those want the
// calendar, not an interval.
package monoclock

// Nanos is a reading of the machine's monotonic clock, in nanoseconds. It
// names no epoch: only the DIFFERENCE between two readings on the same
// machine means anything, the same restriction CLOCK_MONOTONIC itself
// carries. Two readings from two different machines are not comparable at
// all — see the package doc's note on why a far-host helper gets no
// intents that carry a commitBy in this epic.
type Nanos int64

// Now reads the machine's monotonic clock: unix.ClockGettime(CLOCK_MONOTONIC)
// on Linux, unix.ClockGettime(CLOCK_MONOTONIC_RAW) on Darwin (the
// mach_continuous_time domain, per-platform in monoclock_linux.go and
// monoclock_darwin.go). It never calls time.Now() — see the package doc.
func Now() Nanos { return nowRaw() }

// Readings is one pair of readings from the two kernel clocks a sleep detector
// needs: Awake stops while the machine is suspended, Continuous does not. The
// difference between their deltas IS the time the machine spent asleep, which
// is the one thing a process frozen with the rest of userspace can still learn
// about a suspend (wakesource, nocx-oalo1.4.5).
//
// Both readings come from the same kernel, per-platform in readings_linux.go
// and readings_darwin.go. They are comparable only to another pair from the
// same machine, and only as deltas — the same restriction Now's readings carry.
type Readings struct {
	Awake      Nanos
	Continuous Nanos
}

// Read answers the pair on this platform.
func Read() Readings { return readClocks() }
