//go:build darwin

package wakesource

// The Darwin source: what this platform's two clocks give, and that the source
// turns a suspend they record into a wake event.
//
// No CI machine can suspend itself, so the suspend is injected — through the
// same reading function the real source uses — and the platform's own readings
// are asserted separately, as the invariant the real source depends on.

import (
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestDarwinClocksAdvanceTogetherWithoutASuspend asserts the pair the detector
// needs: CLOCK_UPTIME_RAW (mach_absolute_time) stops while the system sleeps and CLOCK_MONOTONIC_RAW (mach_continuous_time) includes it, and that with nothing asleep the two clocks move
// together, so a quiet machine reports no wake.
func TestDarwinClocksAdvanceTogetherWithoutASuspend(t *testing.T) {
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
// test sets.
type fakeClocks struct {
	mu   sync.Mutex
	next monoclock.Readings
}

func (f *fakeClocks) read() monoclock.Readings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.next
}

func (f *fakeClocks) set(r monoclock.Readings) {
	f.mu.Lock()
	f.next = r
	f.mu.Unlock()
}

// TestTheDarwinSourceDeliversAWakeEvent is this platform source's contract: a
// pair of readings that records a suspend produces one signal.
func TestTheDarwinSourceDeliversAWakeEvent(t *testing.T) {
	clocks := &fakeClocks{}
	clocks.set(monoclock.Readings{Awake: monoclock.Nanos(100), Continuous: monoclock.Nanos(500)})
	src := newSource(clocks.read, time.Millisecond, SleepGap, nil, nil)
	t.Cleanup(src.Stop)

	// A second of ordinary time first: no signal.
	clocks.set(monoclock.Readings{Awake: monoclock.Nanos(100 + int64(time.Second)), Continuous: monoclock.Nanos(500 + int64(time.Second))})
	time.Sleep(5 * time.Millisecond)

	// Now ten seconds of suspend on the continuous clock only.
	clocks.set(monoclock.Readings{Awake: monoclock.Nanos(100 + int64(time.Second) + int64(time.Millisecond)), Continuous: monoclock.Nanos(500 + int64(10*time.Second))})
	waittest.WaitFor(t, "a wake event from the source", func() bool {
		select {
		case <-src.Wakes():
			return true
		default:
			return false
		}
	})
}

// TestTheDarwinSourceIsSilentWithoutASuspend is the paired absence: a clock pair
// that only advances normally never produces a signal.
func TestTheDarwinSourceIsSilentWithoutASuspend(t *testing.T) {
	clocks := &fakeClocks{}
	base := monoclock.Readings{Awake: monoclock.Nanos(100), Continuous: monoclock.Nanos(500)}
	clocks.set(base)
	src := newSource(clocks.read, time.Millisecond, SleepGap, nil, nil)
	t.Cleanup(src.Stop)

	for i := 1; i <= 20; i++ {
		clocks.set(monoclock.Readings{
			Awake:      base.Awake + monoclock.Nanos(int64(i)*int64(time.Millisecond)),
			Continuous: base.Continuous + monoclock.Nanos(int64(i)*int64(time.Millisecond)),
		})
		time.Sleep(time.Millisecond)
	}
	select {
	case <-src.Wakes():
		t.Fatal("the source reported a wake on a machine that never slept")
	default:
	}
}
