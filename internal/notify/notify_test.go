package notify_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/notify"
)

// Test kinds live here so table rows do not depend on the package's own
// kind constants, except where a test deliberately uses them.
const (
	kindA notify.Kind = "test.a"
	kindB notify.Kind = "test.b"
	kindC notify.Kind = "test.c"
	kindD notify.Kind = "test.d"
)

func testLimits() notify.Limits {
	return notify.Limits{
		MaxInFlight:     8,
		MaxQueued:       8,
		MaxRetained:     1 << 20,
		DeliveryTimeout: time.Second,
	}
}

func eventFor(kind notify.Kind, trust notify.Trust) notify.Event {
	return notify.Event{
		SessionID: "s1",
		Title:     "title",
		Body:      "body",
		Kind:      kind,
		Trust:     trust,
	}
}

func event(kind notify.Kind) notify.Event {
	return eventFor(kind, notify.TrustProgramRequest)
}

// recordingSink records every delivery. notified receives one buffered
// signal per call so tests can wait on the first delivery.
type recordingSink struct {
	mu       sync.Mutex
	calls    []notify.Delivery
	leaves   bool
	notified chan struct{}
}

func (s *recordingSink) Deliver(ctx context.Context, d notify.Delivery) error {
	s.mu.Lock()
	s.calls = append(s.calls, d)
	s.mu.Unlock()
	select {
	case s.notified <- struct{}{}:
	default:
	}
	return nil
}

func (s *recordingSink) LeavesMachine() bool { return s.leaves }

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *recordingSink) received() []notify.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notify.Delivery(nil), s.calls...)
}

// gateSink holds an invocation open until the test releases it, even after
// the delivery deadline — the "a deadline is not proof a goroutine stopped
// writing" shape the ADR names. cancelled is closed when ctx fires; release
// unblocks Deliver.
type gateSink struct {
	called    chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (s *gateSink) Deliver(ctx context.Context, d notify.Delivery) error {
	select {
	case s.called <- struct{}{}:
	default:
	}
	<-ctx.Done()
	close(s.cancelled)
	<-s.release
	return ctx.Err()
}

func (s *gateSink) LeavesMachine() bool { return false }

// lateSink violates the no-callback-after-return contract on purpose: after
// Deliver returns it fires a background signal. The router must ignore it.
type lateSink struct {
	fired chan struct{}
	once  sync.Once
}

func (s *lateSink) Deliver(ctx context.Context, d notify.Delivery) error {
	go s.once.Do(func() {
		time.Sleep(20 * time.Millisecond)
		close(s.fired)
	})
	return nil
}

func (s *lateSink) LeavesMachine() bool { return false }

// panickingSink panics inside Deliver.
type panickingSink struct{}

func (panickingSink) Deliver(ctx context.Context, d notify.Delivery) error {
	panic("sink exploded")
}

func (panickingSink) LeavesMachine() bool { return false }

// TestRaise_DefaultDeny_EmptyTable: a (kind, trust) pair with no row in an
// empty table reaches no sink and is not an error.
func TestRaise_DefaultDeny_EmptyTable(t *testing.T) {
	sink := &recordingSink{notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{}, testLimits())
	if err != nil {
		t.Fatal(err)
	}

	out := r.Raise(context.Background(), event(notify.KindProgramNotify))
	if len(out.Resolved) != 0 {
		t.Fatalf("resolved %d routes, want 0", len(out.Resolved))
	}
	if out.Err != nil {
		t.Fatalf("default-deny is not a failure, got %v", out.Err)
	}
	if sink.count() != 0 {
		t.Fatalf("sink reached %d times, want 0", sink.count())
	}
}

// TestRaise_DefaultDeny_KindNotInTable: a kind added to the enum later but
// absent from the table reaches no sink, as does an existing kind whose
// pair has no row.
func TestRaise_DefaultDeny_KindNotInTable(t *testing.T) {
	sink := &recordingSink{notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest}: {
			{Sink: sink, Destination: notify.Destination{Target: "toast"}},
		},
	}, testLimits())
	if err != nil {
		t.Fatal(err)
	}

	const futureKind notify.Kind = "future.kind"
	for _, tc := range []struct {
		name  string
		event notify.Event
	}{
		{"kind added later, not in table", event(futureKind)},
		{"existing kind, no row for the pair", event(notify.KindBell)},
	} {
		out := r.Raise(context.Background(), tc.event)
		if len(out.Resolved) != 0 {
			t.Fatalf("%s: resolved %d routes, want 0", tc.name, len(out.Resolved))
		}
		if sink.count() != 0 {
			t.Fatalf("%s: sink reached %d times, want 0", tc.name, sink.count())
		}
	}
}

// TestRouter_RejectsHeuristicNetworkRow: a table row granting a network
// sink to the heuristic trust class can never fire; the router refuses to
// build it rather than silently resolving it to nothing forever.
func TestRouter_RejectsHeuristicNetworkRow(t *testing.T) {
	net := &recordingSink{leaves: true, notified: make(chan struct{}, 1)}
	_, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: notify.KindPaneWorkFinished, Trust: notify.TrustHeuristic}: {
			{Sink: net, Destination: notify.Destination{Target: "push"}},
		},
	}, testLimits())
	if err == nil {
		t.Fatal("NewRouter accepted a heuristic row granting a network sink")
	}
	if !errors.Is(err, notify.ErrTrustCapability) {
		t.Fatalf("want ErrTrustCapability, got %v", err)
	}
}

// TestHeuristic_ReachesLocalOnly: through a valid router a heuristic event
// reaches its local row and never the network sink the attested row for the
// same kind grants.
func TestHeuristic_ReachesLocalOnly(t *testing.T) {
	local := &recordingSink{notified: make(chan struct{}, 1)}
	net := &recordingSink{leaves: true, notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: notify.KindPaneWorkFinished, Trust: notify.TrustHeuristic}: {
			{Sink: local, Destination: notify.Destination{Target: "toast"}},
		},
		notify.Key{Kind: notify.KindPaneWorkFinished, Trust: notify.TrustAttested}: {
			{Sink: net, Destination: notify.Destination{Target: "push"}},
		},
	}, testLimits())
	if err != nil {
		t.Fatal(err)
	}

	// The caller stamps At now: ingress is the first nocx-owned stage and the
	// router no longer touches it (ingress.go). What is asserted below is
	// therefore carry-through, not stamping.
	ev := eventFor(notify.KindPaneWorkFinished, notify.TrustHeuristic)
	ev.At = time.Now()
	out := r.Raise(context.Background(), ev)
	if len(out.Resolved) != 1 {
		t.Fatalf("resolved %d routes, want 1", len(out.Resolved))
	}
	if local.count() != 1 {
		t.Fatalf("heuristic event did not reach its local sink")
	}
	if net.count() != 0 {
		t.Fatalf("heuristic event reached a network sink")
	}
	got := local.received()[0]
	if got.Destination.Target != "toast" {
		t.Fatalf("sink received destination %q, want the resolved one", got.Destination.Target)
	}
	if got.Event.SessionID != "s1" {
		t.Fatalf("sink received session %q, want s1", got.Event.SessionID)
	}
	if !got.Event.At.Equal(ev.At) {
		t.Fatalf("router delivered At %v, want the caller's %v carried through unchanged", got.Event.At, ev.At)
	}
}

// TestSubscriptionRoute_AttestedOnly: the same table governs the ad-hoc
// completion-subscription route; only an attested event may match it
// (ADR-0047 §3). programRequest and heuristic never do, even where their
// raise route has a row.
func TestSubscriptionRoute_AttestedOnly(t *testing.T) {
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: notify.KindBlockFinished, Trust: notify.TrustAttested}: {
			{Sink: &recordingSink{notified: make(chan struct{}, 1)}, Destination: notify.Destination{Target: "toast"}},
		},
		notify.Key{Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest}: {
			{Sink: &recordingSink{notified: make(chan struct{}, 1)}, Destination: notify.Destination{Target: "toast"}},
		},
	}, testLimits())
	if err != nil {
		t.Fatal(err)
	}

	if got := r.Resolve(notify.KindBlockFinished, notify.TrustAttested, notify.RouteSubscription); len(got) != 1 {
		t.Fatalf("attested subscription resolved %d routes, want 1", len(got))
	}
	for _, trust := range []notify.Trust{notify.TrustProgramRequest, notify.TrustHeuristic} {
		if got := r.Resolve(notify.KindBlockFinished, trust, notify.RouteSubscription); len(got) != 0 {
			t.Fatalf("trust %q matched a completion subscription", trust)
		}
		if got := r.Resolve(notify.KindBlockFinished, trust, notify.RouteRaise); len(got) != 0 {
			t.Fatalf("trust %q resolved a raise route with no row", trust)
		}
	}
	if got := r.Resolve(notify.KindProgramNotify, notify.TrustProgramRequest, notify.RouteRaise); len(got) != 1 {
		t.Fatalf("programRequest raise route resolved %d, want 1", len(got))
	}
	if got := r.Resolve(notify.KindProgramNotify, notify.TrustProgramRequest, notify.RouteSubscription); len(got) != 0 {
		t.Fatalf("programRequest matched a completion subscription")
	}
}

// TestRelease_OnReturn_NotOnExpiry: the in-flight slot is released when the
// invocation RETURNS, never at deadline expiry — a sink that observes
// cancellation but has not returned yet still holds the slot.
func TestRelease_OnReturn_NotOnExpiry(t *testing.T) {
	limits := notify.Limits{
		MaxInFlight:     1,
		MaxQueued:       0,
		MaxRetained:     0,
		DeliveryTimeout: 100 * time.Millisecond,
	}
	gate := &gateSink{
		called:    make(chan struct{}, 1),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
	}
	rec := &recordingSink{notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: gate, Destination: notify.Destination{Target: "gate"}},
		},
		notify.Key{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: rec, Destination: notify.Destination{Target: "rec"}},
		},
	}, limits)
	if err != nil {
		t.Fatal(err)
	}

	aDone := make(chan notify.Outcome, 1)
	go func() { aDone <- r.Raise(context.Background(), event(kindA)) }()
	<-gate.called

	// In flight: the only slot is held, so nothing else is admitted.
	if out := r.Raise(context.Background(), event(kindB)); out.Err == nil {
		t.Fatal("raise admitted while the slot was in flight")
	}

	// The delivery deadline expires; the sink observes cancellation but has
	// not returned.
	<-gate.cancelled
	if out := r.Raise(context.Background(), event(kindB)); out.Err == nil {
		t.Fatal("slot released at deadline expiry, before the invocation returned")
	}

	// The invocation returns; the slot is released.
	close(gate.release)
	out := <-aDone
	if out.Err != nil {
		t.Fatalf("first raise failed: %v", out.Err)
	}
	if len(out.Results) != 1 {
		t.Fatalf("first raise produced %d results, want 1", len(out.Results))
	}
	if !errors.Is(out.Results[0].Err, context.DeadlineExceeded) {
		t.Fatalf("first raise result err = %v, want DeadlineExceeded (the timeout is a logical result)", out.Results[0].Err)
	}

	if out := r.Raise(context.Background(), event(kindB)); out.Err != nil {
		t.Fatalf("slot not released after the invocation returned: %v", out.Err)
	}
}

// TestLateCallback_Ignored_NoDoubleFinalize: a sink that fires a callback
// after returning is ignored — the instance is finalised once, so the late
// signal frees no phantom slot while another event holds it.
func TestLateCallback_Ignored_NoDoubleFinalize(t *testing.T) {
	limits := notify.Limits{
		MaxInFlight:     1,
		MaxQueued:       0,
		MaxRetained:     0,
		DeliveryTimeout: time.Second,
	}
	late := &lateSink{fired: make(chan struct{})}
	gate := &gateSink{
		called:    make(chan struct{}, 1),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
	}
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: late, Destination: notify.Destination{Target: "late"}},
		},
		notify.Key{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: gate, Destination: notify.Destination{Target: "gate"}},
		},
	}, limits)
	if err != nil {
		t.Fatal(err)
	}

	// The first event's sink returns immediately and fires a callback after.
	out := r.Raise(context.Background(), event(kindA))
	if out.Err != nil || len(out.Results) != 1 || out.Results[0].Err != nil {
		t.Fatalf("first raise: %+v", out)
	}

	// The second event holds the only slot.
	bDone := make(chan notify.Outcome, 1)
	go func() { bDone <- r.Raise(context.Background(), event(kindB)) }()
	<-gate.called

	// The first event's late callback fires while the slot is held. If the
	// instance had been finalised twice, a phantom slot would now admit
	// work that must be refused.
	<-late.fired
	if out := r.Raise(context.Background(), event(kindA)); out.Err == nil {
		t.Fatal("raise admitted after a late callback: the instance was finalised twice")
	}

	close(gate.release)
	if out := <-bDone; out.Err != nil {
		t.Fatalf("second raise failed: %v", out.Err)
	}
}

// TestLimits_QueuedInstance_Bounded: with one slot busy and two queue
// places, three further events race for the queue: two are admitted and
// wait, one is a failed delivery — the queue is bounded, never unbounded —
// and no queued or refused event ever runs concurrently with the in-flight
// one. Which of the three is refused is the racers' business; the set of
// outcomes is fixed.
func TestLimits_QueuedInstance_Bounded(t *testing.T) {
	limits := notify.Limits{
		MaxInFlight:     1,
		MaxQueued:       2,
		MaxRetained:     1 << 20,
		DeliveryTimeout: time.Second,
	}
	gate := &unlockSink{
		called:  make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	b := &recordingSink{notified: make(chan struct{}, 1)}
	c := &recordingSink{notified: make(chan struct{}, 1)}
	d := &recordingSink{notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: gate, Destination: notify.Destination{Target: "gate"}},
		},
		notify.Key{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: b, Destination: notify.Destination{Target: "b"}},
		},
		notify.Key{Kind: kindC, Trust: notify.TrustProgramRequest}: {
			{Sink: c, Destination: notify.Destination{Target: "c"}},
		},
		notify.Key{Kind: kindD, Trust: notify.TrustProgramRequest}: {
			{Sink: d, Destination: notify.Destination{Target: "d"}},
		},
	}, limits)
	if err != nil {
		t.Fatal(err)
	}

	aDone := make(chan notify.Outcome, 1)
	go func() { aDone <- r.Raise(context.Background(), event(kindA)) }()
	<-gate.called

	bDone := make(chan notify.Outcome, 1)
	cDone := make(chan notify.Outcome, 1)
	dDone := make(chan notify.Outcome, 1)
	go func() { bDone <- r.Raise(context.Background(), event(kindB)) }()
	go func() { cDone <- r.Raise(context.Background(), event(kindC)) }()
	go func() { dDone <- r.Raise(context.Background(), event(kindD)) }()

	// While the slot is held, nobody else can deliver: delivery requires
	// the slot, and the gate holds it until the test says otherwise.
	if b.count()+c.count()+d.count() != 0 {
		t.Fatal("event delivered while the in-flight slot was held")
	}

	// Admission is settled the moment the third racer's Raise returns:
	// the first two found queue places, the last found the queue full.
	// That refusal is the observable that all three have passed admission,
	// so the test releases the slot then — never sleeping out the delivery
	// deadline (AGENTS.md: a test may not depend on timing).
	var refusedOut notify.Outcome
	select {
	case refusedOut = <-bDone:
	case refusedOut = <-cDone:
	case refusedOut = <-dDone:
	}
	var rej *notify.RefusedError
	if !errors.As(refusedOut.Err, &rej) {
		t.Fatalf("want RefusedError, got %v", refusedOut.Err)
	}
	if rej.Limit != notify.LimitQueued {
		t.Fatalf("want limit %q, got %q", notify.LimitQueued, rej.Limit)
	}

	close(gate.release)
	if out := <-aDone; out.Err != nil {
		t.Fatalf("first raise failed: %v", out.Err)
	}
	// The two queued events deliver once the slot is free, in either order.
	rest := make([]notify.Outcome, 0, 2)
	select {
	case o := <-bDone:
		rest = append(rest, o)
	case o := <-cDone:
		rest = append(rest, o)
	case o := <-dDone:
		rest = append(rest, o)
	}
	select {
	case o := <-bDone:
		rest = append(rest, o)
	case o := <-cDone:
		rest = append(rest, o)
	case o := <-dDone:
		rest = append(rest, o)
	}

	// Exactly two were admitted and delivered; exactly one was refused on
	// the queue bound, and its sink was never reached.
	outs := append(rest, refusedOut)
	refused := 0
	for _, out := range outs {
		switch {
		case out.Err == nil:
			if len(out.Results) != 1 {
				t.Fatalf("admitted raise produced %d results, want 1", len(out.Results))
			}
		default:
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("%d of 3 refused, want exactly 1 (the queue admits 2)", refused)
	}
	if got := b.count() + c.count() + d.count(); got != 2 {
		t.Fatalf("delivered %d events, want 2 (b=%d c=%d d=%d)", got, b.count(), c.count(), d.count())
	}
}

// TestLimits_RetainedBytes_Bounded: retained payload bytes are bounded —
// the queue admits events until the byte budget is exhausted, and the next
// one fails visibly instead of growing the queue.
func TestLimits_RetainedBytes_Bounded(t *testing.T) {
	limits := notify.Limits{
		MaxInFlight:     1,
		MaxQueued:       4,
		MaxRetained:     10,
		DeliveryTimeout: time.Second,
	}
	gate := &unlockSink{
		called:  make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	b := &recordingSink{notified: make(chan struct{}, 1)}
	c := &recordingSink{notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: gate, Destination: notify.Destination{Target: "gate"}},
		},
		notify.Key{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: b, Destination: notify.Destination{Target: "b"}},
		},
		notify.Key{Kind: kindC, Trust: notify.TrustProgramRequest}: {
			{Sink: c, Destination: notify.Destination{Target: "c"}},
		},
	}, limits)
	if err != nil {
		t.Fatal(err)
	}

	aDone := make(chan notify.Outcome, 1)
	go func() { aDone <- r.Raise(context.Background(), event(kindA)) }()
	<-gate.called

	// Two 10-byte events race for a queue that admits 10 bytes: exactly one
	// is queued, the other is refused on the byte budget.
	big := func(kind notify.Kind) notify.Event {
		ev := event(kind)
		ev.Title = "0123456789"
		ev.Body = ""
		return ev
	}
	bDone := make(chan notify.Outcome, 1)
	cDone := make(chan notify.Outcome, 1)
	go func() { bDone <- r.Raise(context.Background(), big(kindB)) }()
	go func() { cDone <- r.Raise(context.Background(), big(kindC)) }()

	// Admission is settled the moment one of the two raises returns: the
	// 10-byte budget admits exactly one event, so the first racer queued
	// and the second was refused on the byte bound while the slot was
	// held. The refusal is the observable; the test releases the slot
	// then, never sleeping out the delivery deadline (AGENTS.md: a test
	// may not depend on timing).
	var refusedOut notify.Outcome
	select {
	case refusedOut = <-bDone:
	case refusedOut = <-cDone:
	}
	var refused *notify.RefusedError
	if !errors.As(refusedOut.Err, &refused) {
		t.Fatalf("want RefusedError for the second event, got %v", refusedOut.Err)
	}
	if refused.Limit != notify.LimitRetained {
		t.Fatalf("want limit %q, got %q", notify.LimitRetained, refused.Limit)
	}

	close(gate.release)
	if out := <-aDone; out.Err != nil {
		t.Fatalf("first raise failed: %v", out.Err)
	}
	// The queued event delivers once the slot is free.
	var queued notify.Outcome
	select {
	case queued = <-bDone:
	case queued = <-cDone:
	}
	if queued.Err != nil || len(queued.Results) != 1 {
		t.Fatalf("the queued event was not delivered: %+v", queued)
	}
	if queued.Results[0].Route.Sink == b {
		if b.count() != 1 || c.count() != 0 {
			t.Fatalf("deliveries: b=%d c=%d, want 1 and 0", b.count(), c.count())
		}
	} else {
		if c.count() != 1 || b.count() != 0 {
			t.Fatalf("deliveries: c=%d b=%d, want 1 and 0", c.count(), b.count())
		}
	}
}

// TestQueuedRaise_CancelledCaller_RemovedAndBytesRefunded: a queued event
// whose caller cancels is removed from the queue (the router stops
// retaining its data) and its bytes are refunded, so the next event admits.
func TestQueuedRaise_CancelledCaller_RemovedAndBytesRefunded(t *testing.T) {
	limits := notify.Limits{
		MaxInFlight:     1,
		MaxQueued:       4,
		MaxRetained:     10,
		DeliveryTimeout: time.Second,
	}
	gate := &unlockSink{
		called:  make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	b := &recordingSink{notified: make(chan struct{}, 1)}
	c := &recordingSink{notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: gate, Destination: notify.Destination{Target: "gate"}},
		},
		notify.Key{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: b, Destination: notify.Destination{Target: "b"}},
		},
		notify.Key{Kind: kindC, Trust: notify.TrustProgramRequest}: {
			{Sink: c, Destination: notify.Destination{Target: "c"}},
		},
	}, limits)
	if err != nil {
		t.Fatal(err)
	}

	aDone := make(chan notify.Outcome, 1)
	go func() { aDone <- r.Raise(context.Background(), event(kindA)) }()
	<-gate.called

	big := func(kind notify.Kind) notify.Event {
		ev := event(kind)
		ev.Title = "0123456789"
		ev.Body = ""
		return ev
	}
	bCtx, bCancel := context.WithCancel(context.Background())
	bDone := make(chan notify.Outcome, 1)
	go func() { bDone <- r.Raise(bCtx, big(kindB)) }()
	bCancel()
	if out := <-bDone; !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("cancelled queued raise: %v, want context.Canceled", out.Err)
	}
	if b.count() != 0 {
		t.Fatal("cancelled queued event reached its sink")
	}

	// The byte budget was refunded: C (10 bytes) admits into the queue.
	cDone := make(chan notify.Outcome, 1)
	go func() { cDone <- r.Raise(context.Background(), big(kindC)) }()
	close(gate.release)
	if out := <-aDone; out.Err != nil {
		t.Fatalf("first raise failed: %v", out.Err)
	}
	<-c.notified
	if out := <-cDone; out.Err != nil {
		t.Fatalf("raise after a refunded cancellation: %v", out.Err)
	}
}

// TestSinkPanic_DoesNotLeakSlot: a panicking sink is a failed delivery for
// its route, other routes still deliver, and the in-flight slot is released
// — finalization is one-shot and happens even when a sink does not return
// normally.
func TestSinkPanic_DoesNotLeakSlot(t *testing.T) {
	limits := notify.Limits{
		MaxInFlight:     1,
		MaxQueued:       0,
		MaxRetained:     0,
		DeliveryTimeout: time.Second,
	}
	rec := &recordingSink{notified: make(chan struct{}, 1)}
	after := &recordingSink{notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{
		notify.Key{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: panickingSink{}, Destination: notify.Destination{Target: "boom"}},
			{Sink: rec, Destination: notify.Destination{Target: "rec"}},
		},
		notify.Key{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: after, Destination: notify.Destination{Target: "after"}},
		},
	}, limits)
	if err != nil {
		t.Fatal(err)
	}

	out := r.Raise(context.Background(), event(kindA))
	if len(out.Results) != 2 {
		t.Fatalf("raise produced %d results, want 2", len(out.Results))
	}
	if out.Results[0].Err == nil {
		t.Fatal("panicking route produced no error")
	}
	if out.Results[1].Err != nil {
		t.Fatalf("route after the panic failed: %v", out.Results[1].Err)
	}
	if rec.count() != 1 {
		t.Fatalf("route after the panic was not reached")
	}

	// The slot was released: a second raise, which resolves to a real row,
	// admits and delivers immediately — with MaxQueued 0 it would be
	// refused if the panicking invocation had leaked the slot.
	out = r.Raise(context.Background(), event(kindB))
	if out.Err != nil {
		t.Fatalf("slot leaked after a panicking sink: %v", out.Err)
	}
	if after.count() != 1 {
		t.Fatal("raise after the panic did not deliver")
	}
}

// TestUnavailableHost_ReportsUnavailable: the host adapter for hosts with no
// desktop attention surface reports unavailable rather than panicking or
// silently succeeding.
func TestUnavailableHost_ReportsUnavailable(t *testing.T) {
	var host notify.UnavailableHost
	ctx := context.Background()
	if err := host.Banner(ctx, event(kindA)); !errors.Is(err, notify.ErrUnavailable) {
		t.Fatalf("Banner: %v, want ErrUnavailable", err)
	}
	if err := host.Badge(ctx, 3); !errors.Is(err, notify.ErrUnavailable) {
		t.Fatalf("Badge: %v, want ErrUnavailable", err)
	}
	if err := host.Bounce(ctx); !errors.Is(err, notify.ErrUnavailable) {
		t.Fatalf("Bounce: %v, want ErrUnavailable", err)
	}
}

// TestResultOutcomeNamesTheEventItReportsOn: every outcome the router can
// return names the event it is about (nocx-r6pxp).
//
// The reason is the failure surface, not tidiness. A delivery that fails
// after notify.raise has already answered has no caller left to fail to — it
// reaches a result handler, which receives an Outcome and nothing else. If
// the outcome cannot say which event failed, the handler cannot attribute
// the failure, and the only other way to know is somewhere remembering "the
// event I just submitted", which is two owners of one fact (AD-8).
//
// All three return paths are checked, because the one that matters most is
// the refusal: it never reaches a sink at all, so nothing downstream ever
// saw the event.
func TestResultOutcomeNamesTheEventItReportsOn(t *testing.T) {
	held := &unlockSink{called: make(chan struct{}, 1), release: make(chan struct{})}
	failing := &errSink{err: errPolicySink}
	r, err := notify.NewRouter(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: failing, Destination: notify.Destination{Target: "banner"}},
		},
		{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: held, Destination: notify.Destination{Target: "held"}},
		},
	}, notify.Limits{MaxInFlight: 1, MaxQueued: 0, MaxRetained: 1 << 20, DeliveryTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	attributed := func(kind notify.Kind) notify.Event {
		ev := event(kind)
		ev.Attribution = notify.Attribution{Backend: "local", Tab: "7", Host: "build-01", Session: "s1"}
		return ev
	}

	// 1. A sink that failed: the delivery happened and lost.
	ev := attributed(kindA)
	out := r.Raise(context.Background(), ev)
	if !errors.Is(out.Results[0].Err, errPolicySink) {
		t.Fatalf("route result err = %v, want the sink's error", out.Results[0].Err)
	}
	if out.Event.Attribution != ev.Attribution || out.Event.Title != ev.Title || out.Event.SessionID != ev.SessionID {
		t.Fatalf("failed delivery outcome names %+v, want the raised event %+v", out.Event, ev)
	}

	// 2. Default-deny: no row, no delivery, and still the event.
	denied := attributed(kindC)
	out = r.Raise(context.Background(), denied)
	if len(out.Resolved) != 0 || out.Err != nil {
		t.Fatalf("default-deny outcome: resolved %d, err %v", len(out.Resolved), out.Err)
	}
	if out.Event.SessionID != denied.SessionID || out.Event.Kind != kindC {
		t.Fatalf("default-deny outcome names %+v, want the raised event", out.Event)
	}

	// 3. Refused at admission: the single in-flight slot is held and the
	// queue bound is zero, so the next raise is refused without ever
	// reaching a sink — the path where nothing else ever saw the event.
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Raise(context.Background(), attributed(kindB))
	}()
	<-held.called

	refusedEv := attributed(kindA)
	refusedEv.Title = "the refused one"
	out = r.Raise(context.Background(), refusedEv)
	var refused *notify.RefusedError
	if !errors.As(out.Err, &refused) {
		t.Fatalf("outcome err = %v, want a refusal", out.Err)
	}
	if out.Event.Title != "the refused one" || out.Event.Attribution != refusedEv.Attribution {
		t.Fatalf("refused outcome names %+v, want the refused event %+v", out.Event, refusedEv)
	}

	close(held.release)
	<-done
}
