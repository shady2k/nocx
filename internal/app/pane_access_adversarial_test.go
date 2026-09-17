package app

// Independent adversarial tests for design §7.1–7.2's authority promises,
// written by a worker who did not implement Task 7 (AGENTS.md testing rule
// 4). These reuse the fakes pane_access_test.go already defines (fakeHelper,
// fakeLookup, fakePaneClock, newGroupTwoCallersRecord) but drive them
// through schedules that file was not written to cover.
//
// bead: nocx-6q1uh.13
//
// nocx-6q1uh.9 (Task 9) replaced the approximation this file originally
// carried, TestRevocationDoesNotReportAckBeforeAHeldIntentIsRefused, with
// TestAnIntentAdmittedBeforeRevocationIsRefusedAtCommit below, now that
// PaneKeys and session.intent exist: the held call is Intent itself (the
// real seam, session_keys.go's commitStep), not AccessBump standing in for
// it, and the refusal is the real access_revoked IntentRefusal, not an
// inferred stand-in read off the hub's own epoch bookkeeping. The original
// finding (§7.2 describes a per-INTENT barrier that this package could not
// yet exercise directly, because Task 7 predates session.intent) is why
// that approximation existed in the first place; it is superseded, not
// merely kept alongside the real thing — one behaviour, one test, per
// AGENTS.md "Two surfaces may never own the same input".

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// ── item 1: revocation between the authority check and the helper write ───

// intentHeldHelper is the real seam §7.2's barrier is about: Intent (Task
// 9's session.intent client call, session_keys.go's commitStep) is held
// open here, modelling the interval the real owner spends between
// commitIntent (Admit+Commit — already past tokenGate's own epoch check at
// receipt) and the physical write actually finishing
// (internal/helper/session/owner.go's writerBusy/inFlight), while
// AccessBump plays a concurrent revocation's own call.
//
// This is NOT the "still queued" interval applyAccessBump's own sweep
// refuses (internal/helper/session/access.go): that sweep only ever
// inspects o.pending, and an intent already past commitIntent has already
// left it. access.go's own doc says so in as many words — "an item already
// writing when this bump arrived was gated, admitted and committed under a
// PRIOR epoch and is left to finish: spec §7.2 bounds it by commitBy, not
// by asking the owner to abandon bytes already in flight" — and
// applyAccessBump resolves the bump itself the moment its sweep is done,
// never waiting on an item that is not (any longer) in it. This fake
// mirrors exactly that, rather than the earlier version's own invented
// mid-flight epoch recheck, which no code here performs: AccessBump answers
// immediately (nothing is queued behind this one intent — it is already
// writerBusy, not pending), and the held Intent completes on its own terms
// once released, its already-admitted epoch untouched.
type intentHeldHelper struct {
	intentEntered chan struct{}
	release       chan struct{}
	intentDone    chan struct{}

	mu     sync.Mutex
	seenAt uint64 // the AccessEpoch Intent's own payload carried, set once
}

func (h *intentHeldHelper) Intent(ctx context.Context, _ string, p proto.IntentParams) (proto.IntentResult, error) {
	h.mu.Lock()
	h.seenAt = p.AccessEpoch
	h.mu.Unlock()
	close(h.intentEntered)
	defer close(h.intentDone)
	select {
	case <-h.release:
	case <-ctx.Done():
		return proto.IntentResult{}, ctx.Err()
	}
	// Already past tokenGate's own epoch check and admitted/committed
	// (access.go: "gated, admitted and committed under a PRIOR epoch"): a
	// concurrent bump does not reach back for it, so there is nothing left
	// to recheck here — it executes on the epoch it was admitted under.
	return proto.IntentResult{State: "executed", BytesWritten: 1}, nil
}

func (h *intentHeldHelper) AccessBump(_ context.Context, _ string, above uint64) (uint64, error) {
	// applyAccessBump resolves the bump the moment its sweep of o.pending is
	// done. With nothing queued behind the held intent — it is already
	// writerBusy, never appended to o.pending — that sweep finds nothing,
	// so the real owner's own ack is immediate; it never waits on
	// intentDone, and neither does this fake.
	return above + 1, nil
}

// Snapshot, Target and IntentStatus are not exercised by this fake's own
// test (it models Intent/AccessBump's ordering alone); these satisfy
// paneHelpers.
func (h *intentHeldHelper) Snapshot(context.Context, string) (proto.SnapshotResult, error) {
	return proto.SnapshotResult{}, errors.New("intentHeldHelper: Snapshot not configured")
}

func (h *intentHeldHelper) Target(context.Context, string, proto.TargetParams) (proto.TargetResult, error) {
	return proto.TargetResult{}, errors.New("intentHeldHelper: Target not configured")
}

func (h *intentHeldHelper) IntentStatus(context.Context, string, string) (proto.IntentStatusResult, error) {
	return proto.IntentStatusResult{}, errors.New("intentHeldHelper: IntentStatus not configured")
}

// fakeKeysReader is the minimal paneKeysReader (session_keys.go) this test
// needs: Record answers from a fixed table (the one target this test mints
// by hand, before Send is ever called); Read is not exercised here — the
// option loop's own tests (Task 9) exercise it directly.
type fakeKeysReader struct {
	records map[string]targetRecord
}

func (r *fakeKeysReader) Record(tokenID string) (targetRecord, bool) {
	rec, ok := r.records[tokenID]
	return rec, ok
}

func (r *fakeKeysReader) Read(context.Context, any, string, *sessionruntime.TargetKind, *sessionruntime.RowRange) (assistant.PaneRead, error) {
	return assistant.PaneRead{}, errors.New("fakeKeysReader: Read not configured")
}

// TestAnIntentAlreadyAdmittedCompletesUnrefusedByALaterRevocation replaces
// this file's own earlier version of this schedule (nocx-6q1uh.18), which
// carried Task 7's plan-listed acceptance criterion verbatim — "the intent
// reaches the helper with the old epoch and is access_revoked" — against a
// fake that invented its own mid-flight epoch recheck inside Intent to make
// that come true. Neither the real owner (internal/helper/session/owner.go)
// nor its bump (access.go) performs any such recheck: tokenGate's epoch
// gate runs once, AT RECEIPT, before an intent is ever appended to
// o.pending, and applyAccessBump's own sweep only ever inspects o.pending —
// an intent already past commitIntent (admitted, on its way to the writer)
// has already left it and is, in access.go's own words, "left to finish".
// So a real PaneKeys.Send that passed its authority check and reached
// session.intent before a revocation started is not "outrun" by it: it
// completes on the epoch it was admitted under, and the revocation's own
// ack does not wait for it — there is nothing left in the queue for its
// sweep to find. AGENTS.md, "before you fix anything": the plan's own
// criterion was written before access.go's real mechanics existed to check
// it against; the tree is what a test may not misrepresent, so this
// exercises the real barrier — the sweep refuses what is still QUEUED, an
// admitted intent is not that — rather than the plan's incorrect guess.
func TestAnIntentAlreadyAdmittedCompletesUnrefusedByALaterRevocation(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	w, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-D", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	sessionID := string(w.Participant.ID)

	const oldEpoch = 1
	helper := &intentHeldHelper{
		intentEntered: make(chan struct{}), release: make(chan struct{}), intentDone: make(chan struct{}),
	}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{sessionID: helper}}
	hub := newPaneAccessHub(registrar, lookup, nil)
	hub.setEpoch(sessionID, oldEpoch)
	access := hub.Bind("sess-D", session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})

	reach, err := access.Resolve(ctx, sessionID, workers.EffectSendInput)
	if err != nil {
		t.Fatalf("resolve before revocation: %v", err)
	}
	reader := &fakeKeysReader{records: map[string]targetRecord{
		"tok-1": {
			Access: access, SessionID: sessionID, Chain: reach.Chain, AccessEpoch: oldEpoch,
			View: assistant.TargetView{Token: "tok-1-signed", TokenID: "tok-1", Kind: sessionruntime.TargetInput},
		},
	}}
	keys := newPaneKeys(reader, hub)

	type sendOutcome struct {
		res assistant.KeysResult
		err error
	}
	sendDone := make(chan sendOutcome, 1)
	go func() {
		key := assistant.KeyName("Down")
		res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "tok-1", Key: &key})
		sendDone <- sendOutcome{res, err}
	}()

	select {
	case <-helper.intentEntered:
	case <-time.After(watchdogBound):
		t.Fatal("Send never reached the helper's Intent — the authority check did not pass, or admission was already closed")
	}

	// The intent has already reached the helper and is on its way to the
	// writer (intentEntered closed) — access.go's own "already writing" —
	// so a concurrent revoke has nothing queued behind it to sweep, and
	// acks immediately: it must NOT wait on the held intent's own release.
	revokeDone := make(chan map[string]string, 1)
	go func() { revokeDone <- hub.revoke(context.Background(), []string{sessionID}) }()

	select {
	case res := <-revokeDone:
		if res[sessionID] != "ack" {
			t.Fatalf("revoke result = %v, want ack (nothing was queued behind the already-admitted intent)", res)
		}
	case <-time.After(watchdogBound):
		t.Fatal("revoke waited on the already-admitted intent instead of acking immediately (access.go: nothing left in o.pending to sweep)")
	}

	// The ack above must not have touched the held intent itself: it is
	// still exactly that — held — and settles strictly on its own release,
	// never as a side effect of the revocation's own return.
	select {
	case <-helper.intentDone:
		t.Fatal("the held intent finished before its own release fired")
	default:
	}

	close(helper.release) // the held intent now completes on its own terms

	var outcome sendOutcome
	select {
	case outcome = <-sendDone:
	case <-time.After(watchdogBound):
		t.Fatal("Send never returned after the held intent was released")
	}
	if outcome.err != nil {
		t.Fatalf("Send: %v", outcome.err)
	}
	if outcome.res.State != "executed" || outcome.res.BytesWritten != 1 {
		t.Fatalf("Send result = %+v, want the already-admitted intent to complete unrefused "+
			"(access.go: an item already writing when a bump arrives is left to finish)", outcome.res)
	}
	// Design §7.2: the intent carries the epoch ITS OWN authority check
	// observed, never one re-read after the fact — this is the one part of
	// the original finding that still holds, and the one a regression here
	// would actually be about (session_keys.go threading rec.AccessEpoch,
	// not a live re-read of the hub's current epoch).
	if got := helper.seenAt; got != oldEpoch {
		t.Fatalf("the intent that reached the helper carried AccessEpoch=%d, want the OLD epoch %d its own authority check observed", got, oldEpoch)
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
