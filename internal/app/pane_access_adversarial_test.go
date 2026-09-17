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
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
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

// ── item 1b: a revocation that COMPLETED while the intent was still held ──

// nocx-6q1uh.19. Item 1 above exercises the interval on the far side of the
// helper's receipt — the intent is already admitted and on its way to the
// writer, and access.go leaves it to finish. This section exercises the
// interval BEFORE the receipt, which is the one §7.2's epoch is for and the
// one nothing covered: the coordinator's authority check has passed, the
// intent is parked between that check and the session.intent call, the
// revocation runs all the way to its acknowledgement, and only THEN is the
// intent sent — carrying the epoch its own check observed. The helper must
// refuse it at receipt (access_revoked) with zero bytes reaching the program.
//
// The epoch enforcement under test is tokenGate
// (internal/helper/session/tokens.go) and it runs inside the real owner, so
// the stand below is a real internal/helper/session Service on a real host
// peer reached through a real helperclient — the recipe
// session_close_releases_helper_budget_test.go already uses for a
// helper-hosted session — rather than a fake that would only be agreeing with
// its own author about what the gate says. The ONE injected seam is a carrier
// that parks Intent before it ever reaches the helper: the "test hook"
// holding the intent is a channel and never a sleep, and nothing in it decides
// anything about epochs.

// heldIntentProcess is the program this stand's session writes to: it keeps
// its pipe open, produces nothing, and records every byte the owner's write
// path hands it — which is what "zero bytes" is read off, rather than a
// returned field.
//
// It answers rawReader (WaitReadable/RawReadUntilAgain, owner.go) the way a
// local PTY does, and that is load-bearing rather than incidental:
// hasReadBarrier (owner_ssh.go) is what decides whether an intent may commit
// at all, so a process without it would refuse EVERY intent in this section
// no_read_barrier and these tests would then be measuring the wrong gate.
//
// Its read side mirrors a real PTY's shape rather than simplifying it: a
// readiness EDGE the owner waits on, and a drain that reports EOF once the
// process is gone. A fixture whose WaitReadable returned an error instead of
// waking the drain would leave the owner's own eofSeen false forever, and
// Service.Close — which waits for the owner goroutine to exit — would never
// return (measured: a 10-minute package timeout from a test that had already
// recorded its own failure).
type heldIntentProcess struct {
	mu     sync.Mutex
	writes [][]byte
	ready  chan struct{}
	ended  chan struct{}
	closed sync.Once
	pid    int
}

func newHeldIntentProcess(pid int) *heldIntentProcess {
	return &heldIntentProcess{ready: make(chan struct{}, 1), ended: make(chan struct{}), pid: pid}
}

func (p *heldIntentProcess) Read([]byte) (int, error) { <-p.ended; return 0, io.EOF }

func (p *heldIntentProcess) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.writes = append(p.writes, append([]byte(nil), b...))
	p.mu.Unlock()
	return len(b), nil
}

func (p *heldIntentProcess) Close() error {
	p.closed.Do(func() {
		close(p.ended)
		p.wake() // the drain that reports EOF: a real PTY's fd becomes readable
	})
	return nil
}

// wake raises the readiness edge without blocking on a drain that has not
// arrived yet (the shape runReadiness itself relies on).
func (p *heldIntentProcess) wake() {
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

// WaitReadable parks until there is something to drain or the read is
// cancelled. It consumes nothing: the owner's own drain (drainLocal) reads.
func (p *heldIntentProcess) WaitReadable(ctx context.Context) error {
	select {
	case <-p.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RawReadUntilAgain delivers nothing and answers EOF exactly once the
// process has ended — rawReaderFakeProcess's own contract, which is what
// makes the owner's eofSeen true and lets it exit.
func (p *heldIntentProcess) RawReadUntilAgain([]byte, func([]byte)) (bool, error) {
	select {
	case <-p.ended:
		return true, nil
	default:
		return false, nil
	}
}

func (p *heldIntentProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (p *heldIntentProcess) Done() <-chan struct{}                { return p.ended }
func (p *heldIntentProcess) WaitErr() (error, bool)               { return nil, false }
func (p *heldIntentProcess) Pid() int                             { return p.pid }
func (p *heldIntentProcess) Shell() string                        { return "/bin/held-intent" }
func (p *heldIntentProcess) ForegroundProcessGroup() (int, error) { return p.pid, nil }

// written is every payload the write path handed this program, in order.
func (p *heldIntentProcess) written() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.writes...)
}

// heldIntentSpawner is the helper's Spawner with nothing behind it: what these
// tests ask is what the helper's own gate decides, so what a shell would print
// is not part of them — the same substitution session_readopt_test.go's
// scriptedSpawner makes, and for the same reason.
type heldIntentSpawner struct {
	mu    sync.Mutex
	procs []*heldIntentProcess
}

func (s *heldIntentSpawner) Spawn(helpersession.SpawnRequest) (helpersession.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := newHeldIntentProcess(5000 + len(s.procs))
	s.procs = append(s.procs, p)
	return p, nil
}

// only is the program this helper started, which every stand here expects to
// be exactly one.
func (s *heldIntentSpawner) only(t *testing.T) *heldIntentProcess {
	t.Helper()
	s.mu.Lock()
	procs := append([]*heldIntentProcess(nil), s.procs...)
	s.mu.Unlock()
	if len(procs) != 1 {
		t.Fatalf("%d programs were started, want exactly 1", len(procs))
	}
	return procs[0]
}

// heldIntentCarrier is the per-session paneHelpers handle this section's hub
// resolves: the app's own helperPaneClient (panescreen.go) — the adapter the
// production lookup returns for a session it already located — with ONE
// injected seam. Intent parks at the boundary between the coordinator's own
// checks and the RPC's arrival at the helper, which is exactly the interval
// §7.2's access epoch is for; AccessBump, Snapshot, Target and IntentStatus
// pass straight through, so the revocation this section drives is the real
// one and no gate is re-implemented here.
type heldIntentCarrier struct {
	inner helperPaneClient

	// hold parks Intent until release is closed. It is set once, before the
	// call under test starts.
	hold    bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu      sync.Mutex
	intents []proto.IntentParams
}

func (h *heldIntentCarrier) Intent(ctx context.Context, sessionID string, p proto.IntentParams) (proto.IntentResult, error) {
	h.mu.Lock()
	h.intents = append(h.intents, p)
	h.mu.Unlock()
	if h.hold {
		h.once.Do(func() { close(h.entered) })
		select {
		case <-h.release:
		case <-ctx.Done():
			return proto.IntentResult{}, ctx.Err()
		}
	}
	return h.inner.Intent(ctx, sessionID, p)
}

// sent is every intent handed to this seam, in order — read BEFORE the held
// one is released, which is what makes "the intent carries the epoch its own
// check observed" an observation rather than a claim about what the
// coordinator meant to send. Its LENGTH is asserted too: one call to
// session.keys spends its target at most once (spec §6.2), and a second
// intent reaching the helper would be that promise broken.
func (h *heldIntentCarrier) sent(t *testing.T) []proto.IntentParams {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.intents) == 0 {
		t.Fatal("no intent reached the helper seam")
	}
	return append([]proto.IntentParams(nil), h.intents...)
}

func (h *heldIntentCarrier) AccessBump(ctx context.Context, sessionID string, above uint64) (uint64, error) {
	return h.inner.AccessBump(ctx, sessionID, above)
}

func (h *heldIntentCarrier) Snapshot(ctx context.Context, sessionID string) (proto.SnapshotResult, error) {
	return h.inner.Snapshot(ctx, sessionID)
}

func (h *heldIntentCarrier) Target(ctx context.Context, sessionID string, p proto.TargetParams) (proto.TargetResult, error) {
	return h.inner.Target(ctx, sessionID, p)
}

func (h *heldIntentCarrier) IntentStatus(ctx context.Context, sessionID, tokenID string) (proto.IntentStatusResult, error) {
	return h.inner.IntentStatus(ctx, sessionID, tokenID)
}

var _ paneHelpers = (*heldIntentCarrier)(nil)

// heldIntentStand is one real helper, one of its sessions, and the
// coordinator-side path session.keys spends that session's targets through.
type heldIntentStand struct {
	client  *helperclient.Client
	spawner *heldIntentSpawner
	carrier *heldIntentCarrier

	keys *paneKeys
	hub  *paneAccessHub
	// access is the capability the adapter would have bound before dispatch
	// (design §7.1) — resolved through the same Registrar session.keys'
	// own re-check uses.
	access *DescendantPaneAccess

	// sessionID is what the coordinator names this session by (its own
	// vocabulary, exactly as rec.SessionID carries it); the carrier is the
	// half that maps it to the helper that holds the pane and to that
	// helper's own HostSessionID, which is what paneScreen.owner does in
	// production.
	sessionID string
	hostID    helperclient.HostSessionID
	tokenID   string
	// epoch is the access epoch the snapshot this target was minted from
	// reported, i.e. the one the coordinator's own check observed.
	epoch uint64
}

// newHeldIntentStand builds the stand: a real helper session service on a real
// host peer, a real helperclient over a socketpair, a spawned session, and a
// target minted from that session's own first snapshot.
func newHeldIntentStand(t *testing.T) *heldIntentStand {
	t.Helper()
	ctx := context.Background()
	logger := discardLogger(t)
	const generation = "6q1uh19heldrev00000000000000000"

	spawner := &heldIntentSpawner{}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(generation),
		Spawner:    spawner,
		Log:        logger,
	})
	serverConn, clientConn := net.Pipe()
	peer := helperhost.New(serverConn, serverConn, generation, "instance-a", logger)
	peer.Register(svc)
	release := svc.Bind(peer)
	served := make(chan error, 1)
	go func() { served <- peer.Serve(ctx) }()
	t.Cleanup(func() {
		_ = clientConn.Close()
		// Bounded, because Service.Close waits for the owner goroutine to see
		// EOF (hostSession.stop / owner.stop): a fixture that fails to produce
		// one hangs here rather than failing, and this harness's own first
		// draft did exactly that — a test that had already recorded its
		// failure cost the PACKAGE its 10-minute timeout instead of five
		// seconds. The bound is a safety net against a genuine hang, never a
		// correctness assertion (watchdogBound's own note).
		closed := make(chan struct{})
		go func() {
			release()
			svc.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(watchdogBound):
			t.Error("the helper service never finished closing — an owner goroutine is still running")
		}
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("helper host: %v", err)
			}
		case <-time.After(watchdogBound):
			t.Error("the helper host never returned after the service closed")
		}
	})

	c, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:        helperclient.NewSocketConn(clientConn),
		ExpectHash:  generation,
		SentinelTTL: time.Second,
		Log:         logger,
	})
	if err != nil {
		t.Fatalf("helperclient.Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	entry, err := c.Spawn(ctx, proto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("spawn on the helper: %v", err)
	}
	hostID := entry.HostSessionID

	// The read path's own two calls, in the order paneReader.Read makes them
	// (session_targets.go): a consistent snapshot, then a target minted
	// against THAT snapshot id and no other.
	snap, err := c.Snapshot(ctx, hostID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	target, err := c.Target(ctx, proto.TargetParams{
		Session:    proto.HostSessionID{Generation: proto.GenerationID(hostID.Generation), Session: hostID.Session},
		SnapshotID: snap.SnapshotID,
		Kind:       string(sessionruntime.TargetInput),
		First:      0,
		Last:       0,
	})
	if err != nil {
		t.Fatalf("target: %v", err)
	}

	carrier := &heldIntentCarrier{
		inner:   helperPaneClient{client: c, id: hostID},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	registrar, _ := newGroupTwoCallersRecord()
	w, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-D", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	sessionID := string(w.Participant.ID)

	lookup := &fakeLookup{helpers: map[string]paneHelpers{sessionID: carrier}}
	// The production clock, not a fake: the commitBy this coordinator promises
	// is expressed in internal/monoclock's own units and the helper beside it
	// reads the SAME clock (owner.go's nowMono), which is §7.2's premise for a
	// local helper — a fake started at zero would put every intent in this
	// stand past its deadline before it was ever sent.
	hub := newPaneAccessHub(registrar, lookup, systemMonoClock{})
	hub.setEpoch(sessionID, snap.AccessEpoch)
	access := hub.Bind("sess-D", session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	reach, err := access.Resolve(ctx, sessionID, workers.EffectSendInput)
	if err != nil {
		t.Fatalf("resolve before revocation: %v", err)
	}
	// The record paneReader.Read keeps for a mint (session_targets.go): the
	// capability, the chain that proved reach, the epoch the SNAPSHOT carried,
	// and the mint's own token.
	reader := &fakeKeysReader{records: map[string]targetRecord{
		target.TokenID: {
			Access: access, SessionID: sessionID, Chain: reach.Chain, AccessEpoch: snap.AccessEpoch,
			View: assistant.TargetView{Token: target.Token, TokenID: target.TokenID, Kind: sessionruntime.TargetInput},
		},
	}}
	return &heldIntentStand{
		client: c, spawner: spawner, carrier: carrier,
		keys: newPaneKeys(reader, hub), hub: hub, access: access,
		sessionID: sessionID, hostID: hostID, tokenID: target.TokenID, epoch: snap.AccessEpoch,
	}
}

// keyOutcome is one session.keys call's answer, carried off its own goroutine
// so the test can run a revocation while the call is parked.
type keyOutcome struct {
	res assistant.KeysResult
	err error
}

// send starts one session.keys write on its own goroutine.
func (s *heldIntentStand) send(text string) <-chan keyOutcome {
	done := make(chan keyOutcome, 1)
	payload := text
	go func() {
		res, err := s.keys.Send(context.Background(), s.access, assistant.KeysRequest{
			SessionID: s.sessionID, TokenID: s.tokenID, Text: &payload,
		})
		done <- keyOutcome{res, err}
	}()
	return done
}

func (s *heldIntentStand) await(t *testing.T, done <-chan keyOutcome) keyOutcome {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-time.After(watchdogBound):
		t.Fatal("session.keys never returned")
		return keyOutcome{}
	}
}

// TestAnIntentHeldBeforeItsHelperCallIsRefusedByACompletedRevocation is spec
// §7.2's "revocation between authority check and commit" as an assertion: the
// intent passes its own authority check, is held before session.intent, the
// revocation runs to confirmation, and only then does the helper receive it —
// carrying the epoch its check observed, which the completed revocation has
// already superseded. The helper refuses it AT RECEIPT, and not one byte
// reaches the program.
func TestAnIntentHeldBeforeItsHelperCallIsRefusedByACompletedRevocation(t *testing.T) {
	stand := newHeldIntentStand(t)
	stand.carrier.hold = true

	done := stand.send("x")
	select {
	case <-stand.carrier.entered:
	case <-time.After(watchdogBound):
		t.Fatal("session.keys never reached the helper seam — its authority check did not pass, or admission was already closed")
	}
	// Parked BEFORE session.intent: nothing has reached the helper yet, and
	// what is about to be sent is the epoch its own check read out of the
	// target record, never a fresher one.
	if sent := stand.carrier.sent(t); len(sent) != 1 || sent[0].AccessEpoch != stand.epoch {
		t.Fatalf("intents at the seam = %+v, want exactly one carrying AccessEpoch=%d (the epoch its own check observed)", sent, stand.epoch)
	}

	// The revocation, run to its acknowledgement with the intent still held:
	// there is nothing queued on the helper for its sweep to find — the intent
	// has not even arrived — so the ack is immediate and waits on nothing.
	if res := stand.hub.revoke(context.Background(), []string{stand.sessionID}); res[stand.sessionID] != "ack" {
		t.Fatalf("revoke result = %v, want ack (nothing was queued behind an intent that had not arrived)", res)
	}
	if got := stand.hub.aboveFor(stand.sessionID); got != stand.epoch+1 {
		t.Fatalf("epoch in force after the ack = %d, want %d", got, stand.epoch+1)
	}

	// Only now is the intent sent — on the epoch its check observed.
	close(stand.carrier.release)
	out := stand.await(t, done)
	if out.err != nil {
		t.Fatalf("Send: %v", out.err)
	}
	if out.res.State != "refused" || out.res.Refusal == nil || out.res.Refusal.Cause != "access_revoked" {
		t.Fatalf("Send result = %+v, want refused/access_revoked", out.res)
	}
	if out.res.BytesWritten != 0 {
		t.Fatalf("BytesWritten = %d, want 0", out.res.BytesWritten)
	}
	// Zero bytes on the pane's write path, read off the writer itself.
	if got := stand.spawner.only(t).written(); len(got) != 0 {
		t.Fatalf("the program was written to despite the refusal: %q", got)
	}

	// And the refusal happened IN THE HELPER, at receipt: its own record of
	// the token carries the terminal outcome. A refusal decided coordinator-
	// side (admitting, StillHolds) leaves the token unknown here, so this is
	// what separates "the receipt gate refused it" from "the coordinator never
	// asked". State is the RECORDED spelling (hostSession.intentStatus
	// answers r.State), and RegionOmitted is what marks the answer as coming
	// from that record rather than from a fresh attempt re-reading a screen.
	status, err := stand.client.IntentStatus(context.Background(), stand.hostID, stand.tokenID)
	if err != nil {
		t.Fatalf("intent status: %v", err)
	}
	if status.State != "refused" || status.Result == nil || status.Result.State != "refused" ||
		status.Result.Refusal == nil || status.Result.Refusal.Cause != "access_revoked" ||
		!status.Result.Refusal.RegionOmitted {
		t.Fatalf("the helper's own record of the token = %+v, want the terminal refused/access_revoked it decided at receipt", status)
	}
}

// TestAnIntentAlreadyReceivedByTheHelperExecutesAndALaterRevocationAcksAnyway
// is the pair the schedule above is measured against, over the same real
// helper: the identical call whose intent is NOT held reaches the helper while
// its epoch is still current, so the same gate lets it through and the write
// lands. The revocation that follows finds nothing queued — the intent already
// executed — and acks. Together the two are one schedule with one ordering
// changed, which is what makes the epoch, and not the token or the session,
// the whole of the difference between refused and executed.
func TestAnIntentAlreadyReceivedByTheHelperExecutesAndALaterRevocationAcksAnyway(t *testing.T) {
	stand := newHeldIntentStand(t)

	out := stand.await(t, stand.send("x"))
	if out.err != nil {
		t.Fatalf("Send: %v", out.err)
	}
	if out.res.State != "executed" || out.res.BytesWritten != 1 {
		t.Fatalf("Send result = %+v, want executed with 1 byte", out.res)
	}
	if got := stand.carrier.sent(t); len(got) != 1 || got[0].AccessEpoch != stand.epoch {
		t.Fatalf("intents at the seam = %+v, want exactly one carrying AccessEpoch=%d (the epoch its check observed)", got, stand.epoch)
	}
	if got := stand.spawner.only(t).written(); len(got) != 1 || string(got[0]) != "x" {
		t.Fatalf("the program was written %q, want exactly one write of the intent's own payload", got)
	}

	if res := stand.hub.revoke(context.Background(), []string{stand.sessionID}); res[stand.sessionID] != "ack" {
		t.Fatalf("revoke result = %v, want ack (the intent it would have swept had already executed)", res)
	}
	if got := stand.spawner.only(t).written(); len(got) != 1 {
		t.Fatalf("the revocation itself wrote to the program: %q", got)
	}
}
