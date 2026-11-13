package hostconn_test

// The wake policy (design §5.3, "On a wake signal"; Q4). A wake is the same
// closing policy with a shorter clock: prove yourself at once, five seconds of
// silence reads stalled, thirty seconds of it ends a helper-owned connection,
// and a plain shell follows the silence rule.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// wakeStand is one live connection with the test's own wake source.
func wakeStand(t *testing.T, clock *hostconntest.Clock) (*quietLink, *hostconntest.WakeSource, *hostconn.Manager, *hostconn.Supervisor) {
	t.Helper()
	link := newQuietLink()
	wake := hostconntest.NewWakeSource(clock)
	mgr, sup := managerWith(t, quietCarrier(link), clock, wake)
	return link, wake, mgr, sup
}

// TestAWakeProbesAtOnceAndAHealthyConnectionStaysConnected — a wake asks every
// connection to prove itself now, not at its next interval.
func TestAWakeProbesAtOnceAndAHealthyConnectionStaysConnected(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	var probes atomic.Int32
	link.memoryLink.probe = func() (time.Duration, error) {
		probes.Add(1)
		return time.Millisecond, nil
	}
	wake := hostconntest.NewWakeSource(clock)
	mgr, sup := managerWith(t, quietCarrier(link), clock, wake)
	acquire(t, mgr, hostconn.HoldsPlainShell)

	// Nothing has been asked for yet: the first interval has not elapsed.
	if calls := probes.Load(); calls != 0 {
		t.Fatalf("%d probes before the first interval", calls)
	}
	wake.Wake()
	waittest.WaitFor(t, "the wake's probe", func() bool { return probes.Load() == 1 })
	// No clock advance at all: the probe is the wake's, sent at the signal.
	waittest.WaitFor(t, "the answer", func() bool { return sup.Current().Kind == hostconn.Connected })
	if got := sup.Current().Kind; got != hostconn.Connected {
		t.Fatalf("a healthy connection reads %v after a wake, want connected", got)
	}
}

// TestNoWakeMeansNoExtraProbe is the paired absence: without a signal, the
// cadence is the only prober.
func TestNoWakeMeansNoExtraProbe(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	mgr, _ := managerWith(t, quietCarrier(link), clock, hostconntest.NewWakeSource(clock))
	acquire(t, mgr, hostconn.HoldsPlainShell)

	clock.Advance(hostconn.ProbeInterval - time.Second)
	if calls := link.probeCalls.Load(); calls != 0 {
		t.Fatalf("%d probes before the first interval elapsed: something other than the cadence and the wake is probing", calls)
	}
}

// TestAWakeOverASilentConnectionReadsStalledWithinFiveSeconds is Q4: the signal
// is followed by five seconds, not fifteen.
func TestAWakeOverASilentConnectionReadsStalledWithinFiveSeconds(t *testing.T) {
	clock := hostconntest.New()
	link, wake, mgr, sup := wakeStand(t, clock)
	acquire(t, mgr, hostconn.HoldsPlainShell)

	wake.Wake()
	waittest.WaitFor(t, "the supervisor to be marked awake", func() bool { return !sup.Suspended() })
	waittest.WaitFor(t, "the wake's probe", func() bool { return link.probeCalls.Load() == 1 })
	clock.Advance(hostconn.WakeProbeDeadline)

	waittest.WaitFor(t, "the connection to read stalled within the wake's five seconds", func() bool {
		return sup.Current().Kind == hostconn.Stalled
	})
	if got := sup.Current().Silent; got != hostconn.WakeProbeDeadline {
		t.Fatalf("stalled reports %v of silence, want %v (the wake's probe)", got, hostconn.WakeProbeDeadline)
	}
}

// TestAParkedProbeIsNotDoubledByAWake — a parked SendRequest holds the mux's
// send lock, so a second probe would queue behind it and measure nothing.
func TestAParkedProbeIsNotDoubledByAWake(t *testing.T) {
	clock := hostconntest.New()
	link, wake, mgr, sup := wakeStand(t, clock)
	acquire(t, mgr, hostconn.HoldsPlainShell)

	// The cadence's probe, parked.
	clock.Advance(hostconn.ProbeInterval)
	waittest.WaitFor(t, "the cadence's probe", func() bool { return link.probeCalls.Load() == 1 })
	clock.Advance(hostconn.ProbeDeadline)
	waittest.WaitFor(t, "the stall", func() bool { return sup.Current().Kind == hostconn.Stalled })

	wake.Wake()
	// The guard is made to go round its loop (the probe ask) so the count is
	// read after the wake was certainly applied.
	syncGuard(t, sup)
	if calls := link.probeCalls.Load(); calls != 1 {
		t.Fatalf("%d probes after a wake with one parked, want 1: a second would queue behind the mux's send lock", calls)
	}

	link.release()
	waittest.WaitFor(t, "the parked probe's answer", func() bool {
		return sup.Current().Kind == hostconn.Connected
	})
}

// TestAHelperOwnedConnectionWithoutAnAnswerAfterAWakeReconnects — thirty
// seconds after the signal, and nothing is lost by closing it.
func TestAHelperOwnedConnectionWithoutAnAnswerAfterAWakeReconnects(t *testing.T) {
	clock := hostconntest.New()
	link, wake, mgr, sup := wakeStand(t, clock)
	lease := acquire(t, mgr, hostconn.HoldsNone)

	wake.Wake()
	waittest.WaitFor(t, "the supervisor to be marked awake", func() bool { return !sup.Suspended() })
	waittest.WaitFor(t, "the wake's probe", func() bool { return link.probeCalls.Load() == 1 })
	clock.Advance(hostconn.WakeProbeDeadline)
	waittest.WaitFor(t, "the stall", func() bool { return sup.Current().Kind == hostconn.Stalled })

	clock.Advance(hostconn.WakeCloseAfter)
	waittest.WaitFor(t, "the connection to be closed after the wake", func() bool {
		return sup.Current().Kind == hostconn.Reconnecting
	})
	if got := sup.Current().Cause; got != hostconn.CauseSilence {
		t.Fatalf("closed after a wake with cause %q, want %q", got, hostconn.CauseSilence)
	}
	if !lease.Lost() {
		t.Fatal("the transport outlived the wake's close")
	}
}

// TestAPlainShellConnectionStaysOpenAfterAWake — the person's terminal is not
// the policy's to close, wake or no wake.
func TestAPlainShellConnectionStaysOpenAfterAWake(t *testing.T) {
	clock := hostconntest.New()
	link, wake, mgr, sup := wakeStand(t, clock)
	lease := acquire(t, mgr, hostconn.HoldsPlainShell)

	wake.Wake()
	waittest.WaitFor(t, "the supervisor to be marked awake", func() bool { return !sup.Suspended() })
	waittest.WaitFor(t, "the wake's probe", func() bool { return link.probeCalls.Load() == 1 })
	clock.Advance(hostconn.WakeProbeDeadline)
	waittest.WaitFor(t, "the stall", func() bool { return sup.Current().Kind == hostconn.Stalled })

	clock.Advance(10 * time.Minute)
	if got := sup.Current().Kind; got != hostconn.Stalled {
		t.Fatalf("a plain-shell connection reads %v ten minutes after a wake, want stalled", got)
	}
	if lease.Lost() {
		t.Fatal("a plain-shell connection was closed after a wake")
	}
	link.release()
	waittest.WaitFor(t, "the connection to answer again", func() bool {
		return sup.Current().Kind == hostconn.Connected
	})
}

// TestAWakeIsFanOutToEveryConnection — the signal is the machine's, and every
// live connection proves itself.
func TestAWakeIsFanOutToEveryConnection(t *testing.T) {
	clock := hostconntest.New()
	links := map[hostconn.Key]*quietLink{}
	var mu sync.Mutex
	carrier := &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) {
		mu.Lock()
		defer mu.Unlock()
		l := newQuietLink()
		links[hostconn.Key{Host: "host" + string(rune('a'+len(links))), Port: 22, User: "dev"}] = l
		return l, nil
	}}
	wake := hostconntest.NewWakeSource(clock)
	_, logger := logtest.New(t)
	mgr := hostconn.NewManager(carrier, logger, hostconn.WithClock(clock), hostconn.WithWakeSource(wake))

	// Two connections under different keys, both alive.
	for i := 0; i < 2; i++ {
		p := plan()
		p.Key = hostconn.Key{Host: "host" + string(rune('a'+i)), Port: 22, User: "dev"}
		if _, err := mgr.Acquire(context.Background(), p); err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
	}
	wake.Wake()
	waittest.WaitFor(t, "every connection to prove itself", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range links {
			if l.probeCalls.Load() != 1 {
				return false
			}
		}
		return len(links) == 2
	})
}

// TestAPreSleepEdgePausesTheProberUntilTheWake — the OS's pre-sleep edge lets
// the supervisor stop asking its connections for answers before the machine
// freezes. Nothing is heard while it is away, so a probe sent into the sleep
// would be the first thing to answer on resume, and the silence it spent parked
// is not evidence of anything.
func TestAPreSleepEdgePausesTheProberUntilTheWake(t *testing.T) {
	clock := hostconntest.New()
	link, wake, mgr, sup := wakeStand(t, clock)
	lease := acquire(t, mgr, hostconn.HoldsNone)

	wake.Sleep()
	waittest.WaitFor(t, "the supervisor to be marked asleep", lease.Suspended)
	clock.Advance(10 * time.Minute)
	if calls := link.probeCalls.Load(); calls != 0 {
		t.Fatalf("%d probes were sent while the machine was going to sleep", calls)
	}
	if got := sup.Current().Kind; got != hostconn.Connected {
		t.Fatalf("a sleeping machine's connection reads %v, want connected: the sleep is not silence", got)
	}

	// The wake ends the pause and the connection proves itself at once.
	wake.Wake()
	waittest.WaitFor(t, "the supervisor to be marked awake", func() bool { return !sup.Suspended() })
	waittest.WaitFor(t, "the wake's probe", func() bool { return link.probeCalls.Load() == 1 })
	clock.Advance(hostconn.WakeProbeDeadline)
	waittest.WaitFor(t, "the stall", func() bool {
		return sup.Current().Kind == hostconn.Stalled
	})
}
