package wakesource

// The merge: one owner of "the machine slept", fed by the OS's events and by
// the two clocks, with the rules the design states — one wake per resume
// whichever input saw it first, and an absent input is said once.
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

// TestDrainAnswersEveryDeliveredEventInOrder is the poll loop's half of the
// step's ordering rule: a tick cannot read the clocks before it has taken
// everything the input has already delivered, and an input that has ended says
// so exactly once rather than on every turn.
func TestDrainAnswersEveryDeliveredEventInOrder(t *testing.T) {
	events := make(chan Event, 2)
	events <- Event{Kind: EventWillSleep, At: 1}
	events <- Event{Kind: EventWoke, At: 2}

	pending, next, ended := drain(events)
	if ended {
		t.Fatal("a live input was reported as ended")
	}
	if next != events {
		t.Fatal("drain changed the channel to read next on a live input")
	}
	if len(pending) != 2 || pending[0].Kind != EventWillSleep || pending[1].At != 2 {
		t.Fatalf("drain answered %+v, want both events in the order they arrived", pending)
	}

	close(events)
	pending, next, ended = drain(next)
	if !ended || next != nil {
		t.Fatalf("a closed input answers (ended=%v, next=%v), want (true, nil): a channel read again reports the end forever", ended, next)
	}
	if len(pending) != 0 {
		t.Fatalf("a closed input answered %d events, want none", len(pending))
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
