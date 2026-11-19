package hostconn_test

// WHICH LINK AN OBSERVATION BELONGS TO (nocx-oalo1.6.2).
//
// A coordinator cannot derive this from the transitions it is shown: a watcher
// keeps only the newest observation it was handed, so a reconnecting → connected
// pair that happens between two reads arrives as one connected, and "the state
// changed back" is the same fact whether the link survived the stall or was
// replaced under it. The number therefore travels WITH the observation, and what
// it counts is LINKS: it moves when a link is retired and when a new one is
// admitted, and it stands still for every other transition — a stall and its
// recovery are one transport.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/waittest"
)

// linkQueue is a carrier that hands out one fresh quiet link per dial and lets
// the test reach the link it is talking about.
type linkQueue struct {
	mu    sync.Mutex
	links []*quietLink
}

func (q *linkQueue) carrier() *memoryCarrier {
	return &memoryCarrier{dialPlan: func(hostconn.Plan, hostconn.EventSink) (hostconn.Link, error) {
		q.mu.Lock()
		defer q.mu.Unlock()
		l := newQuietLink()
		q.links = append(q.links, l)
		return l, nil
	}}
}

func (q *linkQueue) at(i int) *quietLink {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.links[i]
}

func (q *linkQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.links)
}

// TestTheGenerationMovesWithTheLinkAndNotWithTheState is the pair a coordinator
// tells apart with it: a connection that stalled and recovered is still the
// generation the coordinator already knew, and a connection that was replaced is
// not.
func TestTheGenerationMovesWithTheLinkAndNotWithTheState(t *testing.T) {
	clock := hostconntest.New()
	q := &linkQueue{}
	mgr, sup := managerOn(t, q.carrier(), clock)

	var log factLog
	stop := sup.Watch(log.add)
	defer stop()

	lease := acquire(t, mgr, hostconn.HoldsPlainShell)
	defer lease.Release()
	waittest.WaitFor(t, "the connected observation", func() bool {
		_, ok := log.find(func(f hostconn.Facts) bool { return f.State.Kind == hostconn.Connected })
		return ok
	})
	first, _ := log.find(func(f hostconn.Facts) bool { return f.State.Kind == hostconn.Connected })
	if first.Generation == 0 {
		t.Fatal("the first link carries generation 0: a reader cannot tell a replaced link from a fresh one")
	}

	// THE SAME LINK, SILENT AND THEN ANSWERING. A plain shell keeps the
	// connection open for the silence, and the answered probe is the same
	// transport coming back — so the generation must not move.
	clock.Advance(30 * time.Second)
	waittest.WaitFor(t, "the stalled observation", func() bool {
		return sup.Current().Kind == hostconn.Stalled
	})
	q.at(0).release()
	waittest.WaitFor(t, "the return to connected", func() bool {
		return sup.Current().Kind == hostconn.Connected
	})
	for _, f := range log.all() {
		if f.Generation == 0 {
			// The subscription's first observation is taken before any link
			// exists, and zero says exactly that.
			continue
		}
		if f.Generation != first.Generation {
			t.Fatalf("a stall and its recovery moved the generation (%d, want %d): they are one link", f.Generation, first.Generation)
		}
	}

	// AND A NEW LINK. The transport is closed under its holder — the
	// reconnect's first move — and the next acquire dials afresh.
	if !sup.Disconnect() {
		t.Fatal("Disconnect reported no connection to close")
	}
	waittest.WaitFor(t, "the reconnecting observation", func() bool {
		return sup.Current().Kind == hostconn.Reconnecting
	})
	next := plan()
	next.Holds = hostconn.HoldsPlainShell
	again, err := mgr.Acquire(context.Background(), next)
	if err != nil {
		t.Fatalf("Acquire after the disconnect: %v", err)
	}
	defer again.Release()
	if q.count() != 2 {
		t.Fatalf("%d dial(s), want 2: the disconnect did not make the next acquire dial", q.count())
	}
	defer q.at(1).release()
	waittest.WaitFor(t, "the second connected observation", func() bool {
		_, ok := log.find(func(f hostconn.Facts) bool {
			return f.State.Kind == hostconn.Connected && f.Generation != first.Generation
		})
		return ok
	})
	second, ok := log.find(func(f hostconn.Facts) bool {
		return f.State.Kind == hostconn.Connected && f.Generation != first.Generation
	})
	if !ok {
		t.Fatalf("a new link reported generation %d, the same as the retired one", first.Generation)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("the new link reports generation %d, not after the retired link's %d", second.Generation, first.Generation)
	}
	// Non-decreasing across the whole log: an observation can never describe an
	// older link than one already applied.
	field := log.all()
	for i := 1; i < len(field); i++ {
		if field[i].Generation < field[i-1].Generation {
			t.Fatalf("generation went backwards: %d after %d", field[i].Generation, field[i-1].Generation)
		}
	}
}

// fingerprintedLink is a link that can name the host key it was verified
// against: the capability internal/helper/sshconn's link carries in production.
type fingerprintedLink struct {
	*memoryLink
	fingerprint string
}

func (l *fingerprintedLink) HostKeyFingerprint() string { return l.fingerprint }

// TestTheFingerprintOfTheLiveLinkRidesTheFact is the identity a coordinator
// that restarted rebuilds its entries from: the host's mode and the person's
// consent are keyed by the key the connection was verified against, and the
// supervisor's link is the only place that key exists.
func TestTheFingerprintOfTheLiveLinkRidesTheFact(t *testing.T) {
	const want = "SHA256:abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	carrier := &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) {
		return &fingerprintedLink{memoryLink: newMemoryLink(), fingerprint: want}, nil
	}}
	mgr, sup := manager(t, carrier)

	if before := sup.Facts(); before.HostKeyFingerprint != "" {
		t.Fatalf("a connection with no link reports the fingerprint %q", before.HostKeyFingerprint)
	}

	lease := acquire(t, mgr, hostconn.HoldsNone)
	waittest.WaitFor(t, "the connected observation", func() bool {
		return sup.Facts().State.Kind == hostconn.Connected
	})
	if got := sup.Facts().HostKeyFingerprint; got != want {
		t.Fatalf("the live link reports the fingerprint %q, want %q", got, want)
	}

	// AND WITH THE LINK, NOT AFTER IT: a supervisor that no longer holds a
	// connection must not go on vouching for the key of the transport it lost.
	lease.Release()
	waittest.WaitFor(t, "the connection's end", func() bool {
		return sup.Current().Kind == hostconn.Ended
	})
	if got := sup.Facts().HostKeyFingerprint; got != "" {
		t.Fatalf("a connection with no link still reports the fingerprint %q", got)
	}
}

// TestTheConnectionTableIsTheSupervisorsAndItAnnouncesNewOnes is what the
// `connections` op is answered from: every connection the helper holds,
// including one nobody has dialled yet, and an observation for each of them —
// the existing ones before the subscription returns, the later ones as they
// appear.
func TestTheConnectionTableIsTheSupervisorsAndItAnnouncesNewOnes(t *testing.T) {
	carrier := &memoryCarrier{}
	mgr, first := manager(t, carrier)
	secondKey := hostconn.Key{Host: "other.example", Port: 22, User: "dev", Identity: "key"}
	_ = mgr.Supervisor(secondKey)

	var mu sync.Mutex
	seen := map[string][]hostconn.Facts{}
	stop := mgr.WatchConnections(func(f hostconn.Facts) {
		mu.Lock()
		seen[f.Key.ID()] = append(seen[f.Key.ID()], f)
		mu.Unlock()
	})

	// THE BARRIER: every existing connection's first observation is delivered
	// before WatchConnections returns, which is what lets the op that installs
	// it answer its list from the same facts.
	mu.Lock()
	for _, key := range []hostconn.Key{plan().Key, secondKey} {
		if len(seen[key.ID()]) == 0 {
			mu.Unlock()
			t.Fatalf("connection %s was not delivered before the subscription returned", key.ID())
		}
	}
	mu.Unlock()

	// A connection that appears LATER is announced too.
	thirdKey := hostconn.Key{Host: "third.example", Port: 22, User: "dev", Identity: "key"}
	mgr.Supervisor(thirdKey)
	mu.Lock()
	_, announced := seen[thirdKey.ID()]
	mu.Unlock()
	if !announced {
		t.Fatal("a connection created after the subscription was never announced")
	}

	table := mgr.Supervisors()
	if len(table) != 3 {
		t.Fatalf("the connection table holds %d connection(s), want 3", len(table))
	}
	for i := 1; i < len(table); i++ {
		if table[i-1].Facts().Key.ID() >= table[i].Facts().Key.ID() {
			t.Fatalf("the table's order is not stable: %s before %s", table[i-1].Facts().Key.ID(), table[i].Facts().Key.ID())
		}
	}

	// And the subscription ends: nothing reaches a stopped reader.
	stop()
	mu.Lock()
	count := len(seen[plan().Key.ID()])
	mu.Unlock()
	lease := acquire(t, mgr, hostconn.HoldsNone)
	defer lease.Release()
	waittest.WaitFor(t, "the connection to reach connected", func() bool {
		return first.Current().Kind == hostconn.Connected
	})
	mu.Lock()
	defer mu.Unlock()
	if got := len(seen[plan().Key.ID()]); got != count {
		t.Fatalf("a stopped subscription received %d more observation(s)", got-count)
	}
}
