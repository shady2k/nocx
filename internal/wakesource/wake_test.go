package wakesource

// The merge: one owner of "the machine slept", fed by the OS's events and by
// the two clocks, with the rules the design states — one wake per resume
// whichever input saw it first, a dark wake is not a wake and suppresses the
// clocks, and an absent input is said once.

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	src := newSource(clocks.read, time.Millisecond, SleepGap, nil, nil)
	t.Cleanup(src.Stop)

	wakeWithin(t, src, "a wake from the clock comparison alone")
}

func TestAnEventsOnlyMachineReportsAWake(t *testing.T) {
	// The clocks never see a gap: an OS input is the only evidence.
	clocks := newClocks(normal(100))
	events := make(chan Event, 4)
	src := newSource(clocks.read, time.Millisecond, SleepGap, events, nil)
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
		clocks := newClocks(normal(100), afterGap(100))
		events := make(chan Event, 4)
		src := newSource(clocks.read, time.Millisecond, SleepGap, events, nil)
		t.Cleanup(src.Stop)

		events <- Event{Kind: EventWoke, At: monoclock.Nanos(100 + int64(time.Millisecond))}
		wakeWithin(t, src, "the event's wake")
		// The gap is noticed on the next tick, inside the merge window: no
		// second signal.
		time.Sleep(20 * time.Millisecond)
		select {
		case at := <-src.Wakes():
			t.Fatalf("a second wake for one resume: %v", at)
		default:
		}
	})

	// The clocks first, then the event.
	t.Run("clocks then event", func(t *testing.T) {
		clocks := newClocks(normal(100), afterGap(100))
		events := make(chan Event, 4)
		src := newSource(clocks.read, time.Millisecond, SleepGap, events, nil)
		t.Cleanup(src.Stop)

		first := wakeWithin(t, src, "the clock's wake")
		events <- Event{Kind: EventWoke, At: first + monoclock.Nanos(time.Second)}
		time.Sleep(20 * time.Millisecond)
		select {
		case at := <-src.Wakes():
			t.Fatalf("a second wake for one resume: %v", at)
		default:
		}
	})
}

func TestTwoResumesApartAreTwoWakes(t *testing.T) {
	clocks := newClocks(normal(100))
	events := make(chan Event, 4)
	src := newSource(clocks.read, time.Millisecond, SleepGap, events, nil)
	t.Cleanup(src.Stop)

	events <- Event{Kind: EventWoke, At: monoclock.Nanos(100)}
	wakeWithin(t, src, "the first wake")
	// Well outside the merge window: a second resume.
	events <- Event{Kind: EventWoke, At: monoclock.Nanos(100) + monoclock.Nanos(MergeWindow) + monoclock.Nanos(time.Second)}
	wakeWithin(t, src, "the second wake")
}

func TestADarkWakeIsNotAWakeAndSuppressesTheClocks(t *testing.T) {
	clocks := newClocks(normal(100), afterGap(100), normal(100))
	events := make(chan Event, 4)
	src := newSource(clocks.read, time.Millisecond, SleepGap, events, nil)
	t.Cleanup(src.Stop)

	events <- Event{Kind: EventDarkWoke, At: monoclock.Nanos(100)}
	// The power nap's gap reaches the clocks; it must not be a wake.
	time.Sleep(20 * time.Millisecond)
	select {
	case at := <-src.Wakes():
		t.Fatalf("a dark wake produced a wake signal: %v", at)
	default:
	}

	// The full wake still does.
	events <- Event{Kind: EventWoke, At: monoclock.Nanos(200)}
	wakeWithin(t, src, "the full wake after the dark one")
}

func TestAWillSleepEdgeIsNotAWakeAndReachesTheSleepsChannel(t *testing.T) {
	// The clocks see no gap at all: only the event is under test here.
	clocks := newClocks(normal(100))
	events := make(chan Event, 4)
	src := newSource(clocks.read, time.Millisecond, SleepGap, events, nil)
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
	// And no wake was invented by the pre-sleep edge.
	select {
	case at := <-src.Wakes():
		t.Fatalf("a will-sleep edge produced a wake: %v", at)
	default:
	}
}

func TestAnEndedEventInputIsSaidOnceAndTheClocksCarryOn(t *testing.T) {
	ctx, logger := logtest.New(t)
	clocks := newClocks(normal(100), afterGap(100))
	events := make(chan Event)
	close(events)
	src := newSource(clocks.read, time.Millisecond, SleepGap, events, logger)
	t.Cleanup(src.Stop)

	// The ended input is reported, once.
	if !logtest.WaitFor(ctx, 2*time.Second, func(r logtest.Record) bool {
		return r.Level == slog.LevelWarn
	}) {
		t.Fatal("an ended OS input was not reported in the log")
	}
	warns := 0
	for _, r := range logtest.RecordsSlog(logtest.Slog(t)) {
		if r.Level == slog.LevelWarn {
			warns++
		}
	}
	_ = warns
	// ... and the clocks still detect a suspend.
	wakeWithin(t, src, "a wake after the input ended")
}
