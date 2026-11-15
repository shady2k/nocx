package hostconn_test

// The closing policy, on a clock the test advances (design §5.3, "Silence and
// closing"; §10: time is an injected clock, never a sleep).
//
// One rule is asserted from every side: silence ends nothing by itself. A
// stalled connection is reported; a connection carrying a plain shell stays;
// a connection whose shells are all helper-owned closes — on the evidence of a
// look-only dial plus a grace, or at the five-minute budget; and a transport
// that errors ends it at once.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/waittest"
)

// quietLink is one connection whose far side a test scripts: whether it answers
// a probe (never, by blocking on a gate), whether a look-only dial is answered,
// and what it counts.
type quietLink struct {
	*memoryLink
	probeBlock chan struct{}
	probeErr   error
	lookErr    error
	// lookAlwaysError makes every look fail, which is a host that answers
	// nothing at all.
	lookAlwaysError bool
	probeCalls      atomic.Int32
}

func newQuietLink() *quietLink {
	q := &quietLink{memoryLink: newMemoryLink(), probeBlock: make(chan struct{})}
	q.memoryLink.probe = func() (time.Duration, error) {
		q.probeCalls.Add(1)
		<-q.probeBlock
		return 0, q.probeErr
	}
	q.memoryLink.look = func() error {
		if q.lookAlwaysError {
			return errors.New("the host did not answer")
		}
		return q.lookErr
	}
	return q
}

func (q *quietLink) release() { close(q.probeBlock) }

// quietCarrier answers the scripted link the test built.
func quietCarrier(link *quietLink) *memoryCarrier {
	return &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) { return link, nil }}
}

func acquire(t *testing.T, mgr *hostconn.Manager, holds hostconn.Holds) *hostconn.Lease {
	t.Helper()
	p := plan()
	p.Holds = holds
	lease, err := mgr.Acquire(context.Background(), p)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(lease.Release)
	return lease
}

// advancingBeforeWaitClock stages a clock jump between the guard reading Now
// and registering its relative wait. The clock then stays frozen.
type advancingBeforeWaitClock struct {
	*hostconntest.Clock
	once sync.Once
}

func (c *advancingBeforeWaitClock) After(d time.Duration) <-chan monoclock.Nanos {
	c.once.Do(func() { c.Advance(hostconn.ProbeInterval + hostconn.ProbeDeadline) })
	return c.Clock.After(d)
}

func TestAClockAdvanceBeforeWaitRegistrationStillReportsSilence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := &advancingBeforeWaitClock{Clock: hostconntest.New()}
		link := newQuietLink()
		defer link.release()
		mgr, sup := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsPlainShell)
		defer lease.Release()

		// All runnable work has finished. A relative timer registered after
		// the jump cannot fire without another advance; the deadline is
		// already due and must be handled without one.
		synctest.Wait()
		if got := sup.Current().Kind; got != hostconn.Stalled {
			t.Fatalf("a silent connection reads %v after its deadline, want stalled", got)
		}
		if lease.Lost() {
			t.Fatal("reporting silence ended a plain shell's connection")
		}
	})
}

// TestASilentConnectionReadsStalledWithinThirtySeconds is Q3: fifteen seconds to
// the next probe plus its fifteen-second deadline, and the parked probe is what
// notices the far side the moment it speaks.
func TestASilentConnectionReadsStalledWithinThirtySeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		mgr, sup := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsNone)
		defer lease.Release()

		synctest.Wait()
		clock.Advance(30 * time.Second)
		synctest.Wait()
		waittest.WaitFor(t, "the connection to read stalled", func() bool {
			return sup.Current().Kind == hostconn.Stalled
		})
		if got := sup.Current().Silent; got != 15*time.Second {
			t.Fatalf("stalled reports %v of silence, want 15s (the unanswered probe)", got)
		}
		if lease.Lost() {
			t.Fatal("a stalled connection is not a lost one")
		}

		// And the parked probe's answer returns it to connected.
		link.release()
		waittest.WaitFor(t, "the stalled connection to answer again", func() bool {
			return sup.Current().Kind == hostconn.Connected
		})
	})
}

// TestAPlainShellConnectionStaysOpenAfterTenMinutesOfSilence is the owner's rule
// (design §5.3: "Silence never ends a shell that would die with the
// connection"): ten minutes of blackhole on the injected clock, and the
// connection is still there.
func TestAPlainShellConnectionStaysOpenAfterTenMinutesOfSilence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		mgr, sup := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsPlainShell)
		defer lease.Release()

		synctest.Wait()
		clock.Advance(30 * time.Second)
		synctest.Wait()
		waittest.WaitFor(t, "the connection to read stalled", func() bool {
			return sup.Current().Kind == hostconn.Stalled
		})
		waittest.WaitFor(t, "the host's new connections to be answered", func() bool {
			looks, _ := link.counters()
			return looks > 0
		})

		synctest.Wait()
		clock.Advance(10 * time.Minute)
		synctest.Wait()
		// The state stays stalled and nobody closed the transport: waiting is the
		// person's choice.
		if got := sup.Current().Kind; got != hostconn.Stalled {
			t.Fatalf("a stalled plain-shell connection reads %v after ten minutes, want stalled", got)
		}
		if lease.Lost() {
			t.Fatal("a plain-shell connection was closed for silence")
		}
		link.release()
		waittest.WaitFor(t, "the connection to answer again", func() bool {
			return sup.Current().Kind == hostconn.Connected
		})
	})
}

// TestAHelperOwnedConnectionClosesThirtySecondsAfterAnAnsweredLook is the other
// side of the same rule: nothing is lost by closing a connection whose shells
// are all helper-owned, so the evidence of a host that answers new connections
// plus the grace ends it.
func TestAHelperOwnedConnectionClosesThirtySecondsAfterAnAnsweredLook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		defer link.release()
		mgr, sup := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsNone)
		defer lease.Release()

		synctest.Wait()
		clock.Advance(30 * time.Second)
		synctest.Wait()
		waittest.WaitFor(t, "the connection to read stalled", func() bool {
			return sup.Current().Kind == hostconn.Stalled
		})
		// The look is answered at thirty seconds, and the nudge follows it.
		waittest.WaitFor(t, "the look to be answered and the nudge sent", func() bool {
			_, nudges := link.counters()
			return nudges == 1
		})

		synctest.Wait()
		clock.Advance(30 * time.Second)
		synctest.Wait()
		waittest.WaitFor(t, "the connection to be closed for silence", func() bool {
			return sup.Current().Kind == hostconn.Reconnecting
		})
		if got := sup.Current().Cause; got != hostconn.CauseSilence {
			t.Fatalf("a connection closed for silence reads cause %q, want %q", got, hostconn.CauseSilence)
		}
		waittest.WaitFor(t, "the transport to be closed", func() bool { return lease.Lost() })
	})
}

// TestAHelperOwnedConnectionClosesAtTheSilenceBudget is the case with no
// evidence at all: the host answers neither the probe nor a new connection, and
// five minutes is the outer bound.
func TestAHelperOwnedConnectionClosesAtTheSilenceBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		defer link.release()
		link.lookAlwaysError = true
		mgr, sup := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsNone)
		defer lease.Release()

		synctest.Wait()
		clock.Advance(30 * time.Second)
		synctest.Wait()
		waittest.WaitFor(t, "the connection to read stalled", func() bool {
			return sup.Current().Kind == hostconn.Stalled
		})
		// The budget counts from the moment this guard JUDGED the connection
		// silent (30 s here), never from the probe's send: a close may not rest on
		// one reading taken after a clock jump (see due's close branch).
		synctest.Wait()
		clock.Advance(hostconn.SilenceCloseBudget)
		synctest.Wait()
		waittest.WaitFor(t, "the connection to be closed at the budget", func() bool {
			return sup.Current().Kind == hostconn.Reconnecting
		})
		if got := sup.Current().Cause; got != hostconn.CauseSilence {
			t.Fatalf("closed for silence with cause %q", got)
		}
		if !lease.Lost() {
			t.Fatal("the transport outlived the budget")
		}
	})
}

// TestAHealthyConnectionNeverReadsStalledOverTenMinutes is the paired success:
// a connection that answers is never reported as silent, for forty probes.
//
// Each interval delivers its automatic probe and its answered observation
// before time advances again: forty answers arrive and none is silence.
func TestAHealthyConnectionNeverReadsStalledOverTenMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		link.memoryLink.probe = func() (time.Duration, error) { return time.Millisecond, nil }
		mgr, sup := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsPlainShell)
		defer lease.Release()
		var answers atomic.Int64
		lease.Observe(func(obs hostconn.Observation) {
			if obs.Answered {
				answers.Add(1)
			}
		})

		// The automatic probe must finish and the guard must schedule its next
		// wake before another interval passes. A manual Probe can join the
		// previous call, so its return is not proof this interval was answered.
		synctest.Wait()
		for i := 0; i < 40; i++ {
			clock.Advance(hostconn.ProbeInterval)
			synctest.Wait()
			if got := answers.Load(); got != int64(i+1) {
				t.Fatalf("after %d intervals: %d answered probes, want %d", i+1, got, i+1)
			}
			if got := last(lease.States()).Kind; got != hostconn.Connected {
				t.Fatalf("a healthy connection reads %v after %d probes", got, i+1)
			}
		}
		for _, state := range sup.States() {
			if state.Kind == hostconn.Stalled {
				t.Fatalf("a healthy connection was reported stalled: %v", sup.States())
			}
		}
	})
}

// TestTheLookStopsAfterTheFirstAnswer — a look can be logged as a preauth close,
// so a person whose link is down must not be locked out by their own client.
func TestTheLookStopsAfterTheFirstAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		defer link.release()
		mgr, _ := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsPlainShell)
		defer lease.Release()

		synctest.Wait()
		clock.Advance(30 * time.Second)
		synctest.Wait()
		waittest.WaitFor(t, "the first look", func() bool { looks, _ := link.counters(); return looks == 1 })
		synctest.Wait()
		clock.Advance(2 * time.Minute)
		synctest.Wait()
		looks, nudges := link.counters()
		if looks != 1 {
			t.Fatalf("%d looks after the first answer, want 1: the cadence stops when the host answers", looks)
		}
		if nudges != 1 {
			t.Fatalf("%d nudges, want 1: one nudge, after the first answered look", nudges)
		}
	})
}

// TestAPlainShellRidingAHopKeepsTheHopOpen is §5.1.1's second trap: the hop's
// plain shells are its own plus those of everything riding it.
func TestAPlainShellRidingAHopKeepsTheHopOpen(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	mgr, _ := managerOn(t, quietCarrier(link), clock)

	hop := hostconn.Key{Host: "bastion.example", Port: 22, User: "dev", Identity: "hop-key"}
	p := plan()
	p.Holds = hostconn.HoldsPlainShell
	p.Route = []hostconn.Key{hop}
	lease, err := mgr.Acquire(context.Background(), p)
	if err != nil {
		t.Fatalf("Acquire through a hop: %v", err)
	}
	if !mgr.Supervisor(hop).HoldsPlainShell() {
		t.Fatal("a plain shell riding a hop left the hop counting nothing: it would be free to close under the shell")
	}

	// Nothing riding: the hop counts its own holders only.
	lease.Release()
	waittest.WaitFor(t, "the rider to leave the hop", func() bool {
		return !mgr.Supervisor(hop).HoldsPlainShell()
	})
}

// TestAProbeItselfEndingTheTransportIsReportedAsLost — the transport error is
// the fact, and nothing about it is an inference.
func TestAProbeErrorEndsTheGuardAndLeavesTheWatcherToReportTheLoss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		defer link.release()
		link.memoryLink.probe = func() (time.Duration, error) { return 0, errors.New("connection reset") }
		mgr, sup := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsNone)
		defer lease.Release()

		synctest.Wait()
		clock.Advance(hostconn.ProbeInterval)
		synctest.Wait()
		// A probe error is a transport error: the connection is over, and Done is
		// what says so. The guard must not read it as silence.
		link.end(errors.New("connection reset"))
		waittest.WaitFor(t, "the transport's end", func() bool { return lease.Lost() })
		waittest.WaitFor(t, "the loss to be reported", func() bool {
			return sup.Current().Kind == hostconn.Reconnecting
		})
	})
}

// TestObservationsReachTheLease is the liveness path the app's host status
// hangs on, re-pointed at the supervisor's one prober.
func TestObservationsReachTheLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		mgr, _ := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsPlainShell)
		defer lease.Release()

		seen := make(chan hostconn.Observation, 8)
		lease.Observe(func(o hostconn.Observation) { seen <- o })

		synctest.Wait()
		clock.Advance(30 * time.Second)
		synctest.Wait()
		select {
		case o := <-seen:
			if o.Answered {
				t.Fatalf("an unanswered probe was reported as answered: %+v", o)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the silence was never reported to the lease that asked")
		}

		link.release()
		select {
		case o := <-seen:
			if !o.Answered {
				t.Fatalf("the answer was reported as silence: %+v", o)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the answer was never reported to the lease that asked")
		}
	})
}

// TestASecondLookIsNotStartedWhileTheFirstOnesWorkIsStillOut is the bound on the
// look's leftovers: a look returns when its context ends — that is what frees the
// guard's slot — and the read or the channel open it started may still be parked
// on the hop. A connection carrying a plain shell can stay stalled for hours at a
// fifteen second cadence, so each of those would add one parked goroutine and one
// open channel; the guard therefore starts a look only once the previous one's
// work is over.
//
// The assertion is WHEN the looks happened, not only how many: a look at the next
// interval is the defect, and its clock reading is a fact the carrier recorded,
// so no part of this test rests on a pause. The person's evidence is unchanged —
// a silent hop answers no look either way.
func TestASecondLookIsNotStartedWhileTheFirstOnesWorkIsStillOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		defer link.release()
		var mu sync.Mutex
		var at []monoclock.Nanos
		// The look returns at once, as a cancelled one does, and reports that the
		// work it started is still out there.
		link.look = func() error {
			mu.Lock()
			at = append(at, clock.Now())
			mu.Unlock()
			link.lookParked.Store(true)
			return errors.New("the hop is silent")
		}
		looks := func() []monoclock.Nanos {
			mu.Lock()
			defer mu.Unlock()
			return append([]monoclock.Nanos(nil), at...)
		}
		mgr, sup := managerOn(t, quietCarrier(link), clock)
		lease := acquire(t, mgr, hostconn.HoldsPlainShell)
		defer lease.Release()

		// Stalled by a probe that never comes back, so the look cadence starts.
		synctest.Wait()
		clock.Advance(hostconn.ProbeInterval)
		synctest.Wait()
		clock.Advance(hostconn.ProbeDeadline)
		synctest.Wait()
		waittest.WaitFor(t, "the stall", func() bool { return sup.Current().Kind == hostconn.Stalled })
		waittest.WaitFor(t, "the first look", func() bool { return len(looks()) == 1 })

		// Three whole intervals pass with the first look's work still out there.
		synctest.Wait()
		clock.Advance(3 * hostconn.LookInterval)
		synctest.Wait()

		// The work ends — the hop's connection does, in the product — and the clock
		// moves one more interval, which is when the next look goes out.
		link.lookParked.Store(false)
		synctest.Wait()
		clock.Advance(hostconn.LookInterval)
		synctest.Wait()
		waittest.WaitFor(t, "the next look once the first one's work ended", func() bool { return len(looks()) >= 2 })

		// TWO, and never more: the first look's fake reports parked on every call, so
		// with the bound in place the second look is the last one that can start. A
		// guard that ignored the bound starts one per overdue interval — four at the
		// same instant here — which is the accumulation.
		if got := len(looks()); got != 2 {
			t.Fatalf("%d looks went out, want 2: %v after the first", got, looks()[1:])
		}
	})
}
