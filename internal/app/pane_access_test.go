package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/workers"
)

// ── fakes for paneHelpers / paneHelperLookup / monotonicClock ──────────────

// fakeHelper is a paneHelpers whose AccessBump can be told to block forever
// (never), to block until released, or to answer immediately — the three
// shapes revoke's wait has to handle.
type fakeHelper struct {
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{} // nil and never==false: answers immediately
	never       bool          // blocks on ctx.Done() only

	mu    sync.Mutex
	calls []uint64
	epoch uint64
	err   error
}

func (h *fakeHelper) AccessBump(ctx context.Context, sessionID string, above uint64) (uint64, error) {
	h.mu.Lock()
	h.calls = append(h.calls, above)
	h.mu.Unlock()
	if h.entered != nil {
		h.enteredOnce.Do(func() { close(h.entered) })
	}
	if h.never {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if h.release != nil {
		select {
		case <-h.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return h.epoch, h.err
}

// Snapshot and Target are not exercised by revoke's own tests (they cover
// AccessBump alone); these satisfy paneHelpers so fakeHelper stays the one
// fake for revocation's tests without a second, unused implementation.
func (h *fakeHelper) Snapshot(context.Context, string) (proto.SnapshotResult, error) {
	return proto.SnapshotResult{}, errors.New("fakeHelper: Snapshot not configured")
}

func (h *fakeHelper) Target(context.Context, string, proto.TargetParams) (proto.TargetResult, error) {
	return proto.TargetResult{}, errors.New("fakeHelper: Target not configured")
}

func (h *fakeHelper) Intent(context.Context, string, proto.IntentParams) (proto.IntentResult, error) {
	return proto.IntentResult{}, errors.New("fakeHelper: Intent not configured")
}

func (h *fakeHelper) IntentStatus(context.Context, string, string) (proto.IntentStatusResult, error) {
	return proto.IntentStatusResult{}, errors.New("fakeHelper: IntentStatus not configured")
}

// fakeLookup answers HelperFor from a fixed table, for a session whose
// helper is already known to the test rather than discovered.
type fakeLookup struct {
	mu      sync.Mutex
	helpers map[string]paneHelpers
}

func (l *fakeLookup) HelperFor(_ context.Context, sessionID string) (paneHelpers, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.helpers[sessionID]
	return h, ok
}

// fakePaneClock is a monotonicClock a test moves by hand — the "wait on an
// observable state change, never on a duration" rule applied to §7.2's
// commitBy wait: a test drives "the deadline has passed" as an explicit
// fact instead of a real elapsed interval.
type fakePaneClock struct {
	mu  sync.Mutex
	now Nanos
}

func (c *fakePaneClock) Now() Nanos {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakePaneClock) advance(to Nanos) {
	c.mu.Lock()
	c.now = to
	c.mu.Unlock()
}

// watch is the shared "did this finish yet" watchdog pattern already used in
// this codebase (internal/workers/wait_close_test.go): a generous safety-net
// bound against a genuine hang, never a correctness assertion about how fast
// something ran.
const watchdogBound = 5 * time.Second

// ── §7.1: binding ────────────────────────────────────────────────────────

// The endpoint adapter binds BOTH the admitted session's own incarnation and
// a DescendantPaneAccess, before dispatch, from the session it admitted —
// never inferred from a call's own parameters (design §7.1).
func TestAdmitBindsTheSessionsOwnIdentityAndPaneAccess(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Watch(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	root := peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)}
	pinner := &workerAuthPinner{root: root, member: map[int]bool{9001: true}}
	auth, err := newToolAuthorizer(pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace, allowWorkerApproval{})
	if err != nil {
		t.Fatalf("new tool authorizer: %v", err)
	}
	registrar, _ := newGroupTwoCallersRecord()
	hub := newPaneAccessHub(registrar, nil, nil)
	auth.BindPaneAccess(hub)

	inv, _, admitErr := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001}, publishForTest)
	if admitErr != nil {
		t.Fatalf("admit: %v", admitErr)
	}
	if inv.RunContext.ControllerIdentity != sess.Identity() {
		t.Fatalf("controller identity = %+v, want %+v", inv.RunContext.ControllerIdentity, sess.Identity())
	}
	access, ok := inv.RunContext.PaneAccess.(*DescendantPaneAccess)
	if !ok || access == nil {
		t.Fatalf("PaneAccess = %#v, want a bound *DescendantPaneAccess", inv.RunContext.PaneAccess)
	}
	if access.Controller() != string(sess.ID()) {
		t.Fatalf("bound controller = %q, want %q", access.Controller(), sess.ID())
	}
	if access.Identity() != sess.Identity() {
		t.Fatalf("bound identity = %+v, want %+v", access.Identity(), sess.Identity())
	}
	ep, ok := access.Authority().(EndpointAuthority)
	if !ok || ep.AdmissionEpoch == 0 {
		t.Fatalf("authority = %+v, want an EndpointAuthority with a nonzero admission epoch", access.Authority())
	}
}

// An authorizer built with no BindPaneAccess call — the state of a backend
// before Task 5/8 exist — leaves PaneAccess nil rather than panicking or
// binding something unusable. Nil is the safe answer every reader of it
// must already treat as "no descendant authority granted".
func TestAdmitLeavesPaneAccessNilWithoutABoundHub(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4243
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Watch(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	root := peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)}
	pinner := &workerAuthPinner{root: root, member: map[int]bool{9002: true}}
	auth := mustToolAuthorizer(t, pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace, allowWorkerApproval{})

	inv, _, err := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9002}, publishForTest)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if inv.RunContext.PaneAccess != nil {
		t.Fatalf("PaneAccess = %#v, want nil with no hub bound", inv.RunContext.PaneAccess)
	}
	if inv.RunContext.ControllerIdentity != sess.Identity() {
		t.Fatalf("controller identity = %+v, want %+v (identity travels independently of PaneAccess)",
			inv.RunContext.ControllerIdentity, sess.Identity())
	}
}

// ── §7.2: controller admission retired ──────────────────────────────────

// retire is one of §7.2's closed trigger set: ending the interval a
// session's answer carried must also end that session's authority over
// every descendant reachable through it, not merely the connection.
func TestRetiringAnAdmissionRevokesTheControllersDescendants(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	w1, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-C", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register w1: %v", err)
	}
	w1Session := string(w1.Participant.ID)
	w2, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: w1Session, Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register w2: %v", err)
	}
	w2Session := string(w2.Participant.ID)

	if _, resolveErr := registrar.Resolve(ctx, "sess-C", w2Session, workers.EffectObserve); resolveErr != nil {
		t.Fatalf("resolve before retirement: %v", resolveErr)
	}

	auth, err := newToolAuthorizer(nil, nil, nil, nil, workerTestWorkspace, allowWorkerApproval{})
	if err != nil {
		t.Fatalf("new tool authorizer: %v", err)
	}
	auth.BindRevoker(registrar)
	auth.retire(session.ID("sess-C"), toolendpoint.AdmissionEpoch(1))

	if _, err := registrar.Resolve(ctx, "sess-C", w1Session, workers.EffectObserve); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("resolve w1 after retirement: err = %v, want ErrNotReachable", err)
	}
	if _, err := registrar.Resolve(ctx, "sess-C", w2Session, workers.EffectObserve); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("resolve w2 after retirement: err = %v, want ErrNotReachable", err)
	}
}

// retire with no revoker wired (a backend that never called BindRevoker) is
// a no-op rather than a panic — paired with the case above the way a
// failure path pairs with its ordinary success.
func TestRetireWithNoRevokerBoundDoesNothing(t *testing.T) {
	auth, err := newToolAuthorizer(nil, nil, nil, nil, workerTestWorkspace, allowWorkerApproval{})
	if err != nil {
		t.Fatalf("new tool authorizer: %v", err)
	}
	auth.retire(session.ID("sess-C"), toolendpoint.AdmissionEpoch(1))
}

// ── DescendantPaneAccess.Resolve is a thin, correctly-wired passthrough ───

func TestDescendantPaneAccessResolvesThroughTheBoundRegistrar(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	w1, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-C", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	hub := newPaneAccessHub(registrar, nil, nil)
	access := hub.Bind("sess-C", session.Identity{InstanceID: "backend-A", Epoch: 1},
		EndpointAuthority{AdmissionEpoch: 7})

	reach, err := access.Resolve(ctx, string(w1.Participant.ID), workers.EffectObserve)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if reach.SessionID != string(w1.Participant.ID) {
		t.Fatalf("reach session = %q, want %q", reach.SessionID, w1.Participant.ID)
	}
	if _, err := access.Resolve(ctx, "sess-nonexistent", workers.EffectObserve); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("resolve a session nothing runs in: err = %v, want ErrNotReachable", err)
	}
}

// A capability with no hub behind it (the zero value, or one built before a
// registrar existed) answers ErrNotReachable rather than panicking.
func TestDescendantPaneAccessNilSafety(t *testing.T) {
	var access *DescendantPaneAccess
	if _, err := access.Resolve(context.Background(), "sess-x", workers.EffectObserve); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("nil access resolve: err = %v, want ErrNotReachable", err)
	}
	empty := &DescendantPaneAccess{}
	if _, err := empty.Resolve(context.Background(), "sess-x", workers.EffectObserve); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("unbound access resolve: err = %v, want ErrNotReachable", err)
	}
}

// ── revoke: the helper-epoch half ──────────────────────────────────────

// revoke reports ConfirmedBy "ack" only once the helper's own AccessBump
// call has returned — modelling §7.2's "acknowledges only after every older
// uncommitted intent is terminal" without session.intent existing yet
// (Task 9): the fake helper's own entered/release gate stands in for the
// in-flight intent this design describes.
func TestRevokeWaitsForTheHelpersAcknowledgementBeforeReportingAck(t *testing.T) {
	release := make(chan struct{})
	helper := &fakeHelper{entered: make(chan struct{}), release: release, epoch: 5}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{"sess-w1": helper}}
	hub := newPaneAccessHub(nil, lookup, nil)

	doneCh := make(chan map[string]string, 1)
	go func() { doneCh <- hub.revoke(context.Background(), []string{"sess-w1"}) }()

	select {
	case <-helper.entered:
	case <-time.After(watchdogBound):
		t.Fatal("revoke never reached the helper")
	}
	// Deterministic, not a race: revoke cannot have returned yet, because
	// its only paths to returning are the helper answering (blocked, on
	// h.release) or a commitBy deadline (none set for this session).
	select {
	case res := <-doneCh:
		t.Fatalf("revoke reported %v before the helper acknowledged", res)
	default:
	}
	close(release)
	select {
	case res := <-doneCh:
		if res["sess-w1"] != "ack" {
			t.Fatalf("confirmedBy = %v, want ack", res)
		}
	case <-time.After(watchdogBound):
		t.Fatal("revoke did not return after the helper acknowledged")
	}
}

// A helper that never answers is waited out by the LATEST commitBy this
// coordinator recorded for that session, on the injected clock — never on
// real elapsed time (design §7.2).
func TestAHelperThatDoesNotAnswerIsWaitedOutByCommitBy(t *testing.T) {
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
	select {
	case res := <-doneCh:
		t.Fatalf("revoke reported %v before the clock passed commitBy", res)
	default:
	}
	clock.advance(2500)
	select {
	case res := <-doneCh:
		if res["sess-w1"] != "deadline" {
			t.Fatalf("confirmedBy = %v, want deadline", res)
		}
	case <-time.After(watchdogBound):
		t.Fatal("revoke did not resolve after the clock passed commitBy")
	}
}

// Paired successes/failures for revokeOne's non-deadline path: a session
// nothing holds any more needs no bump at all, and a helper that answers
// with an error is treated the same as one that never answered.
func TestRevokeIsAckImmediatelyWhenNoHelperHoldsTheSession(t *testing.T) {
	lookup := &fakeLookup{helpers: map[string]paneHelpers{}}
	hub := newPaneAccessHub(nil, lookup, nil)
	res := hub.revoke(context.Background(), []string{"sess-gone"})
	if res["sess-gone"] != "ack" {
		t.Fatalf("confirmedBy = %v, want ack for a session nothing holds", res)
	}
}

func TestRevokeReportsDeadlineWhenTheHelperErrors(t *testing.T) {
	helper := &fakeHelper{entered: make(chan struct{}), err: errors.New("bump refused")}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{"sess-w1": helper}}
	hub := newPaneAccessHub(nil, lookup, nil)
	res := hub.revoke(context.Background(), []string{"sess-w1"})
	if res["sess-w1"] != "deadline" {
		t.Fatalf("confirmedBy = %v, want deadline when the helper's bump errors", res)
	}
}

// No coordinator lock is held during a helper call: an outstanding revoke
// against one session's helper (blocked forever) must not delay Resolve for
// an unrelated session on the same registrar.
func TestNoRegistrarLockIsHeldAcrossAHelperCall(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	w1, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-C", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register w1: %v", err)
	}
	w1Session := string(w1.Participant.ID)
	w2, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-C", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register w2: %v", err)
	}
	w2Session := string(w2.Participant.ID)

	helper := &fakeHelper{entered: make(chan struct{}), never: true}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{w1Session: helper}}
	hub := newPaneAccessHub(registrar, lookup, nil)

	blockedCtx, cancel := context.WithCancel(ctx)
	defer cancel() // releases the leaked goroutine once the test ends
	go hub.revoke(blockedCtx, []string{w1Session})

	select {
	case <-helper.entered:
	case <-time.After(watchdogBound):
		t.Fatal("revoke never reached the helper")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := registrar.Resolve(ctx, "sess-C", w2Session, workers.EffectObserve); err != nil {
			t.Errorf("resolve during an outstanding helper call: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(watchdogBound):
		t.Fatal("resolve blocked while a helper call was outstanding — a coordinator lock is held across it")
	}
}

// admitting is false exactly while a bump is outstanding for a session, and
// true again once it resolves — Task 9's gate for whether a new intent may
// be admitted (design §7.2: "until a session's bump is acknowledged ... the
// coordinator admits no new intent there").
func TestAdmittingReflectsWhetherABumpIsOutstanding(t *testing.T) {
	hub := newPaneAccessHub(nil, nil, nil)
	if !hub.admitting("sess-w1") {
		t.Fatal("a session with no outstanding bump should admit")
	}
	hub.markPending("sess-w1", true)
	if hub.admitting("sess-w1") {
		t.Fatal("admitting stayed true while a bump was outstanding")
	}
	hub.markPending("sess-w1", false)
	if !hub.admitting("sess-w1") {
		t.Fatal("admitting did not return to true once the bump resolved")
	}
}

// noteCommitBy keeps the LATEST deadline recorded for a session — an older
// one arriving after a later one must not shorten the wait §7.2 promises.
func TestNoteCommitByKeepsTheLatestDeadline(t *testing.T) {
	hub := newPaneAccessHub(nil, nil, nil)
	hub.noteCommitBy("sess-w1", 2000)
	hub.noteCommitBy("sess-w1", 1000) // older, must not move the deadline back
	got, ok := hub.commitByFor("sess-w1")
	if !ok || got != 2000 {
		t.Fatalf("commitBy = %v, %v, want 2000, true", got, ok)
	}
	hub.noteCommitBy("sess-w1", 3000) // newer, must move it forward
	got, ok = hub.commitByFor("sess-w1")
	if !ok || got != 3000 {
		t.Fatalf("commitBy = %v, %v, want 3000, true", got, ok)
	}
}
