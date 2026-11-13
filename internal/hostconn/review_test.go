package hostconn_test

// The stage-2 review's findings, as tests. Each one states the invariant the
// review found broken and would fail if the fix were reverted.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestATaintedConnectionIsNotGivenToANewCaller — finding 6: the pool retires a
// connection from new admission (a detached writer's cap, a rotation), and the
// supervisor honours that instead of holding a second opinion. "May this be
// reused" has one owner.
func TestATaintedConnectionIsNotGivenToANewCaller(t *testing.T) {
	clock := hostconntest.New()
	var mu sync.Mutex
	var links []*quietLink
	carrier := &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) {
		mu.Lock()
		defer mu.Unlock()
		l := newQuietLink()
		links = append(links, l)
		return l, nil
	}}
	_, logger := logtest.New(t)
	mgr := hostconn.NewManager(carrier, logger, hostconn.WithClock(clock))
	first := acquire(t, mgr, hostconn.HoldsNone)

	links[0].tainted.Store(true)
	second, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire after taint: %v", err)
	}
	t.Cleanup(second.Release)
	mu.Lock()
	dials := len(links)
	mu.Unlock()
	if dials != 2 {
		t.Fatalf("%d dials after a taint, want a fresh connection", dials)
	}
	if second.Link() == first.Link() {
		t.Fatal("a tainted connection was handed to a new caller")
	}
	tainted := false
	for _, state := range first.States() {
		if state.Kind == hostconn.Reconnecting && state.Cause == hostconn.CauseTainted {
			tainted = true
		}
	}
	if !tainted {
		t.Fatalf("the retired connection never read reconnecting(tainted): %v", first.States())
	}
}

// TestAnAcquireOnAStalledConnectionWaits — finding 7: no channel is asked of a
// stalled connection. The acquire waits for the state to move, with the
// caller's deadline, instead of opening a session that would park.
func TestAnAcquireOnAStalledConnectionWaits(t *testing.T) {
	clock := hostconntest.New()
	link, _, mgr, sup := wakeStand(t, clock)
	first := acquire(t, mgr, hostconn.HoldsNone)

	// The cadence's probe parks, and the deadline reads stalled.
	clock.Advance(hostconn.ProbeInterval)
	waittest.WaitFor(t, "the cadence's probe", func() bool { return link.probeCalls.Load() == 1 })
	clock.Advance(hostconn.ProbeDeadline)
	waittest.WaitFor(t, "the stall", func() bool { return sup.Current().Kind == hostconn.Stalled })

	// An acquire now waits rather than opening a channel on it.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := mgr.Acquire(ctx, plan()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire on a stalled connection answered %v, want the caller's deadline", err)
	}
	if calls := link.probeCalls.Load(); calls != 1 {
		t.Fatalf("%d probes; the waiting acquire asked for a channel, not a probe", calls)
	}

	// When it answers, the same connection is handed out again.
	link.release()
	waittest.WaitFor(t, "the recovery", func() bool { return sup.Current().Kind == hostconn.Connected })
	third, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire after recovery: %v", err)
	}
	t.Cleanup(third.Release)
	if third.Link() != first.Link() {
		t.Fatal("the recovered connection was not the one waited for")
	}
}

// TestRidersFollowTheGenerationTheLeaseHolds — finding 8: rider accounting is
// per generation. Losing a connection must drop ITS riders once, and an old
// lease letting go must not subtract the protection of the connection that
// replaced it.
func TestRidersFollowTheGenerationTheLeaseHolds(t *testing.T) {
	clock := hostconntest.New()
	var mu sync.Mutex
	links := 0
	carrier := &memoryCarrier{dialPlan: func(hostconn.Plan, hostconn.EventSink) (hostconn.Link, error) {
		mu.Lock()
		defer mu.Unlock()
		links++
		l := newQuietLink()
		if links > 1 {
			l.memoryLink.probe = func() (time.Duration, error) { return time.Millisecond, nil }
		}
		return l, nil
	}}
	_, logger := logtest.New(t)
	mgr := hostconn.NewManager(carrier, logger, hostconn.WithClock(clock))

	hop := hostconn.Key{Host: "bastion.example", Port: 22, User: "dev", Identity: "hop"}
	hopSup := mgr.Supervisor(hop)

	p := plan()
	p.Holds = hostconn.HoldsPlainShell
	p.Route = []hostconn.Key{hop}
	old, err := mgr.Acquire(context.Background(), p)
	if err != nil {
		t.Fatalf("Acquire through the hop: %v", err)
	}
	if !hopSup.HoldsPlainShell() {
		t.Fatal("the hop does not count the rider")
	}

	// The connection dies; the generation's riders go with it.
	oldLink, _ := old.Link().(*quietLink)
	if oldLink != nil {
		oldLink.end(errors.New("connection reset"))
	}
	waittest.WaitFor(t, "the loss to be reported", func() bool {
		return last(old.States()).Kind == hostconn.Reconnecting
	})
	if hopSup.HoldsPlainShell() {
		t.Fatal("the dead generation's rider was left on the hop")
	}

	// A replacement with its own rider: releasing the OLD lease must not
	// subtract the new generation's protection.
	replacement, err := mgr.Acquire(context.Background(), p)
	if err != nil {
		t.Fatalf("Acquire the replacement: %v", err)
	}
	t.Cleanup(replacement.Release)
	if !hopSup.HoldsPlainShell() {
		t.Fatal("the replacement does not count its rider")
	}
	old.Release()
	if !hopSup.HoldsPlainShell() {
		t.Fatal("releasing the dead generation's lease dropped the replacement's rider")
	}
}

// TestASleepEdgeNewerThanAWakeIsNotUndone — finding 9: control edges are
// ordered by their own readings, so a stale wake cannot resume a connection a
// later sleep has suspended (and a later wake does resume it).
func TestASleepEdgeNewerThanAWakeIsNotUndone(t *testing.T) {
	clock := hostconntest.New()
	_, _, mgr, sup := wakeStand(t, clock)
	lease := acquire(t, mgr, hostconn.HoldsPlainShell)

	base := clock.Now()
	sup.Suspend(base + monoclock.Nanos(2*time.Second))
	waittest.WaitFor(t, "the suspension", lease.Suspended)

	// A wake from BEFORE the sleep arrives late: it must not resume anything.
	sup.Wake(base + monoclock.Nanos(time.Second))
	waitABit()
	if !lease.Suspended() {
		t.Fatal("an older wake cleared a newer sleep edge")
	}

	// A wake after the sleep does resume it.
	sup.Wake(base + monoclock.Nanos(3*time.Second))
	waittest.WaitFor(t, "the resume", func() bool { return !lease.Suspended() })
}

// TestAnAnswerDoesNotClearALaterSleep — finding 9: a probe sent before the
// sleep answered about a machine that was already going away, so its answer is
// not evidence that the connection is awake.
func TestAnAnswerDoesNotClearALaterSleep(t *testing.T) {
	clock := hostconntest.New()
	link, _, mgr, sup := wakeStand(t, clock)
	lease := acquire(t, mgr, hostconn.HoldsPlainShell)

	// The cadence's probe is sent and parks.
	clock.Advance(hostconn.ProbeInterval)
	waittest.WaitFor(t, "the probe", func() bool { return link.probeCalls.Load() == 1 })

	// The sleep edge follows the probe's send.
	clock.Advance(time.Second)
	sup.Suspend(clock.Now())
	waittest.WaitFor(t, "the suspension", lease.Suspended)

	// The parked probe answers; the connection is still going to sleep.
	link.release()
	waittest.WaitFor(t, "the answer", func() bool { return sup.Current().Kind == hostconn.Connected })
	if !lease.Suspended() {
		t.Fatal("a probe sent before the sleep treated its answer as a wake")
	}

	// The wake is what clears it.
	sup.Wake(clock.Now() + monoclock.Nanos(time.Second))
	waittest.WaitFor(t, "the wake", func() bool { return !lease.Suspended() })
}

// TestAWakeDuringTheDialIsNotLost — finding 9: the five-second proof survives
// the window between a successful dial and its publication.
func TestAWakeDuringTheDialIsNotLost(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	released := make(chan struct{})
	carrier := &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) {
		<-released
		return link, nil
	}}
	_, logger := logtest.New(t)
	mgr := hostconn.NewManager(carrier, logger, hostconn.WithClock(clock))
	sup := mgr.Supervisor(plan().Key)

	acquired := make(chan *hostconn.Lease, 1)
	go func() {
		lease, err := mgr.Acquire(context.Background(), plan())
		if err == nil {
			acquired <- lease
		}
	}()

	// The wake arrives while the dial is in flight.
	clock.Advance(time.Second)
	sup.Wake(clock.Now())
	close(released)

	var lease *hostconn.Lease
	waittest.WaitFor(t, "the dial to publish", func() bool {
		select {
		case lease = <-acquired:
			return true
		default:
			return false
		}
	})
	t.Cleanup(lease.Release)

	// The published connection proves itself at once, without a cadence.
	waittest.WaitFor(t, "the wake's probe", func() bool { return link.probeCalls.Load() == 1 })
	clock.Advance(hostconn.WakeProbeDeadline)
	waittest.WaitFor(t, "the wake's stall", func() bool { return sup.Current().Kind == hostconn.Stalled })
}

// TestTheReleaseBarrierReusesTheOneProber — finding 1: the barrier asks the
// supervisor for one answer, and a probe already in flight is the evidence.
func TestTheReleaseBarrierReusesTheOneProber(t *testing.T) {
	clock := hostconntest.New()
	link, _, mgr, sup := wakeStand(t, clock)
	var probes atomic.Int64
	link.memoryLink.probe = func() (time.Duration, error) {
		probes.Add(1)
		return time.Millisecond, nil
	}
	held := acquire(t, mgr, hostconn.HoldsPlainShell)

	if err := sup.Probe(context.Background()); err != nil {
		t.Fatalf("Probe on a healthy connection: %v", err)
	}
	if calls := probes.Load(); calls != 1 {
		t.Fatalf("%d probes for one barrier request, want exactly the one prober's", calls)
	}

	// With a probe parked, the barrier waits on it and sends nothing.
	link.memoryLink.probe = func() (time.Duration, error) {
		probes.Add(1)
		<-link.probeBlock
		return 0, nil
	}
	clock.Advance(hostconn.ProbeInterval)
	waittest.WaitFor(t, "the parked probe", func() bool { return probes.Load() == 2 })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := sup.Probe(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a barrier over a parked probe answered %v, want its own deadline", err)
	}
	if calls := probes.Load(); calls != 2 {
		t.Fatalf("%d probes: the barrier started a second prober", calls)
	}
	// And a barrier that could not get an answer did not close anything: the
	// closing policy is the supervisor's, never the caller's.
	if held.Lost() {
		t.Fatal("the barrier closed the connection it could not get an answer from")
	}
}
