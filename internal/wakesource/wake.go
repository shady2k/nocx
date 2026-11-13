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
// # Two inputs, one owner, one order
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
// happens. The clocks are the fallback: they catch a sleep no event reported — a
// machine with no logind, a missed signal, a helper that started while the
// machine was already away. ONE resume yields ONE wake signal, whichever input
// saw it first.
//
// The two inputs are merged in ONE serialized step (decide.step), and the events
// that have already arrived are applied BEFORE the clocks are compared, so a
// tick decides on the OS's latest word and on the clock pair together. One
// resume stays one wake whichever input names it, and the answer is the same
// whichever case the runtime's select happened to pick — the scheduler is out of
// the decision.
//
// # What no shipped build can say: a dark wake
//
// A dark wake — Power Nap, maintenance, a network daemon — powers the CPU and
// the network without the display or the user's session, and design §5.3 says
// it must not make every connection probe. No public API tells it from a full
// wake: macOS's capability set (IOPMCapabilityBits, IOPMIsADarkWake,
// IOPMConnectionCreate) is declared in IOPMLibPrivate.h, which is SPI, and
// logind has no such edge. So a dark wake reads as a wake here: one probe per
// connection, which is evidence and never a verdict and closes nothing.
// iokit_darwin.go states the same limit beside the code that would need it.
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
	// EventWoke is a wake: the machine is back and every connection must
	// prove itself.
	EventWoke
)

// Event is one thing an OS input said, seen at At.
type Event struct {
	Kind EventKind
	At   monoclock.Nanos
}

// outcome is what one serialized step of the merge decided to report.
type outcome struct {
	slept, woke     bool
	sleepAt, wakeAt monoclock.Nanos
}

// decide is the merge's rule and all of its state: one resume is one wake,
// whichever input saw it first. It touches nothing outside itself and starts no
// goroutine, so the rule is exercised one step at a time, with no clock to wait
// on and no scheduler to hope for.
type decide struct {
	gap, window time.Duration
	lastWake    monoclock.Nanos
}

func newDecide(gap, window time.Duration) *decide {
	return &decide{gap: gap, window: window}
}

// step is ONE serialized step of the merge: the events that have already
// arrived, in the order they arrived, and the clock pair. Events first, then the
// clocks, so the pair is compared against the state the events just set (see
// "one order" in the package comment).
func (d *decide) step(events []Event, prev, cur monoclock.Readings, now monoclock.Nanos) outcome {
	var o outcome
	for _, ev := range events {
		switch ev.Kind {
		case EventWillSleep:
			o.slept, o.sleepAt = true, ev.At
		case EventWoke:
			if d.woke(ev.At) {
				o.woke, o.wakeAt = true, ev.At
			}
		}
	}
	// The clocks act LAST, on the resume the events above may have just named.
	if slept(prev, cur, d.gap) {
		if d.woke(now) {
			o.woke, o.wakeAt = true, now
		}
	}
	return o
}

// woke records one resume and answers whether it is a new one: the event input
// and the clock input are two views of the same resume, and whichever sees it
// first is the signal.
func (d *decide) woke(at monoclock.Nanos) bool {
	if d.lastWake != 0 && at >= d.lastWake && at-d.lastWake < monoclock.Nanos(d.window) {
		return false
	}
	d.lastWake = at
	return true
}

// Source merges the OS's sleep/wake events with the clock comparison, and is the
// one owner of "the machine slept".
type Source struct {
	log    log.Logger
	read   func() monoclock.Readings
	gap    time.Duration
	events <-chan Event
	// ticks says "compare the clocks now". Production answers PollEvery's
	// ticker; a test answers its own channel, so nothing here waits on
	// elapsed time.
	ticks <-chan time.Time
	// ticker is non-nil only when this Source made the ticker itself, and is
	// what Stop stops.
	ticker *time.Ticker
	merge  *decide

	wakes  chan monoclock.Nanos
	sleeps chan monoclock.Nanos
	done   chan struct{}

	stopOnce sync.Once
}

// New starts the process's wake detector: the platform's OS events (when the
// machine has them) merged with its clock pair, for the life of the process. An
// absent OS input is reported once, here.
func New(logger log.Logger) *Source {
	events, err := osEvents()
	ticker := time.NewTicker(PollEvery)
	s := newSource(monoclock.Read, SleepGap, events, ticker.C, logger)
	s.ticker = ticker
	if err != nil {
		s.log.Warn("wakesource: this machine has no operating-system sleep/wake events; the clock comparison is the only input",
			"why", err)
	}
	return s
}

// newSource is the detector on injected inputs: the clock pair, the OS events,
// and the ticks that ask for a comparison. Every interval is the caller's —
// PollEvery in New, a test's own tick in a test.
func newSource(read func() monoclock.Readings, gap time.Duration, events <-chan Event, ticks <-chan time.Time, logger log.Logger) *Source {
	if read == nil {
		read = monoclock.Read
	}
	if logger == nil {
		logger = log.NewSlogAdapter(nil)
	}
	s := &Source{
		log:    logger,
		read:   read,
		gap:    gap,
		events: events,
		ticks:  ticks,
		merge:  newDecide(gap, MergeWindow),
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
func (s *Source) Stop() {
	s.stopOnce.Do(func() {
		if s.ticker != nil {
			s.ticker.Stop()
		}
		close(s.done)
	})
}

// poll reads the inputs and hands each turn to the merge. It is the merge's only
// caller and the only writer of the two channels.
func (s *Source) poll() {
	events := s.events
	prev := s.read()
	for {
		select {
		case <-s.done:
			return
		case <-s.ticks:
			// Everything already delivered is applied before the clocks are
			// read, so the step sees the OS's latest word and the clock pair
			// together rather than in whatever order the scheduler picked.
			pending, next, ended := drain(events)
			events = next
			if ended {
				s.log.Warn("wakesource: the operating system's sleep/wake input ended; the clock comparison is the only input")
			}
			cur := s.read()
			s.report(s.merge.step(pending, prev, cur, monoclock.Now()))
			prev = cur
		case ev, ok := <-events:
			if !ok {
				// An input that ended is gone; the clocks carry on, and this
				// is said once rather than per tick.
				s.log.Warn("wakesource: the operating system's sleep/wake input ended; the clock comparison is the only input")
				events = nil
				continue
			}
			s.report(s.merge.step([]Event{ev}, prev, prev, monoclock.Now()))
		}
	}
}

// drain answers every event already delivered, in order, without waiting, and
// the channel to read next — nil once the input has ended.
func drain(events <-chan Event) ([]Event, <-chan Event, bool) {
	var pending []Event
	for events != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				return pending, nil, true
			}
			pending = append(pending, ev)
		default:
			return pending, events, false
		}
	}
	return pending, events, false
}

// report delivers what one step decided, without blocking: a signal nobody has
// read yet is already the fact this one would say.
func (s *Source) report(o outcome) {
	if o.slept {
		select {
		case s.sleeps <- o.sleepAt:
		default:
		}
	}
	if o.woke {
		select {
		case s.wakes <- o.wakeAt:
		default:
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
