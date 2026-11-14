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
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
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

// TestASilentConnectionReadsStalledWithinThirtySeconds is Q3: fifteen seconds to
// the next probe plus its fifteen-second deadline, and the parked probe is what
// notices the far side the moment it speaks.
func TestASilentConnectionReadsStalledWithinThirtySeconds(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	mgr, sup := managerOn(t, quietCarrier(link), clock)
	lease := acquire(t, mgr, hostconn.HoldsNone)

	clock.Advance(30 * time.Second)
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
}

// TestAPlainShellConnectionStaysOpenAfterTenMinutesOfSilence is the owner's rule
// (design §5.3: "Silence never ends a shell that would die with the
// connection"): ten minutes of blackhole on the injected clock, and the
// connection is still there.
func TestAPlainShellConnectionStaysOpenAfterTenMinutesOfSilence(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	mgr, sup := managerOn(t, quietCarrier(link), clock)
	lease := acquire(t, mgr, hostconn.HoldsPlainShell)

	clock.Advance(30 * time.Second)
	waittest.WaitFor(t, "the connection to read stalled", func() bool {
		return sup.Current().Kind == hostconn.Stalled
	})
	waittest.WaitFor(t, "the host's new connections to be answered", func() bool {
		looks, _ := link.counters()
		return looks > 0
	})

	clock.Advance(10 * time.Minute)
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
}

// TestAHelperOwnedConnectionClosesThirtySecondsAfterAnAnsweredLook is the other
// side of the same rule: nothing is lost by closing a connection whose shells
// are all helper-owned, so the evidence of a host that answers new connections
// plus the grace ends it.
func TestAHelperOwnedConnectionClosesThirtySecondsAfterAnAnsweredLook(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	mgr, sup := managerOn(t, quietCarrier(link), clock)
	lease := acquire(t, mgr, hostconn.HoldsNone)

	clock.Advance(30 * time.Second)
	waittest.WaitFor(t, "the connection to read stalled", func() bool {
		return sup.Current().Kind == hostconn.Stalled
	})
	// The look is answered at thirty seconds, and the nudge follows it.
	waittest.WaitFor(t, "the look to be answered and the nudge sent", func() bool {
		_, nudges := link.counters()
		return nudges == 1
	})

	clock.Advance(30 * time.Second)
	waittest.WaitFor(t, "the connection to be closed for silence", func() bool {
		return sup.Current().Kind == hostconn.Reconnecting
	})
	if got := sup.Current().Cause; got != hostconn.CauseSilence {
		t.Fatalf("a connection closed for silence reads cause %q, want %q", got, hostconn.CauseSilence)
	}
	waittest.WaitFor(t, "the transport to be closed", func() bool { return lease.Lost() })
}

// TestAHelperOwnedConnectionClosesAtTheSilenceBudget is the case with no
// evidence at all: the host answers neither the probe nor a new connection, and
// five minutes is the outer bound.
func TestAHelperOwnedConnectionClosesAtTheSilenceBudget(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	link.lookAlwaysError = true
	mgr, sup := managerOn(t, quietCarrier(link), clock)
	lease := acquire(t, mgr, hostconn.HoldsNone)

	clock.Advance(30 * time.Second)
	waittest.WaitFor(t, "the connection to read stalled", func() bool {
		return sup.Current().Kind == hostconn.Stalled
	})
	// The budget counts from the moment this guard JUDGED the connection
	// silent (30 s here), never from the probe's send: a close may not rest on
	// one reading taken after a clock jump (see due's close branch).
	clock.Advance(hostconn.SilenceCloseBudget)
	waittest.WaitFor(t, "the connection to be closed at the budget", func() bool {
		return sup.Current().Kind == hostconn.Reconnecting
	})
	if got := sup.Current().Cause; got != hostconn.CauseSilence {
		t.Fatalf("closed for silence with cause %q", got)
	}
	if !lease.Lost() {
		t.Fatal("the transport outlived the budget")
	}
}

// TestAHealthyConnectionNeverReadsStalledOverTenMinutes is the paired success:
// a connection that answers is never reported as silent, for forty probes.
//
// The probes are ASKED FOR, not left to the cadence's scheduling: each
// `Supervisor.Probe` is the connection making itself answer now (the same act
// the release barrier asks for), so the test states "forty answers arrive and
// none is silence" without depending on when a timer happened to be noticed.
func TestAHealthyConnectionNeverReadsStalledOverTenMinutes(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	var probes atomic.Int64
	link.memoryLink.probe = func() (time.Duration, error) {
		probes.Add(1)
		return time.Millisecond, nil
	}
	mgr, sup := managerOn(t, quietCarrier(link), clock)
	lease := acquire(t, mgr, hostconn.HoldsPlainShell)

	for i := 0; i < 40; i++ {
		clock.Advance(hostconn.ProbeInterval)
		if err := sup.Probe(context.Background()); err != nil {
			t.Fatalf("probe %d: %v", i+1, err)
		}
		if got := last(lease.States()).Kind; got == hostconn.Stalled {
			t.Fatalf("a healthy connection reads %v after %d probes", got, i+1)
		}
	}
	if got := probes.Load(); got < 40 {
		t.Fatalf("%d probes for forty asks, want at least forty", got)
	}
	for _, state := range lease.States() {
		if state.Kind == hostconn.Stalled {
			t.Fatalf("a healthy connection was reported stalled: %v", lease.States())
		}
	}
}

// TestTheLookStopsAfterTheFirstAnswer — a look can be logged as a preauth close,
// so a person whose link is down must not be locked out by their own client.
func TestTheLookStopsAfterTheFirstAnswer(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	mgr, _ := managerOn(t, quietCarrier(link), clock)
	acquire(t, mgr, hostconn.HoldsPlainShell)

	clock.Advance(30 * time.Second)
	waittest.WaitFor(t, "the first look", func() bool { looks, _ := link.counters(); return looks == 1 })
	clock.Advance(2 * time.Minute)
	// Give the guard a chance to make another look if it were going to.
	waittest.WaitFor(t, "the clock advance to be seen", func() bool { return true })
	looks, nudges := link.counters()
	if looks != 1 {
		t.Fatalf("%d looks after the first answer, want 1: the cadence stops when the host answers", looks)
	}
	if nudges != 1 {
		t.Fatalf("%d nudges, want 1: one nudge, after the first answered look", nudges)
	}
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
	clock := hostconntest.New()
	link := newQuietLink()
	link.memoryLink.probe = func() (time.Duration, error) { return 0, errors.New("connection reset") }
	mgr, sup := managerOn(t, quietCarrier(link), clock)
	lease := acquire(t, mgr, hostconn.HoldsNone)

	clock.Advance(hostconn.ProbeInterval)
	// A probe error is a transport error: the connection is over, and Done is
	// what says so. The guard must not read it as silence.
	link.end(errors.New("connection reset"))
	waittest.WaitFor(t, "the transport's end", func() bool { return lease.Lost() })
	waittest.WaitFor(t, "the loss to be reported", func() bool {
		return sup.Current().Kind == hostconn.Reconnecting
	})
}

// TestObservationsReachTheLease is the liveness path the app's host status
// hangs on, re-pointed at the supervisor's one prober.
func TestObservationsReachTheLease(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	mgr, _ := managerOn(t, quietCarrier(link), clock)
	lease := acquire(t, mgr, hostconn.HoldsPlainShell)

	seen := make(chan hostconn.Observation, 8)
	lease.Observe(func(o hostconn.Observation) { seen <- o })

	clock.Advance(30 * time.Second)
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
}
