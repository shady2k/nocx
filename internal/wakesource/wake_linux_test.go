//go:build linux

package wakesource

// The Linux source: what this platform's two clocks give, and that the source
// turns a suspend they record into a wake event.
//
// No CI machine can suspend itself, so the suspend is injected — through the
// same reading function the real source uses — and the platform's own readings
// are asserted separately, as the invariant the real source depends on. Ticks
// are injected too, so "the source has compared the clocks" is an observable
// read count and not an elapsed time (nocx-oalo1.4 review, finding 15).

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestLinuxClocksAdvanceTogetherWithoutASuspend asserts the pair the detector
// needs: CLOCK_MONOTONIC stops while the system sleeps and CLOCK_BOOTTIME includes it, and that with nothing asleep the two clocks move
// together, so a quiet machine reports no wake.
func TestLinuxClocksAdvanceTogetherWithoutASuspend(t *testing.T) {
	prev := monoclock.Read()
	if prev.Continuous < prev.Awake {
		t.Fatalf("continuous clock %d is behind the awake clock %d: the pair is wired the wrong way round", prev.Continuous, prev.Awake)
	}
	time.Sleep(20 * time.Millisecond)
	cur := monoclock.Read()
	if cur.Awake <= prev.Awake || cur.Continuous <= prev.Continuous {
		t.Fatalf("the clocks did not advance: %+v then %+v", prev, cur)
	}
	if slept(prev, cur, SleepGap) {
		t.Fatalf("a machine that did not sleep reported one: %+v then %+v", prev, cur)
	}
}

// fakeClocks is the platform's pair, scripted: the next read answers what the
// test sets, and every read is counted so a tick's work can be observed.
type fakeClocks struct {
	mu    sync.Mutex
	next  monoclock.Readings
	reads atomic.Int32
}

func (f *fakeClocks) read() monoclock.Readings {
	f.reads.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.next
}

func (f *fakeClocks) set(r monoclock.Readings) {
	f.mu.Lock()
	f.next = r
	f.mu.Unlock()
}

// TestTheLinuxSourceDeliversAWakeEvent is this platform source's contract: a
// pair of readings that records a suspend produces one signal.
func TestTheLinuxSourceDeliversAWakeEvent(t *testing.T) {
	clocks := &fakeClocks{}
	base := monoclock.Readings{Awake: monoclock.Nanos(100), Continuous: monoclock.Nanos(500)}
	clocks.set(base)
	ticks := make(chan time.Time, 1)
	src := newSource(clocks.read, SleepGap, nil, ticks, nil)
	t.Cleanup(src.Stop)

	// A second of ordinary time first: no signal.
	clocks.set(monoclock.Readings{Awake: base.Awake + monoclock.Nanos(time.Second), Continuous: base.Continuous + monoclock.Nanos(time.Second)})
	ticks <- time.Now()
	waittest.WaitFor(t, "the tick to read the clocks", func() bool { return clocks.reads.Load() == 2 })
	select {
	case at := <-src.Wakes():
		t.Fatalf("a machine that did not sleep produced a wake: %v", at)
	default:
	}

	// Now ten seconds of suspend on the continuous clock only.
	clocks.set(monoclock.Readings{Awake: base.Awake + monoclock.Nanos(time.Second) + monoclock.Nanos(time.Millisecond), Continuous: base.Continuous + monoclock.Nanos(10*time.Second)})
	ticks <- time.Now()
	waittest.WaitFor(t, "a wake event from the source", func() bool {
		select {
		case <-src.Wakes():
			return true
		default:
			return false
		}
	})
}

// TestTheLinuxSourceIsSilentWithoutASuspend is the paired absence: a clock pair
// that only advances normally never produces a signal. Each tick's read is
// counted, so the last tick is known to have been decided before the absence is
// asserted.
func TestTheLinuxSourceIsSilentWithoutASuspend(t *testing.T) {
	clocks := &fakeClocks{}
	base := monoclock.Readings{Awake: monoclock.Nanos(100), Continuous: monoclock.Nanos(500)}
	clocks.set(base)
	ticks := make(chan time.Time, 20)
	src := newSource(clocks.read, SleepGap, nil, ticks, nil)
	t.Cleanup(src.Stop)

	const tickCount = 20
	for i := 1; i <= tickCount; i++ {
		clocks.set(monoclock.Readings{
			Awake:      base.Awake + monoclock.Nanos(int64(i)*int64(time.Millisecond)),
			Continuous: base.Continuous + monoclock.Nanos(int64(i)*int64(time.Millisecond)),
		})
		ticks <- time.Now()
	}
	waittest.WaitFor(t, "every tick to read the clocks", func() bool {
		return clocks.reads.Load() == 1+tickCount
	})
	select {
	case at := <-src.Wakes():
		t.Fatalf("the source reported a wake on a machine that never slept: %v", at)
	default:
	}
}
