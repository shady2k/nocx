package wakesource

// The merge: one owner of "the machine slept", fed by the OS's events and by
// the two clocks, with the rules the design states — one wake per resume
// whichever input saw it first, a dark wake is not a wake and suppresses the
// clocks, and an absent input is said once.
//
// Every test here drives ONE STEP of the merge directly, or an injected tick
// through the poll loop: the OS input is a channel the test writes, the tick is
// a channel the test writes, and the clock pair is a script. No assertion waits
// on elapsed time or on a scheduler's choice (nocx-oalo1.4 review, finding 15).

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/waittest"
)

// scriptedClocks answers the clock pair from a script: each read takes the next
// value, and the last one is repeated once the script is spent.
type scriptedClocks struct {
	mu     sync.Mutex
	values []monoclock.Readings
	index  int
	reads  atomic.Int32
}

func newClocks(values ...monoclock.Readings) *scriptedClocks {
	return &scriptedClocks{values: values}
}

func (c *scriptedClocks) read() monoclock.Readings {
	c.reads.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.values) == 0 {
		return monoclock.Readings{}
	}
	v := c.values[min(c.index, len(c.values)-1)]
	c.index++
	return v
}

// normal is one awake second: both clocks move by a second.
func normal(base monoclock.Nanos) monoclock.Readings {
	return monoclock.Readings{Awake: base, Continuous: base}
}

// afterGap is the same pair after ten seconds of suspend: the continuous clock
// has them and the awake one does not.
func afterGap(base monoclock.Nanos) monoclock.Readings {
	return monoclock.Readings{Awake: base, Continuous: base + monoclock.Nanos(10*time.Second)}
}

// wakeWithin requires exactly one wake and answers it. Nothing may be waiting
// when the test is about to assert an absence, which is why the caller drains
// through this helper.
func wakeWithin(t *testing.T, src *Source, what string) monoclock.Nanos {
	t.Helper()
	var got monoclock.Nanos
	waittest.WaitFor(t, what, func() bool {
		select {
		case at := <-src.Wakes():
			got = at
			return true
		default:
			return false
		}
	})
	return got
}

func TestAClockOnlyMachineReportsAWake(t *testing.T) {
	clocks := newClocks(normal(100), afterGap(100))
	ticks := make(chan time.Time, 1)
	src := newSource(clocks.read, SleepGap, nil, ticks, nil)
	t.Cleanup(src.Stop)

	ticks <- time.Now()
	wakeWithin(t, src, "a wake from the clock comparison alone")
}

func TestAnEventsOnlyMachineReportsAWake(t *testing.T) {
	// The clocks never see a gap: an OS input is the only evidence.
	clocks := newClocks(normal(100))
	events := make(chan Event, 4)
	src := newSource(clocks.read, SleepGap, events, nil, nil)
	t.Cleanup(src.Stop)

	events <- Event{Kind: EventWoke, At: monoclock.Nanos(500)}
	at := wakeWithin(t, src, "a wake from the OS input alone")
	if at != monoclock.Nanos(500) {
		t.Fatalf("the wake carries %v, want the event's own moment 500", at)
	}
}

func TestOneResumeYieldsOneWakeWhicheverInputSawItFirst(t *testing.T) {
	// The event first, then the gap the clocks notice a moment later.
	t.Run("event then clocks", func(t *testing.T) {
		m := newDecide(SleepGap, MergeWindow)
		first := m.step([]Event{{Kind: EventWoke, At: 100}}, normal(100), normal(100), 100)
		if !first.woke {
			t.Fatal("the event produced no wake")
		}
		second := m.step(nil, normal(100), afterGap(100), 100+monoclock.Nanos(time.Millisecond))
		if second.woke {
			t.Fatalf("a second wake for one resume: %v", second.wakeAt)
		}
	})

	// The clocks first, then the event.
	t.Run("clocks then event", func(t *testing.T) {
		m := newDecide(SleepGap, MergeWindow)
		first := m.step(nil, normal(100), afterGap(100), 100)
		if !first.woke {
			t.Fatal("the clock gap produced no wake")
		}
		second := m.step([]Event{{Kind: EventWoke, At: 100 + monoclock.Nanos(time.Millisecond)}}, normal(100), afterGap(100), 100)
		if second.woke {
			t.Fatalf("a second wake for one resume: %v", second.wakeAt)
		}
	})
}

func TestTwoResumesApartAreTwoWakes(t *testing.T) {
	m := newDecide(SleepGap, MergeWindow)
	if !m.step([]Event{{Kind: EventWoke, At: 100}}, normal(100), normal(100), 100).woke {
		t.Fatal("the first resume produced no wake")
	}
	second := m.step([]Event{{Kind: EventWoke, At: 100 + monoclock.Nanos(MergeWindow) + monoclock.Nanos(time.Second)}}, normal(100), normal(100), 100)
	if !second.woke {
		t.Fatal("a resume well outside the merge window produced no wake")
	}
}

func TestADarkWakeIsNotAWakeAndSuppressesTheClocks(t *testing.T) {
	m := newDecide(SleepGap, MergeWindow)
	if got := m.step([]Event{{Kind: EventDarkWoke, At: 100}}, normal(100), normal(100), 100); got.woke {
		t.Fatalf("a dark wake produced a wake signal: %v", got.wakeAt)
	}
	// The power nap's gap reaches the clocks; it must not be a wake.
	if got := m.step(nil, normal(100), afterGap(100), 200); got.woke {
		t.Fatalf("a dark wake produced a wake signal from the clocks: %v", got.wakeAt)
	}

	// The full wake still does.
	if got := m.step([]Event{{Kind: EventWoke, At: 200}}, normal(100), afterGap(100), 200); !got.woke {
		t.Fatal("the full wake after the dark one produced no wake")
	}
}

// TestAnArrivedDarkWakeIsAppliedBeforeTheClockTick is finding 10's regression:
// both inputs are ready in the SAME step, and the event is applied first, so the
// clock sees the dark state and emits nothing. What the event says afterwards
// cannot retract a wake the clocks already emitted, which is why the order is a
// property of the step and not of the scheduler.
func TestAnArrivedDarkWakeIsAppliedBeforeTheClockTick(t *testing.T) {
	m := newDecide(SleepGap, MergeWindow)
	got := m.step(
		[]Event{{Kind: EventDarkWoke, At: 100}},
		normal(100), afterGap(100),
		100,
	)
	if got.woke {
		t.Fatalf("a dark wake that had arrived produced a clock wake for the same resume: %v", got.wakeAt)
	}
}

// TestATickNeverOutrunsAPendingEvent is the same rule for the poll loop: the
// dark event is already in the input channel when the tick arrives, and the
// clock gap is the same resume's. The scripted clock pair counts its reads, so
// "the tick ran" is an observable state and not a sleep.
func TestATickNeverOutrunsAPendingEvent(t *testing.T) {
	clocks := newClocks(normal(100), afterGap(100))
	events := make(chan Event, 1)
	ticks := make(chan time.Time, 1)
	src := newSource(clocks.read, SleepGap, events, ticks, nil)
	t.Cleanup(src.Stop)

	events <- Event{Kind: EventDarkWoke, At: 100}
	ticks <- time.Now()
	// Two reads: the construction's, then the tick's. When the second has
	// happened the tick's step is decided.
	waittest.WaitFor(t, "the tick to read the clocks", func() bool { return clocks.reads.Load() >= 2 })

	select {
	case at := <-src.Wakes():
		t.Fatalf("a tick emitted a wake while a dark wake was waiting: %v", at)
	default:
	}
}

func TestAWillSleepEdgeIsNotAWakeAndReachesTheSleepsChannel(t *testing.T) {
	// The clocks see no gap at all: only the event is under test here.
	clocks := newClocks(normal(100))
	events := make(chan Event, 4)
	src := newSource(clocks.read, SleepGap, events, nil, nil)
	t.Cleanup(src.Stop)

	events <- Event{Kind: EventWillSleep, At: monoclock.Nanos(300)}
	var slept monoclock.Nanos
	waittest.WaitFor(t, "the pre-sleep edge", func() bool {
		select {
		case slept = <-src.Sleeps():
			return true
		default:
			return false
		}
	})
	if slept != monoclock.Nanos(300) {
		t.Fatalf("the sleep edge carries %v, want 300", slept)
	}
	// The edge's own step has been applied by now, so its absence of a wake is
	// a fact about that step and not a race with it.
	select {
	case at := <-src.Wakes():
		t.Fatalf("a will-sleep edge produced a wake: %v", at)
	default:
	}
}

func TestAnEndedEventInputIsSaidOnceAndTheClocksCarryOn(t *testing.T) {
	// One logger for the source AND for the counts: logtest.New and
	// logtest.Slog return two different buffers, and reading the one the source
	// never wrote to counts nothing.
	sl := logtest.Slog(t)
	clocks := newClocks(normal(100), afterGap(100))
	events := make(chan Event)
	close(events)
	ticks := make(chan time.Time, 2)
	src := newSource(clocks.read, SleepGap, events, ticks, log.NewSlogAdapter(sl))
	t.Cleanup(src.Stop)

	// The ended input is reported once, on the first turn that reads it.
	ticks <- time.Now()
	if !logtest.WaitForSlog(sl, 2*time.Second, func(r logtest.Record) bool {
		return r.Level == slog.LevelWarn
	}) {
		t.Fatal("an ended OS input was not reported in the log")
	}
	// ... and the clocks still detect a suspend. The wake means the second
	// tick's turn is over, so no later turn can still be about to log.
	ticks <- time.Now()
	wakeWithin(t, src, "a wake after the input ended")

	warns := 0
	for _, r := range logtest.RecordsSlog(sl) {
		if r.Level == slog.LevelWarn {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("the ended input was reported %d times, want once", warns)
	}
}
