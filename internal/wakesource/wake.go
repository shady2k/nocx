// Package wakesource reports that the machine has woken from sleep.
//
// # What a process can know about a suspend
//
// A helper process does not run while the lid is closed: both operating
// systems suspend userspace with the machine, and every clock a process reads
// from inside it stops or runs on a schedule of its own. On Linux both
// CLOCK_MONOTONIC and Go's own timers stop (Go's runtime reads CLOCK_MONOTONIC),
// so a fifteen-second probe timer set before the sleep fires fifteen seconds
// after the RESUME, not during the sleep — which is why a stale socket can sit
// unnoticed after a lid is opened, and why the design asks for a wake signal
// rather than a shorter interval.
//
// What survives is the pair of kernel clocks monoclock.Read answers: one that
// stops while the machine sleeps and one that does not. Their difference is
// exactly the time spent suspended — Linux's CLOCK_BOOTTIME against
// CLOCK_MONOTONIC, Darwin's mach_continuous_time against mach_absolute_time
// (CLOCK_MONOTONIC_RAW against CLOCK_UPTIME_RAW). Reading the pair once a second
// costs one timer and needs no D-Bus, no IOKit run loop and no privileges, and
// it is the same answer on both platforms this product ships to.
//
// # What it cannot see
//
//   - A machine that was switched off rather than suspended. That is a boot:
//     the helper starts with the system and its connections start empty.
//   - A suspend shorter than SleepGap. A sub-second freeze cannot have done
//     anything to a connection that a one-second probe would find.
//   - A platform whose kernel offers no continuous clock. There is no such
//     platform among nocx's targets; a port would add a readings file and say
//     so, rather than reporting wakes it cannot see.
package wakesource

import (
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/monoclock"
)

const (
	// PollEvery is how often the detector reads the clock pair. The signal is
	// therefore delivered within a second of a resume, and the supervisor's
	// five-second wake deadline starts from the signal (Q4's measure).
	PollEvery = time.Second
	// SleepGap is the smallest suspend worth a signal. Anything shorter is a
	// freeze that no probe interval could have been affected by.
	SleepGap = time.Second
)

// Source watches the clock pair and answers one event per wake.
type Source struct {
	read  func() monoclock.Readings
	every time.Duration
	gap   time.Duration

	wakes    chan monoclock.Nanos
	done     chan struct{}
	stopOnce sync.Once
}

// New starts the process's wake detector: the platform's clocks, polled once a
// second for the life of the process.
func New() *Source { return newSource(monoclock.Read, PollEvery, SleepGap) }

// newSource is the detector on an injected clock pair, which is how a test
// states a suspend: the continuous clock moves, the awake one does not.
func newSource(read func() monoclock.Readings, every, gap time.Duration) *Source {
	s := &Source{
		read:  read,
		every: every,
		gap:   gap,
		// One pending wake is enough: the supervisor sends one probe per
		// connection per wake, and two wakes nobody has read yet are one
		// fact for every connection that has not answered the first.
		wakes: make(chan monoclock.Nanos, 1),
		done:  make(chan struct{}),
	}
	go s.poll()
	return s
}

// Wakes answers the channel that receives once per wake, with the clock reading
// at which it was seen. It is never closed: the source lives as long as the
// process.
func (s *Source) Wakes() <-chan monoclock.Nanos { return s.wakes }

// Stop ends the poll loop. Idempotent.
func (s *Source) Stop() { s.stopOnce.Do(func() { close(s.done) }) }

func (s *Source) poll() {
	ticker := time.NewTicker(s.every)
	defer ticker.Stop()
	prev := s.read()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			cur := s.read()
			if slept(prev, cur, s.gap) {
				// The signal carries the moment it was SEEN, on the clock the
				// supervisor measures its five seconds on (monoclock), so a
				// supervisor that gets to the fan-out a moment later still
				// measures from the wake.
				select {
				case s.wakes <- monoclock.Now():
				default:
				}
			}
			prev = cur
		}
	}
}

// slept reports whether the machine was suspended between two readings: the
// continuous clock moved further than the awake one did, by at least gap.
func slept(prev, cur monoclock.Readings, gap time.Duration) bool {
	continuous := cur.Continuous - prev.Continuous
	awake := cur.Awake - prev.Awake
	if continuous <= awake {
		// Equal deltas are an ordinary second; a negative difference would be
		// two clocks disagreeing about time, which is not a suspend either.
		return false
	}
	return time.Duration(continuous-awake) >= gap
}
