package client

// The downlink's own bookkeeping, tested against a send spy: the
// spawn-answer race (an accepted completion can predate the helper session's
// identity) and the exactly-once rule are THIS type's behaviour, and no wire
// is needed to judge them.
//
// WHAT PRODUCTION CAN REACH, stated because the review asked. The pre-bind
// window these buffering tests exercise — a completion accepted before the
// bind names the session — is one production CANNOT CURRENTLY REACH: on a
// fresh open the bind runs inside hostedSpawn.run the moment the spawn
// answers, while the bridge that can carry an Observe starts only after the
// open returns (the transport's StartLifecycle); on a re-adoption the
// identity is known before the downlink is built, so it binds at
// construction. The tests stay because the buffer is the carrier's own
// contract — bounded, ordered, exactly-once — and the wedge-detector for the
// window the type was built for; making production reach the window would
// mean bridging a lifecycle channel for a session that may never exist,
// which reorders the open's rollback for a test's benefit.

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

type spySend struct {
	mu       sync.Mutex
	sessions []proto.HostSessionID
	fence    [][32]byte
	exits    []*int
	err      error
	// started closes the first time a send begins, and block parks every
	// send until closed: together they pin a delivery in flight without
	// depending on a clock.
	started     chan struct{}
	startedOnce sync.Once
	block       chan struct{}
}

func (s *spySend) send(_ context.Context, params proto.LifecycleCompleteParams) error {
	if s.started != nil {
		s.startedOnce.Do(func() { close(s.started) })
	}
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, params.Session)
	var fence [32]byte
	raw, err := hex.DecodeString(params.Nonce)
	if err != nil || len(raw) != 32 {
		panic("spy: a downlink sent a nonce that is not 64 hex characters: " + params.Nonce)
	}
	copy(fence[:], raw)
	s.fence = append(s.fence, fence)
	s.exits = append(s.exits, params.ExitCode)
	return s.err
}

func newTestDownlink(spy *spySend, reported *[]error) *CompletionDownlink {
	var mu sync.Mutex
	return &CompletionDownlink{
		ctx: context.Background(),
		send: func(ctx context.Context, params proto.LifecycleCompleteParams) error {
			return spy.send(ctx, params)
		},
		report: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			*reported = append(*reported, err)
		},
	}
}

// TestCompletionsAcceptedBeforeBindWaitForTheBindThenDeliverInOrder is the
// spawn-answer race: the adapter exists before the spawn RPC answers, so a
// completion the kernel accepts in that window arrives with no addressee.
// It is buffered, and the bind delivers the buffered completions in the
// order the kernel accepted them — exactly once, never re-delivered by a
// second bind.
func TestCompletionsAcceptedBeforeBindWaitForTheBindThenDeliverInOrder(t *testing.T) {
	spy := &spySend{}
	var reported []error
	dl := newTestDownlink(spy, &reported)

	first, second := [32]byte{1}, [32]byte{2}
	dl.Observe(first, nil)
	dl.Observe(second, nil)
	if got := len(spy.sessions); got != 0 {
		t.Fatalf("%d completions sent before the bind named a session, want 0 buffered until then", got)
	}

	entry := HostSessionID{Generation: "gen-under-test", Session: "0123456789abcdef0123456789abcdef"}
	dl.Bind(entry)
	if len(spy.sessions) != 2 {
		t.Fatalf("%d completions delivered by the bind, want the 2 that were buffered", len(spy.sessions))
	}
	for i, got := range spy.sessions {
		if got.Generation != "gen-under-test" || got.Session != entry.Session {
			t.Fatalf("buffered completion %d delivered to %+v, want the bound session", i, got)
		}
	}
	if spy.fence[0] != first || spy.fence[1] != second {
		t.Fatalf("buffered completions delivered out of acceptance order: %v", spy.fence)
	}

	// A second Bind is the open path's idempotence: nothing re-delivered.
	dl.Bind(entry)
	if len(spy.sessions) != 2 {
		t.Fatalf("a second bind re-delivered; %d completions sent, want 2", len(spy.sessions))
	}
	if len(reported) != 0 {
		t.Fatalf("the happy path reported %v, want nothing", reported)
	}
}

// TestAnUnboundDownlinkBuffersOnlyWhatIsBounded keeps the pre-bind buffer
// from becoming the helper's unbounded memory: a completion that arrives
// past the bound is dropped and REPORTED, because a kernel-accepted
// completion disappearing silently is the exact degrade this carrier exists
// to refuse.
func TestAnUnboundDownlinkBuffersOnlyWhatIsBounded(t *testing.T) {
	spy := &spySend{}
	var reported []error
	dl := newTestDownlink(spy, &reported)

	for i := 0; i < maxPendingCompletions; i++ {
		dl.Observe([32]byte{byte(i)}, nil)
	}
	if len(reported) != 0 {
		t.Fatalf("the bound reported %v before the bound was exceeded", reported)
	}
	dl.Observe([32]byte{0xFF}, nil)
	if len(reported) != 1 {
		t.Fatalf("the completion past the bound was dropped without a report: %v", reported)
	}
	dl.Bind(HostSessionID{Session: "0123456789abcdef0123456789abcdef"})
	if len(spy.sessions) != maxPendingCompletions {
		t.Fatalf("%d buffered completions delivered, want %d", len(spy.sessions), maxPendingCompletions)
	}
}

// TestAFailedDeliveryIsReported is the failure half of the criterion pair:
// a send the helper refuses is handed to the report seam, which is the
// whole of the failure's handling — nothing here reaches back into the
// kernel, whose execution state is exactly what it set on acceptance.
func TestAFailedDeliveryIsReported(t *testing.T) {
	spy := &spySend{err: errors.New("no_such_session")}
	var reported []error
	dl := newTestDownlink(spy, &reported)
	dl.Bind(HostSessionID{Session: "0123456789abcdef0123456789abcdef"})

	dl.Observe([32]byte{9}, nil)
	if len(reported) != 1 || reported[0] == nil {
		t.Fatalf("a failed delivery reported %v, want the send's error", reported)
	}
}

// TestTheBindDrainHoldsTheDispatch pins the ordering guarantee the exact way
// the old defect failed it: while the bind's backlog delivery is parked in
// flight, the dispatch MUST be held — an Observe arriving in that window can
// then only queue behind the backlog, never deliver ahead of it. TryLock is
// the deterministic probe: under a drain that releases the dispatch, it
// succeeds, and the scheduling-dependent race this test replaces passed
// straight through it.
func TestTheBindDrainHoldsTheDispatch(t *testing.T) {
	spy := &spySend{started: make(chan struct{}), block: make(chan struct{})}
	var reported []error
	dl := newTestDownlink(spy, &reported)

	buffered := [32]byte{1}
	dl.Observe(buffered, nil)

	entry := HostSessionID{Generation: "gen-under-test", Session: "0123456789abcdef0123456789abcdef"}
	bindDone := make(chan struct{})
	go func() {
		dl.Bind(entry)
		close(bindDone)
	}()
	<-spy.started // the backlog delivery is in flight: the dispatch is held

	if dl.mu.TryLock() {
		dl.mu.Unlock()
		close(spy.block)
		<-bindDone
		t.Fatal("the bind's drain is not holding the dispatch: an observation arriving now could deliver ahead of the backlog")
	}
	close(spy.block)
	<-bindDone
	if len(spy.fence) != 1 || spy.fence[0] != buffered {
		t.Fatalf("the drained backlog delivered %v, want the buffered fence exactly once", spy.fence)
	}
}

// TestTheDownlinkDeliversUnderItsOwnContext pins the delivery SEAM the
// lifetime fix hangs the downlink off: deliver runs under the context the
// downlink was built with — the hosted session's lifetime, per the
// composition — and not under a fresh or detached one. The session's end
// cancels exactly that context, and the dispatch-time stop acts on it, so a
// deliver that swapped it away would make the stop unreachable from the
// wire: the composition cancels a context nobody reads. Over a real
// transport a swapped context is unobservable until the stop misfires, so
// the pin is here, where the send is a spy.
//
// The send's context is a CHILD of the downlink's — every delivery is
// bounded by its own deadline (completionDeliveryTimeout) — so what is
// asserted is the property that matters and that identity was standing in
// for: the composition's cancellation reaches the context the send is
// holding. A deliver that built a fresh or detached context fails here, and
// no clock is involved: cancel propagates to children before it returns.
func TestTheDownlinkDeliversUnderItsOwnContext(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// The context is inspected while the send still HOLDS it: a delivery's
	// context ends with the delivery, so one read after the call has
	// returned says nothing about what the send was given.
	seen := make(chan context.Context, 1)
	release := make(chan struct{})
	dl := &CompletionDownlink{
		ctx: ctx,
		send: func(ctx context.Context, _ proto.LifecycleCompleteParams) error {
			seen <- ctx
			<-release
			return nil
		},
		report: nil,
	}
	dl.Bind(HostSessionID{Generation: "gen-under-test", Session: "0123456789abcdef0123456789abcdef"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		dl.Observe([32]byte{1}, nil)
	}()

	select {
	case got := <-seen:
		if got.Err() != nil {
			t.Fatalf("the send's context was already done on arrival: %v", got.Err())
		}
		if _, ok := got.Deadline(); !ok {
			t.Fatal("the send's context carries no deadline: a helper that never answers would park the ingest goroutine")
		}
		// THE SESSION ENDS. The context the send is holding must end with it.
		stop()
		if got.Err() == nil {
			t.Fatal("deliver ran under a context the downlink's own cancellation does not reach: the composition would cancel a context nobody reads")
		}
		close(release)
		<-done
	case <-time.After(5 * time.Second):
		t.Fatal("deliver never reached the send")
	}
}
