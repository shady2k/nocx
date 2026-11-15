package hostconn_test

// What a supervisor tells a reader (design §5.3, §5.6, stage 3).
//
// A pane is drawn from these facts, so they are asserted as the two things a
// person meets: the path a healthy connection takes, and the numbers a connection
// that stopped answering carries. Time is the injected clock's throughout, so
// "stalled after thirty seconds" is thirty simulated seconds and not a sleep.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/waittest"
)

// factLog collects a supervisor's observations without blocking it.
type factLog struct {
	mu    sync.Mutex
	facts []hostconn.Facts
}

func (l *factLog) add(f hostconn.Facts) {
	l.mu.Lock()
	l.facts = append(l.facts, f)
	l.mu.Unlock()
}

func (l *factLog) all() []hostconn.Facts {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]hostconn.Facts(nil), l.facts...)
}

func (l *factLog) find(match func(hostconn.Facts) bool) (hostconn.Facts, bool) {
	for _, f := range l.all() {
		if match(f) {
			return f, true
		}
	}
	return hostconn.Facts{}, false
}

// TestAWatchSeesThePathAndTheStateItIsIn is the healthy open: the first facts a
// pane receives are opening stages, then connected — and never anything else.
func TestAWatchSeesThePathAndTheStateItIsIn(t *testing.T) {
	clock := hostconntest.New()
	mgr, sup := managerOn(t, &memoryCarrier{}, clock)

	var log factLog
	stop := sup.Watch(log.add)
	defer stop()

	lease := acquire(t, mgr, hostconn.HoldsPlainShell)
	defer lease.Release()

	waittest.WaitFor(t, "a connected observation", func() bool {
		_, ok := log.find(func(f hostconn.Facts) bool { return f.State.Kind == hostconn.Connected })
		return ok
	})

	facts := log.all()
	if facts[0].State.Kind != hostconn.Opening {
		t.Fatalf("the first fact is %v, want an opening: a pane must be told a stage before anything else", facts[0].State)
	}
	if facts[0].State.Stage != hostconn.StageResolve {
		t.Fatalf("the first opening stage is %q, want resolve", facts[0].State.Stage)
	}
	var connected hostconn.Facts
	for i, f := range facts {
		if f.Key != plan().Key {
			t.Fatalf("fact %d names %v, want %v", i, f.Key, plan().Key)
		}
		if i > 0 && f.Seq <= facts[i-1].Seq {
			t.Fatalf("fact %d has seq %d after %d: an observation must be orderable", i, f.Seq, facts[i-1].Seq)
		}
		if f.State.Kind == hostconn.Connected {
			connected = f
		}
		if f.State.Kind != hostconn.Opening && f.State.Kind != hostconn.Connected {
			t.Fatalf("a healthy open reported %v", f.State)
		}
	}
	if connected.PlainShells != 1 {
		t.Fatalf("connected says plainShells=%d, want 1: closing it would end this person's shell", connected.PlainShells)
	}
}

// TestAStalledFactCarriesTheLookAndReturnsOnTheAnswer is Q3's other half: the
// silence is reported with how long it has lasted, a look-only dial that the
// host answers is named as evidence, and the parked probe's answer is connected
// again.
func TestAStalledFactCarriesTheLookAndReturnsOnTheAnswer(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	mgr, sup := managerOn(t, quietCarrier(link), clock)

	var log factLog
	stop := sup.Watch(log.add)
	defer stop()

	lease := acquire(t, mgr, hostconn.HoldsPlainShell)
	clock.Advance(30 * time.Second)
	waittest.WaitForDetail(t, "the stalled observation with a look answered",
		func() string { return fmt.Sprintf("facts: %+v", log.all()) },
		func() bool {
			_, ok := log.find(func(f hostconn.Facts) bool { return f.State.Kind == hostconn.Stalled && f.LookAnswered })
			return ok
		})
	stalled, _ := log.find(func(f hostconn.Facts) bool { return f.State.Kind == hostconn.Stalled && f.LookAnswered })
	if stalled.State.Silent != 15*time.Second {
		t.Fatalf("stalled says silent=%v, want 15s", stalled.State.Silent)
	}
	if stalled.LookAnsweredFor < 0 {
		t.Fatalf("lookAnsweredFor is negative: %v", stalled.LookAnsweredFor)
	}
	if stalled.PlainShells != 1 {
		t.Fatalf("stalled says plainShells=%d, want 1", stalled.PlainShells)
	}

	// The parked probe is answered: the connection is whole again.
	link.release()
	waittest.WaitFor(t, "the return to connected", func() bool {
		f, ok := log.find(func(f hostconn.Facts) bool { return f.State.Kind == hostconn.Connected })
		return ok && f.State.Silent == 0
	})
	lease.Release()
}

// TestASlowAnswerCarriesItsGrade: the measurement rides the fact, and the grade
// is the one function that decides it.
func TestASlowAnswerCarriesItsGrade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		link := newQuietLink()
		link.memoryLink.probe = func() (time.Duration, error) { return 600 * time.Millisecond, nil }
		mgr, sup := managerOn(t, quietCarrier(link), clock)

		var log factLog
		stop := sup.Watch(log.add)
		defer stop()

		lease := acquire(t, mgr, hostconn.HoldsNone)
		defer lease.Release()
		synctest.Wait()
		clock.Advance(hostconn.ProbeInterval)
		synctest.Wait()
		waittest.WaitFor(t, "a measured observation", func() bool {
			f, ok := log.find(func(f hostconn.Facts) bool { return f.Measured })
			return ok && f.Slow && f.RoundTrip == 600*time.Millisecond
		})
	})
}

// TestDisconnectClosesTheLiveTransportSoTheNextAcquireDials is the reconnect's
// first move (design §5.3): a stalled/held connection is closed, and the next
// acquire gets a fresh dial rather than the dead one.
func TestDisconnectClosesTheLiveTransportSoTheNextAcquireDials(t *testing.T) {
	clock := hostconntest.New()
	link := newQuietLink()
	link.memoryLink.probe = func() (time.Duration, error) { return time.Millisecond, nil }
	carrier := &memoryCarrier{dialPlan: func(hostconn.Plan, hostconn.EventSink) (hostconn.Link, error) {
		return link, nil
	}}
	mgr, sup := managerOn(t, carrier, clock)
	lease := acquire(t, mgr, hostconn.HoldsPlainShell)

	if !sup.Disconnect() {
		t.Fatal("Disconnect reported no connection to close")
	}
	waittest.WaitFor(t, "the fenced state", func() bool {
		return sup.Current().Kind == hostconn.Reconnecting
	})
	if !lease.Lost() {
		t.Fatal("the lease survives a disconnect: the pane would keep a dead transport")
	}
	if sup.Disconnect() {
		t.Fatal("a second Disconnect closed something: it must be idempotent")
	}
	// A new acquire dials again rather than joining the closed one.
	again, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire after a disconnect: %v", err)
	}
	again.Release()
	if carrier.dialCount() != 2 {
		t.Fatalf("%d dial(s), want 2: the closed connection was reused", carrier.dialCount())
	}
}

// TestAStoppedWatchStopsCalling keeps the subscription's end honest: a pane that
// closed must not be called by the connection it left.
func TestAStoppedWatchStopsCalling(t *testing.T) {
	clock := hostconntest.New()
	mgr, sup := managerOn(t, &memoryCarrier{}, clock)

	var mu sync.Mutex
	calls := 0
	stop := sup.Watch(func(hostconn.Facts) {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	waittest.WaitFor(t, "the subscription's first call", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls > 0
	})
	stop()
	mu.Lock()
	after := calls
	mu.Unlock()
	lease := acquire(t, mgr, hostconn.HoldsNone)
	_ = lease
	// Nothing in the acquire is allowed to reach a stopped reader. The
	// assertion is the absence of a call, so it is read after the fact the
	// subscription would have carried is observable on the supervisor.
	waittest.WaitFor(t, "the connection to reach connected", func() bool {
		return sup.Current().Kind == hostconn.Connected
	})
	mu.Lock()
	defer mu.Unlock()
	if calls != after {
		t.Fatalf("a stopped watch was called %d more time(s)", calls-after)
	}
}
