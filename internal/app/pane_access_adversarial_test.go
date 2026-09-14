package app

// Independent adversarial tests for design §7.1–7.2's authority promises,
// written by a worker who did not implement Task 7 (AGENTS.md testing rule
// 4). These reuse the fakes pane_access_test.go already defines (fakeHelper,
// fakeLookup, fakePaneClock, newGroupTwoCallersRecord) but drive them
// through schedules that file was not written to cover.
//
// bead: nocx-6q1uh.13
//
// FINDING (reported in full in the worker's session report, summarized
// here): §7.2 describes a per-INTENT barrier — "the owner acknowledges [a
// bump] only after every older uncommitted intent is terminal", and refuses
// a stale intent at commit with `access_revoked`, carrying the epoch it was
// admitted under. Task 7 (this package, internal/workers) has not built
// session.intent yet (that is Task 9/10), so there is no production type
// named "intent", no `access_revoked` outcome, and no per-intent epoch
// anywhere in this package today — confirmed by grep: `access_revoked`
// appears nowhere in internal/. The tests below model the barrier against
// the one seam this layer exposes for it (paneHelpers.AccessBump blocking,
// which IS how the owner is expected to hold a bump open per §7.2's own
// wording), and record the epoch the hub itself has for a session
// (aboveFor/setEpoch) as the closest available stand-in for "the epoch an
// admitted intent carries". Task 7's own plan-listed acceptance criterion
// for this exact schedule, `TestAnIntentAdmittedBeforeRevocationIsRefusedAt
// Commit`, was never written in pane_access_test.go — grep confirms no such
// test exists on this branch. This gap should be revisited once Task 9/10
// land a real session.intent seam (13b).

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/workers"
)

// ── item 1: revocation between the authority check and the helper write ───

// heldIntentHelper models §7.2's "the owner applies a bump ahead of queued
// intents and acknowledges only after every older uncommitted intent is
// terminal" at the seam this layer has today: AccessBump blocking IS the
// helper holding an older intent open. entered fires the instant the bump
// itself reaches the helper (after paneAccessHub.revokeOne has already read
// the session's recorded epoch via aboveFor); release lets the fake finish
// that held intent and answer, which is this test's stand-in for "the held
// intent went terminal".
type heldIntentHelper struct {
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
	epoch       uint64
	aboveSeen   uint64 // set atomically: the epoch threshold the held call actually saw
}

func (h *heldIntentHelper) AccessBump(ctx context.Context, sessionID string, above uint64) (uint64, error) {
	atomic.StoreUint64(&h.aboveSeen, above)
	h.enteredOnce.Do(func() { close(h.entered) })
	select {
	case <-h.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return h.epoch, nil
}

// Snapshot and Target are not exercised by this fake's own tests (it
// models AccessBump's hold alone); these satisfy paneHelpers.
func (h *heldIntentHelper) Snapshot(context.Context, string) (proto.SnapshotResult, error) {
	return proto.SnapshotResult{}, errors.New("heldIntentHelper: Snapshot not configured")
}

func (h *heldIntentHelper) Target(context.Context, string, proto.TargetParams) (proto.TargetResult, error) {
	return proto.TargetResult{}, errors.New("heldIntentHelper: Target not configured")
}

// A revocation must not report ack while an intent it needs to outlast is
// still held: it may only ack once that intent has resolved (refused, in
// the design's own wording, once session.intent exists). While held, a
// concurrent reader of the hub's own epoch bookkeeping sees the OLD epoch —
// the one every intent admitted so far was checked against — never one a
// still-outstanding bump has applied early.
func TestRevocationDoesNotReportAckBeforeAHeldIntentIsRefused(t *testing.T) {
	const oldEpoch = 1
	helper := &heldIntentHelper{entered: make(chan struct{}), release: make(chan struct{}), epoch: oldEpoch + 1}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{"sess-w1": helper}}
	hub := newPaneAccessHub(nil, lookup, nil)
	hub.setEpoch("sess-w1", oldEpoch)

	doneCh := make(chan map[string]string, 1)
	go func() { doneCh <- hub.revoke(context.Background(), []string{"sess-w1"}) }()

	select {
	case <-helper.entered:
	case <-time.After(watchdogBound):
		t.Fatal("revoke never reached the helper")
	}

	// Deterministic, not a race: the held intent has not resolved (release
	// is not yet closed), and revoke's only paths to returning are the
	// helper answering or a commitBy deadline (none set here).
	select {
	case res := <-doneCh:
		t.Fatalf("revoke reported %v before the held intent was refused", res)
	default:
	}
	if got := hub.aboveFor("sess-w1"); got != oldEpoch {
		t.Fatalf("epoch visible while the intent is held = %d, want the old epoch %d", got, oldEpoch)
	}

	close(helper.release) // the held intent is now terminal

	select {
	case res := <-doneCh:
		if res["sess-w1"] != "ack" {
			t.Fatalf("confirmedBy = %v, want ack once the held intent resolved", res)
		}
	case <-time.After(watchdogBound):
		t.Fatal("revoke did not return after the held intent resolved")
	}
	if got := atomic.LoadUint64(&helper.aboveSeen); got != oldEpoch {
		t.Fatalf("the bump reaching the helper carried above=%d, want the old epoch %d recorded before it was sent", got, oldEpoch)
	}
}

// Paired ordinary success: when nothing is held (the helper answers with no
// gate at all), revoke acks without any interval where it could have blocked
// on anything — the failure case above is measured against this baseline.
func TestRevocationAcksImmediatelyWhenNothingIsHeld(t *testing.T) {
	helper := &fakeHelper{epoch: 3}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{"sess-w1": helper}}
	hub := newPaneAccessHub(nil, lookup, nil)
	hub.setEpoch("sess-w1", 1)

	res := hub.revoke(context.Background(), []string{"sess-w1"})
	if res["sess-w1"] != "ack" {
		t.Fatalf("confirmedBy = %v, want ack when nothing was held", res)
	}
}

// ── item 4: an unanswering helper waited out step by step by commitBy ─────

// revoke must never report a result before the injected clock actually
// crosses the latest noted commitBy — checked at several intermediate clock
// readings, not just the final one, so a poll loop that fired early on some
// intermediate value (rather than only at or after the deadline) would be
// caught (design §7.2: "observe state, not durations", AGENTS.md).
func TestRevokeIsNeverConfirmedBeforeTheClockCrossesTheLatestCommitBy(t *testing.T) {
	clock := &fakePaneClock{now: 1000}
	helper := &fakeHelper{entered: make(chan struct{}), never: true}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{"sess-w1": helper}}
	hub := newPaneAccessHub(nil, lookup, clock)
	hub.noteCommitBy("sess-w1", 2000)

	doneCh := make(chan map[string]string, 1)
	go func() { doneCh <- hub.revoke(context.Background(), []string{"sess-w1"}) }()

	select {
	case <-helper.entered:
	case <-time.After(watchdogBound):
		t.Fatal("revoke never reached the helper")
	}

	for _, step := range []Nanos{1200, 1500, 1800, 1950, 1999} {
		clock.advance(step)
		// Give the production poll loop (pollInterval, a real 1ms ticker)
		// several real cycles to have rechecked and wrongly fired before
		// asserting it did not — a false PASS is impossible here (the
		// select is non-blocking), only a slow scheduler could produce a
		// false confidence, which the sleep below is sized against.
		time.Sleep(10 * time.Millisecond)
		select {
		case res := <-doneCh:
			t.Fatalf("revoke reported %v at clock=%d, before its commitBy 2000 passed", res, step)
		default:
		}
	}

	clock.advance(2001)
	select {
	case res := <-doneCh:
		if res["sess-w1"] != "deadline" {
			t.Fatalf("confirmedBy = %v, want deadline", res)
		}
	case <-time.After(watchdogBound):
		t.Fatal("revoke did not resolve once the clock passed commitBy")
	}
}

// Paired ordinary success: a helper that answers promptly acks right away
// even though a commitBy deadline is pending far in the future — the
// deadline is a bound on the WAIT, never a delay revoke imposes on its own.
func TestRevokeAcksAsSoonAsTheHelperAnswersDespiteAPendingCommitBy(t *testing.T) {
	clock := &fakePaneClock{now: 1000}
	helper := &fakeHelper{epoch: 9}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{"sess-w1": helper}}
	hub := newPaneAccessHub(nil, lookup, clock)
	hub.noteCommitBy("sess-w1", 1_000_000) // far in the future

	res := hub.revoke(context.Background(), []string{"sess-w1"})
	if res["sess-w1"] != "ack" {
		t.Fatalf("confirmedBy = %v, want ack — a prompt answer must not wait for any deadline", res)
	}
}

// ── item 6: the two callers cannot use each other's authority ─────────────

// A DescendantPaneAccess bound for controller A can never resolve B's
// descendants, and vice versa — enforced today because Resolve always
// re-derives the chain from the BOUND controller, never from the session
// asking.
func TestBoundControllersCannotResolveEachOthersDescendants(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()

	wA, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-A", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register under A: %v", err)
	}
	wB, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-B", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register under B: %v", err)
	}

	hub := newPaneAccessHub(registrar, nil, nil)
	accessA := hub.Bind("sess-A", session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	accessB := hub.Bind("sess-B", session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 2})

	if _, err := accessB.Resolve(ctx, string(wA.Participant.ID), workers.EffectObserve); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("B resolved A's descendant: err = %v, want ErrNotReachable", err)
	}
	if _, err := accessA.Resolve(ctx, string(wB.Participant.ID), workers.EffectObserve); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("A resolved B's descendant: err = %v, want ErrNotReachable", err)
	}

	// Paired ordinary success: each capability still resolves its own.
	if _, err := accessA.Resolve(ctx, string(wA.Participant.ID), workers.EffectObserve); err != nil {
		t.Fatalf("A could not resolve its own descendant: %v", err)
	}
	if _, err := accessB.Resolve(ctx, string(wB.Participant.ID), workers.EffectObserve); err != nil {
		t.Fatalf("B could not resolve its own descendant: %v", err)
	}
}

// FORMERLY TestAuthorityKindIsBoundButNotEnforcedByAnyDownstreamCheck
// (nocx-6q1uh.13a's finding): AuthorityInterval was one struct with a plain
// string Kind nothing read below Bind, so an access minted "endpoint" and
// one minted "kernel" for the same controller resolved identically and
// nothing downstream could tell them apart. Task 8 closed that gap by
// making the two variants distinct TYPES (EndpointAuthority,
// KernelAuthority) behind a sealed Authority interface, with AsEndpoint/
// AsKernel accessors that refuse the wrong variant by a failed type
// assertion rather than by comparing a label. This test now demonstrates
// the closed gap directly: Resolve still treats both bindings identically
// (§7.1 never said reach should differ by adapter), but a consumer that
// needs ONE variant's own fact — here, the admission epoch only
// EndpointAuthority carries — is refused the other outright.
func TestAKernelBoundAccessRefusesTheEndpointAccessorAndViceVersa(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	w, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-C", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	hub := newPaneAccessHub(registrar, nil, nil)

	endpointBound := hub.Bind("sess-C", session.Identity{InstanceID: "backend-A", Epoch: 1},
		EndpointAuthority{AdmissionEpoch: 7})
	kernelBound := hub.Bind("sess-C", session.Identity{InstanceID: "backend-A", Epoch: 1},
		KernelAuthority{RunID: "run-1"})

	// The refusal: each binding's accessor for the OTHER variant fails,
	// rather than silently returning a zero value a caller might mistake
	// for a real fact.
	if _, ok := kernelBound.AsEndpoint(); ok {
		t.Fatal("a kernel-bound access answered AsEndpoint as ok — the wrong variant was not refused")
	}
	if _, ok := endpointBound.AsKernel(); ok {
		t.Fatal("an endpoint-bound access answered AsKernel as ok — the wrong variant was not refused")
	}

	// Paired ordinary success: each binding's OWN accessor answers its own
	// fact correctly.
	ep, ok := endpointBound.AsEndpoint()
	if !ok || ep.AdmissionEpoch != 7 {
		t.Fatalf("endpoint-bound AsEndpoint = %+v, %v, want {AdmissionEpoch:7}, true", ep, ok)
	}
	k, ok := kernelBound.AsKernel()
	if !ok || k.RunID != "run-1" {
		t.Fatalf("kernel-bound AsKernel = %+v, %v, want {RunID:run-1}, true", k, ok)
	}

	// Resolve itself is unaffected by which variant bound the access: reach
	// is a property of the controller and the chain, never of the adapter
	// that authenticated the caller (§7.1's own scope for the two
	// variants).
	epReach, epErr := endpointBound.Resolve(ctx, string(w.Participant.ID), workers.EffectObserve)
	kReach, kErr := kernelBound.Resolve(ctx, string(w.Participant.ID), workers.EffectObserve)
	if epErr != nil || kErr != nil {
		t.Fatalf("resolve: endpoint err=%v kernel err=%v, want both to succeed", epErr, kErr)
	}
	if epReach.SessionID != kReach.SessionID || len(epReach.Chain) != len(kReach.Chain) {
		t.Fatalf("endpoint- and kernel-bound access resolved differently for the same controller: %+v vs %+v", epReach, kReach)
	}
}

// ── item 7: no coordinator lock across a helper call, under contention ────

// Sixteen goroutines resolving OTHER sessions must complete while one
// revocation's helper call blocks forever — scaled well past the
// single-session case pane_access_test.go already covers, and through
// RevokeParticipant (Registrar.Revoke composed with the helper-epoch bump)
// rather than the hub's revoke alone, so the registrar's own storeMu is
// exercised in the same call that blocks on a helper.
func TestSixteenConcurrentResolvesCompleteWhileOneRevokeBlocksOnItsHelperForever(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()

	blocked, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-C", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register the blocked worker: %v", err)
	}
	blockedSession := string(blocked.Participant.ID)

	const n = 16
	others := make([]string, n)
	for i := 0; i < n; i++ {
		w, err := registrar.Register(ctx, workers.RegisterRequest{
			CoordinatorSession: "sess-C", Role: workers.RoleWorker,
			Task: "t", Command: "agent", Environment: "env-local",
		})
		if err != nil {
			t.Fatalf("register other worker %d: %v", i, err)
		}
		others[i] = string(w.Participant.ID)
	}

	helper := &fakeHelper{entered: make(chan struct{}), never: true}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{blockedSession: helper}}
	hub := newPaneAccessHub(registrar, lookup, nil)

	blockedCtx, cancel := context.WithCancel(ctx)
	defer cancel() // releases the leaked goroutine once the test ends
	go func() { _, _ = hub.RevokeParticipant(blockedCtx, blocked.Participant.ID, "contention") }()

	select {
	case <-helper.entered:
	case <-time.After(watchdogBound):
		t.Fatal("revoke never reached the blocked helper")
	}

	var wg sync.WaitGroup
	var completed int32
	for _, sid := range others {
		sid := sid
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := registrar.Resolve(ctx, "sess-C", sid, workers.EffectObserve); err != nil {
				t.Errorf("resolve %q while a revoke was blocked on its helper: %v", sid, err)
				return
			}
			atomic.AddInt32(&completed, 1)
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(watchdogBound):
		t.Fatalf("only %d of %d resolves completed while one revoke's helper call blocked forever — a coordinator lock is held across it", atomic.LoadInt32(&completed), n)
	}
	if got := atomic.LoadInt32(&completed); got != n {
		t.Fatalf("completed = %d, want %d", got, n)
	}
}
