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
// open here, and AccessBump — a real revocation's own call — enforces the
// ordering §7.2 states in its own words: "the owner acknowledges [a bump]
// only after every older uncommitted intent is terminal". This fake plays
// the owner's part for both calls at once, which is what lets the test
// assert the ordering rather than assume it: AccessBump blocks on
// intentDone, and only Intent's own release closes it — so an AccessBump
// that returned early would be a bug in the FAKE the test would itself
// catch by timing out on the wrong branch, not a bug the test takes on
// faith.
type intentHeldHelper struct {
	intentEntered chan struct{}
	release       chan struct{}
	intentDone    chan struct{}

	mu       sync.Mutex
	bumpedTo uint64
	seenAt   uint64 // the AccessEpoch Intent's own payload carried, set once
}

func (h *intentHeldHelper) Intent(ctx context.Context, _ string, p proto.IntentParams) (proto.IntentResult, error) {
	h.mu.Lock()
	h.seenAt = p.AccessEpoch
	h.mu.Unlock()
	close(h.intentEntered)
	select {
	case <-h.release:
	case <-ctx.Done():
		return proto.IntentResult{}, ctx.Err()
	}
	defer close(h.intentDone)
	h.mu.Lock()
	bumped := h.bumpedTo
	h.mu.Unlock()
	if bumped != 0 && p.AccessEpoch < bumped {
		return proto.IntentResult{State: "refused", Refusal: &proto.IntentRefusal{Cause: "access_revoked"}}, nil
	}
	return proto.IntentResult{State: "executed", BytesWritten: 1}, nil
}

func (h *intentHeldHelper) AccessBump(ctx context.Context, _ string, above uint64) (uint64, error) {
	h.mu.Lock()
	h.bumpedTo = above + 1
	h.mu.Unlock()
	select {
	case <-h.intentDone:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
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

// TestAnIntentAdmittedBeforeRevocationIsRefusedAtCommit is Task 7's own
// plan-listed acceptance criterion for this schedule, never written until
// Task 9 built session.intent (this file's own finding above, and
// nocx-6q1uh.13b): a real PaneKeys.Send passes its authority check (the
// token's record resolves, StillHolds holds), is held at the real
// session.intent call by intentHeldHelper, a real revocation runs
// concurrently and bumps the session's access epoch, and only once the held
// intent has been refused access_revoked by (this fake's model of) the
// owner's own commit-point check does the revocation itself report ack —
// never before.
func TestAnIntentAdmittedBeforeRevocationIsRefusedAtCommit(t *testing.T) {
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

	revokeDone := make(chan map[string]string, 1)
	go func() { revokeDone <- hub.revoke(context.Background(), []string{sessionID}) }()

	// Deterministic, not a race: the held intent has not resolved (release
	// is not yet closed), and revoke's only path to returning is the
	// helper's AccessBump, which this fake ties to intentDone.
	select {
	case res := <-revokeDone:
		t.Fatalf("revoke reported %v before the held intent was refused", res)
	default:
	}

	close(helper.release) // the held intent now sees the bumped epoch and refuses

	var outcome sendOutcome
	select {
	case outcome = <-sendDone:
	case <-time.After(watchdogBound):
		t.Fatal("Send never returned after the held intent was released")
	}
	if outcome.err != nil {
		t.Fatalf("Send: %v", outcome.err)
	}
	if outcome.res.State != "refused" || outcome.res.Refusal == nil || outcome.res.Refusal.Cause != "access_revoked" {
		t.Fatalf("Send result = %+v, want refused/access_revoked", outcome.res)
	}
	if outcome.res.BytesWritten != 0 {
		t.Fatalf("bytesWritten = %d, want 0 for a refusal", outcome.res.BytesWritten)
	}

	select {
	case res := <-revokeDone:
		if res[sessionID] != "ack" {
			t.Fatalf("confirmedBy = %v, want ack once the held intent resolved", res)
		}
	case <-time.After(watchdogBound):
		t.Fatal("revoke did not return after the held intent resolved")
	}
	if got := helper.seenAt; got != oldEpoch {
		t.Fatalf("the intent that reached the helper carried AccessEpoch=%d, want the OLD epoch %d it was admitted under", got, oldEpoch)
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
