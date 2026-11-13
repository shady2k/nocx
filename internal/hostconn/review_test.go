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
	// The guard is made to go round its loop (the probe ask), so "still
	// suspended" is read after the edge was certainly applied, not after a
	// pause.
	sup.Wake(base + monoclock.Nanos(time.Second))
	syncGuard(t, sup)
	if !lease.Suspended() {
		t.Fatal("an older wake cleared a newer sleep edge")
	}

	// A wake after the sleep does resume it.
	sup.Wake(base + monoclock.Nanos(3*time.Second))
	waittest.WaitFor(t, "the resume", func() bool { return !lease.Suspended() })
}

// syncGuard makes the guard complete one loop, so a test that asserts the
// ABSENCE of an effect reads it after the edge was certainly applied. The ask
// is the connection's own door; its short deadline is what makes the wait
// bounded, and the assertion that follows is about state, never about time.
func syncGuard(t *testing.T, sup *hostconn.Supervisor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = sup.Probe(ctx)
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

// TestAnAcquireDuringTheLastReleaseWaits — the verification's last-release
// race: the fence and the decrement happen under ONE hold of the admission
// mutex, so an acquire that arrives while the teardown is in progress waits for
// it instead of joining a connection nobody holds (and being closed with it).
func TestAnAcquireDuringTheLastReleaseWaits(t *testing.T) {
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
	held := acquire(t, mgr, hostconn.HoldsNone)
	link, _ := held.Link().(*quietLink)
	link.closeGate = make(chan struct{})

	released := make(chan struct{})
	go func() {
		held.Release()
		close(released)
	}()
	// The teardown is physically in progress: Close has been entered and is
	// held at its gate.
	select {
	case <-link.closeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the release never reached the physical close")
	}

	// An acquire now must NOT be handed that connection.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if lease, err := mgr.Acquire(ctx, plan()); err == nil {
		lease.Release()
		t.Fatal("an acquire joined a connection whose last release was tearing it down")
	}

	close(link.closeGate)
	<-released

	// And once it is gone, a fresh acquire dials.
	next, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire after the teardown: %v", err)
	}
	t.Cleanup(next.Release)
	mu.Lock()
	dials := len(links)
	mu.Unlock()
	if dials != 2 {
		t.Fatalf("%d dials, want a fresh connection after the teardown", dials)
	}
}

// TestATaintedConnectionKeepsItsExistingSiblings — the verification's taint
// regression: taint means "no new holder", never "kill the holders you have".
func TestATaintedConnectionKeepsItsExistingSiblings(t *testing.T) {
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
	sibling := acquire(t, mgr, hostconn.HoldsPlainShell)

	links[0].tainted.Store(true)
	fresh, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire after taint: %v", err)
	}
	t.Cleanup(fresh.Release)
	if fresh.Link() == sibling.Link() {
		t.Fatal("a tainted connection took a new holder")
	}
	// The sibling is untouched by the decision about admission.
	if sibling.Lost() {
		t.Fatal("a tainted connection's transport was closed under its existing holder")
	}
	if got := last(sibling.States()).Kind; got == hostconn.Ended {
		t.Fatalf("the tainted connection reads %v while it still has a holder", got)
	}

	// It closes at ITS last release, as the pool's taint always promised.
	sibling.Release()
	waittest.WaitFor(t, "the tainted connection to close at its last release", sibling.Lost)
}

// TestACoalescedBurstKeepsTheLastWake — the control queue coalesces per kind
// and never drops an edge: after any burst, the newest wake is what the guard
// applies.
func TestACoalescedBurstKeepsTheLastWake(t *testing.T) {
	clock := hostconntest.New()
	link, _, mgr, sup := wakeStand(t, clock)
	lease := acquire(t, mgr, hostconn.HoldsPlainShell)

	// Each edge is stamped at the reading it was seen; the clock moves between
	// them so every reading is honest and the burst really is four edges.
	sup.Suspend(clock.Now())
	clock.Advance(time.Second)
	sup.Wake(clock.Now())
	clock.Advance(time.Second)
	sup.Suspend(clock.Now())
	clock.Advance(time.Second)
	sup.Wake(clock.Now())

	// The newest edge is a wake: the connection is not suspended, and it proves
	// itself at once.
	waittest.WaitFor(t, "the final wake to be applied", func() bool { return !lease.Suspended() })
	waittest.WaitFor(t, "the wake's probe", func() bool { return link.probeCalls.Load() == 1 })
}

// TestASleepDuringTheDialPublishesSuspended — the control queue's other half:
// a sleep edge that arrives while the connection is being dialled is honoured
// by the published guard, which then asks a sleeping machine nothing.
func TestASleepDuringTheDialPublishesSuspended(t *testing.T) {
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

	clock.Advance(time.Second)
	sup.Suspend(clock.Now())
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

	waittest.WaitFor(t, "the published connection to read suspended", lease.Suspended)
	clock.Advance(10 * time.Minute)
	if calls := link.probeCalls.Load(); calls != 0 {
		t.Fatalf("%d probes were sent to a machine that is asleep", calls)
	}
	sup.Wake(clock.Now())
	waittest.WaitFor(t, "the wake", func() bool { return !lease.Suspended() })
	waittest.WaitFor(t, "the wake's probe", func() bool { return link.probeCalls.Load() == 1 })
}
