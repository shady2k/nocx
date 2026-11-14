package panedriver

// The automatic retry, asserted on its own terms (design §5.3; nocx-oalo1.5.4).
//
// These live INSIDE the package because the clock and the jitter are seams a
// TEST supplies and production never does: an option that only a test passes is
// dead code by the rot's own definition, and the ratchet says so. The behaviour
// is public — every value below is reachable through New and Apply — and the
// package-internal test is the smallest way to state "attempt fourteen happens
// now" without exporting a knob nobody else should turn.

import (
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
)

// fakeScheduler records what was scheduled so a test fires it by hand: a retry
// is asserted by its observable, never by waiting.
type fakeScheduler struct {
	mu      sync.Mutex
	pending []func()
	delays  []time.Duration
}

func (f *fakeScheduler) schedule(delay time.Duration, fn func()) func() {
	f.mu.Lock()
	f.pending = append(f.pending, fn)
	f.delays = append(f.delays, delay)
	f.mu.Unlock()
	return func() {}
}

func (f *fakeScheduler) fireAll() {
	f.mu.Lock()
	fns := f.pending
	f.pending = nil
	f.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (f *fakeScheduler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

// autoDriver builds a driver whose clock, jitter and policy the test owns.
func autoDriver(t *testing.T, policy string, sched *fakeScheduler) *Driver {
	t.Helper()
	d := New("instance-1", log.NewSlogAdapter(nil), WithPolicy(func() string { return policy }))
	d.schedule = sched.schedule
	d.jitter = func(delay time.Duration) time.Duration { return delay }
	return d
}

func drop(seq uint64, attempt int, cause string) proto.ConnectionStateEvent {
	return proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: seq, State: proto.ConnectionReconnecting,
		Attempt: attempt, Cause: cause, StateMs: 100,
	}
}

// TestAutoReconnectsADroppedPaneByItself is design §5.3: under `auto` the
// backend puts a network-class drop back without asking, through the same action
// a press takes.
func TestAutoReconnectsADroppedPaneByItself(t *testing.T) {
	sched := &fakeScheduler{}
	d := autoDriver(t, PolicyAuto, sched)
	calls := 0
	d.SetAction(func(string, uint64) error { calls++; return nil })
	d.Apply(drop(1, 1, causeLostLink))
	d.Bind("pane-1", "conn-a")
	if sched.count() == 0 {
		t.Fatal("auto scheduled no retry for a network-class drop")
	}
	sched.fireAll()
	if calls != 1 {
		t.Fatalf("%d reconnect(s), want 1", calls)
	}
}

// TestAskAndNeverDoNotReconnectByThemselves: the button is there, and nothing
// acts until a person presses it.
func TestAskAndNeverDoNotReconnectByThemselves(t *testing.T) {
	for _, policy := range []string{PolicyAsk, PolicyNever} {
		t.Run(policy, func(t *testing.T) {
			sched := &fakeScheduler{}
			d := autoDriver(t, policy, sched)
			calls := 0
			d.SetAction(func(string, uint64) error { calls++; return nil })
			d.Apply(drop(1, 1, causeLostLink))
			d.Bind("pane-1", "conn-a")
			if sched.count() != 0 {
				t.Fatalf("policy %q scheduled an automatic retry", policy)
			}
			sched.fireAll()
			if calls != 0 {
				t.Fatalf("policy %q reconnected by itself", policy)
			}
		})
	}
}

// TestANonNetworkCauseIsNotRetried: a changed host key or a refused sign-in is
// not something a retry can fix (design §5.3, "Only network-class failures
// retry").
func TestANonNetworkCauseIsNotRetried(t *testing.T) {
	sched := &fakeScheduler{}
	d := autoDriver(t, PolicyAuto, sched)
	d.SetAction(func(string, uint64) error { return nil })
	d.Apply(proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: 1, State: proto.ConnectionFailed, Cause: "host-key-changed",
	})
	d.Bind("pane-1", "conn-a")
	if sched.count() != 0 {
		t.Fatal("a changed host key scheduled a retry")
	}
}

// TestAReconnectInFlightSuppressesTheAutomaticOne: a person's press and the
// retry timer must not both open a shell.
func TestAReconnectInFlightSuppressesTheAutomaticOne(t *testing.T) {
	sched := &fakeScheduler{}
	d := autoDriver(t, PolicyAuto, sched)
	calls := 0
	d.SetAction(func(string, uint64) error { calls++; return nil })
	d.Apply(drop(1, 1, causeLostLink))
	d.Bind("pane-1", "conn-a")
	d.ReconnectStarted("pane-1")
	sched.fireAll()
	if calls != 0 {
		t.Fatalf("%d automatic reconnect(s) ran beside a press", calls)
	}
	d.ReconnectFinished("pane-1")
}

// TestTheBackoffGrowsAndCapsAtFifteenSeconds: design §5.3's cap replaced the
// three-attempt limit, and the first wait is short enough to land while the
// person is looking.
func TestTheBackoffGrowsAndCapsAtFifteenSeconds(t *testing.T) {
	sched := &fakeScheduler{}
	d := autoDriver(t, PolicyAuto, sched)
	d.SetAction(func(string, uint64) error { return nil })
	epochs := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for i := 1; i <= 10; i++ {
		sched.mu.Lock()
		sched.pending = nil
		sched.mu.Unlock()
		d.Apply(drop(epochs[i-1], i, causeLostLink))
		d.Bind("pane-1", "conn-a")
	}
	sched.mu.Lock()
	defer sched.mu.Unlock()
	if len(sched.delays) == 0 {
		t.Fatal("no retry was scheduled")
	}
	if first := sched.delays[0]; first != autoBackoffBase {
		t.Fatalf("first delay %v, want %v", first, autoBackoffBase)
	}
	for _, delay := range sched.delays {
		if delay > autoBackoffCap {
			t.Fatalf("a delay of %v exceeds the fifteen-second cap", delay)
		}
	}
}
