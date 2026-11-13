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
// # Two inputs, one owner
//
// ONE source owns the fact "the machine slept", and it merges the two things a
// process can know about a suspend:
//
//  1. the operating system's own sleep/wake events — IOKit's power
//     notifications on macOS, logind's PrepareForSleep on Linux (the OS inputs
//     in this package); and
//  2. the pair of kernel clocks monoclock.Read answers, one of which stops
//     while the machine sleeps and one of which does not. Their difference is
//     exactly the time spent suspended — Linux's CLOCK_BOOTTIME against
//     CLOCK_MONOTONIC, Darwin's mach_continuous_time against mach_absolute_time
//     (CLOCK_MONOTONIC_RAW against CLOCK_UPTIME_RAW). Reading the pair once a
//     second costs one timer, needs no privileges, and is the same answer on
//     both platforms.
//
// The events are the precise input: they say "about to sleep" before it
// happens, and they distinguish a full wake from a DARK wake (Power Nap and its
// peers), which is not a wake at all and must not make every connection probe.
// The clocks are the fallback: they catch a sleep no event reported — a machine
// with no logind, a missed signal, a helper that started while the machine was
// already away. ONE resume yields ONE wake signal, whichever input saw it
// first; while a dark wake is the last thing the OS said, the clock input is
// suppressed, because the gap it sees is the power nap and not a resume.
//
// # What it cannot see
//
//   - A machine that was switched off rather than suspended. That is a boot:
//     the helper starts with the system and its connections start empty.
//   - A suspend shorter than SleepGap when only the clocks are available. A
//     sub-second freeze cannot have done anything to a connection that a
//     one-second probe would find.
//   - An OS input that is absent. That is SAID once, at construction, rather
//     than silently degrading to the clocks: a machine where one input is
//     missing is a machine whose behaviour differs, and that belongs in the log.
package wakesource

import (
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/monoclock"
)

const (
	// PollEvery is how often the detector reads the clock pair. The signal is
	// therefore delivered within a second of a resume, and the supervisor's
	// five-second wake deadline starts from the signal (Q4's measure).
	PollEvery = time.Second
	// SleepGap is the smallest suspend worth a signal when only the clocks can
	// see it. Anything shorter is a freeze that no probe interval could have
	// been affected by.
	SleepGap = time.Second
	// MergeWindow is how close two wakes must be to be ONE resume. The OS
	// event arrives at the wake itself and the clock gap is noticed at the
	// next poll, so the two are at most a poll apart; a second resume inside
	// this window is the same resume for a probe that is due five seconds
	// after the first signal.
	MergeWindow = 2 * PollEvery
)

// EventKind is what an operating system's input said about sleep.
type EventKind uint8

const (
	// EventWillSleep is the machine going to sleep, said BEFORE it does. The
	// supervisors use it to stop asking their connections to answer: nothing
	// will be heard until the machine returns.
	EventWillSleep EventKind = iota
	// EventWoke is a full wake: the machine is back and every connection must
	// prove itself.
	EventWoke
	// EventDarkWoke is a wake with the display and the user's session still
	// down — Power Nap and its peers. The machine did not come back to a
	// person, so it is not a wake for any connection: no probe, and the clock
	// input is suppressed while this is the last state, because the gap it
	// would see is the nap.
	EventDarkWoke
)

// Event is one thing an OS input said, seen at At.
type Event struct {
	Kind EventKind
	At   monoclock.Nanos
}

// Source merges the OS's sleep/wake events with the clock comparison, and is the
// one owner of "the machine slept".
type Source struct {
	log   log.Logger
	read  func() monoclock.Readings
	every time.Duration
	gap   time.Duration
	merge time.Duration
	// events is the OS input, nil when this machine has none.
	events <-chan Event

	wakes  chan monoclock.Nanos
	sleeps chan monoclock.Nanos
	done   chan struct{}

	stopOnce sync.Once
	// The merge's own state, touched only by its goroutine.
	lastWake monoclock.Nanos
	dark     bool
}

// New starts the process's wake detector: the platform's OS events (when the
// machine has them) merged with its clock pair, for the life of the process. An
// absent OS input is reported once, here.
func New(logger log.Logger) *Source {
	events, err := osEvents()
	s := newSource(monoclock.Read, PollEvery, SleepGap, events, logger)
	if err != nil {
		logger.Warn("wakesource: this machine has no operating-system sleep/wake events; the clock comparison is the only input",
			"why", err)
	}
	return s
}

// newSource is the detector on an injected clock pair and injected events,
// which is how a test states an OS input: a channel it feeds.
func newSource(read func() monoclock.Readings, every, gap time.Duration, events <-chan Event, logger log.Logger) *Source {
	if read == nil {
		read = monoclock.Read
	}
	if logger == nil {
		logger = log.NewSlogAdapter(nil)
	}
	s := &Source{
		log:    logger,
		read:   read,
		every:  every,
		gap:    gap,
		merge:  MergeWindow,
		events: events,
		// One pending signal is enough: the supervisor sends one probe per
		// connection per wake, and two wakes nobody has read yet are one fact
		// for every connection that has not answered the first.
		wakes:  make(chan monoclock.Nanos, 1),
		sleeps: make(chan monoclock.Nanos, 1),
		done:   make(chan struct{}),
	}
	go s.poll()
	return s
}

// Wakes answers the channel that receives once per wake, with the clock reading
// at which it was seen. It is never closed: the source lives as long as the
// process.
func (s *Source) Wakes() <-chan monoclock.Nanos { return s.wakes }

// Sleeps answers the channel that receives once when the machine is about to
// sleep. A machine whose OS input is absent answers a channel that never
// receives.
func (s *Source) Sleeps() <-chan monoclock.Nanos { return s.sleeps }

// Stop ends the poll loop. Idempotent.
func (s *Source) Stop() { s.stopOnce.Do(func() { close(s.done) }) }

// poll is the merge. It is the only writer of the merge's state (lastWake,
// dark) and of the two channels.
func (s *Source) poll() {
	ticker := time.NewTicker(s.every)
	defer ticker.Stop()
	prev := s.read()
	events := s.events
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			cur := s.read()
			if slept(prev, cur, s.gap) && !s.dark {
				// The clock's own evidence of a suspend. Suppressed while the
				// last thing the OS said was a dark wake: the gap is the
				// power nap, not a resume.
				s.wake(monoclock.Now())
			}
			prev = cur
		case ev, ok := <-events:
			if !ok {
				// An input that ended is gone; the clocks carry on, and this
				// is said once rather than per tick.
				s.log.Warn("wakesource: the operating system's sleep/wake input ended; the clock comparison is the only input")
				events = nil
				continue
			}
			s.mergeEvent(ev)
		}
	}
}

// mergeEvent applies one OS event.
func (s *Source) mergeEvent(ev Event) {
	switch ev.Kind {
	case EventWillSleep:
		s.dark = false
		select {
		case s.sleeps <- ev.At:
		default:
		}
	case EventWoke:
		s.dark = false
		s.wake(ev.At)
	case EventDarkWoke:
		s.dark = true
	}
}

// wake reports one resume, once: the event input and the clock input are two
// views of the same resume, and whichever sees it first is the signal.
func (s *Source) wake(at monoclock.Nanos) {
	if s.lastWake != 0 && at >= s.lastWake && at-s.lastWake < monoclock.Nanos(s.merge) {
		return
	}
	s.lastWake = at
	select {
	case s.wakes <- at:
	default:
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
