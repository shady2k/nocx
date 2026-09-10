package app

// nocx-ui8q6.7: a registration that fails at delegation, at mark-live, or at
// attach-supervision — i.e. AFTER the participant's enrolment has already
// arrived — used to leave its tab standing. internal/workers.Registrar's own
// compensate() reaches the undo through the Spawned interface's Kill, and
// Kill used to close only the session; the tab, minted by workerSpawner.Spawn
// and known only to it, was left for nobody to remove.
//
// These tests exercise Registrar.Register end to end, with a real
// workerSpawner (minting real tab ids through a recording double and real
// sessions through a real registry) behind a Store and a Supervisor that can
// be told to fail one late step on demand. That is a step further than
// worker_spawn_axis_test.go's own regression tests
// (TestARefusedFirstLineNowDeletesTheOrphanTab and
// TestASessionOpenFailureDeletesTheOrphanTab), which exercise Spawn directly
// and never construct a Registrar: those cover the EARLY failures, before a
// Spawned is ever handed back. These cover the LATE ones, where Kill and not
// compensateSpawn is what has to do the undoing.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// failableStore wraps a real workers.Store and lets a test refuse one write
// call on demand; every other method passes through to the embedded store
// unchanged, which is what lets Register's other five steps run for real.
type failableStore struct {
	workers.Store
	mu             sync.Mutex
	failDelegation bool
	failMarkLive   bool
	// beforeMarkLiveFailure, when set, runs synchronously the instant a
	// failMarkLive injection is about to return its error — before
	// Registrar.compensate is ever called. A test uses it to cancel the SAME
	// ctx Register is holding at that exact point, so what compensate is
	// handed is provably the caller's own already-done context (nocx-4gj5w)
	// rather than one raced against a background goroutine on a guess about
	// timing.
	beforeMarkLiveFailure func()
}

func (f *failableStore) PutDelegation(ctx context.Context, d workers.Delegation) error {
	f.mu.Lock()
	fail := f.failDelegation
	f.mu.Unlock()
	if fail {
		return errors.New("injected: the delegation store refused this write")
	}
	return f.Store.PutDelegation(ctx, d)
}

func (f *failableStore) MarkLive(ctx context.Context, id workers.ParticipantID, l workers.Liveness) error {
	f.mu.Lock()
	fail := f.failMarkLive
	hook := f.beforeMarkLiveFailure
	f.mu.Unlock()
	if fail {
		if hook != nil {
			hook()
		}
		return errors.New("injected: the mark-live store refused this write")
	}
	return f.Store.MarkLive(ctx, id, l)
}

// failableSupervisor wraps a real workers.Supervisor and lets a test refuse
// Attach on demand.
type failableSupervisor struct {
	workers.Supervisor
	fail bool
}

func (f *failableSupervisor) Attach(ctx context.Context, p workers.Participant) error {
	if f.fail {
		return errors.New("injected: supervision refused to attach")
	}
	return f.Supervisor.Attach(ctx, p)
}

// recordingTabs is the paneMinter double every test stand in this file wires
// in: whatever else it does, it must record every CreateTab and DeleteTab
// call so a test can assert the one created tab was the one deleted.
// fakeAxisTabs (worker_spawn_axis_test.go) is the ordinary one; ctxSpyTabs
// below additionally answers the question fakeAxisTabs cannot, because it
// never looks at ctx at all — was DeleteTab's own context already done.
type recordingTabs interface {
	paneMinter
	snapshot() (created, deleted []string)
}

// ctxSpyTabs records, for every DeleteTab call, whether the context it was
// handed was ALREADY done. That is exactly what distinguishes Kill's own
// derived compensation context (nocx-4gj5w, workers.go's killContext) from a
// caller's context that expired before compensation ran — fakeAxisTabs
// cannot tell the two apart because it ignores ctx entirely.
type ctxSpyTabs struct {
	mu            sync.Mutex
	created       []string
	deleted       []string
	deleteCtxDone []bool
}

func (f *ctxSpyTabs) CreateTab(_ context.Context, tab content.Tab, _ content.Pane) (content.Created[content.NewTab], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, tab.ID)
	return content.Created[content.NewTab]{}, nil
}

func (f *ctxSpyTabs) DeleteTab(ctx context.Context, id string, _ content.Replacement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	f.deleteCtxDone = append(f.deleteCtxDone, ctx.Err() != nil)
	return nil
}

func (f *ctxSpyTabs) snapshot() (created, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...), append([]string(nil), f.deleted...)
}

// lateFailureStand is the composition these tests assert against: a real
// workerSpawner over a recording tab double and a real session registry,
// behind a Registrar whose store and supervisor can be told to fail one late
// step on demand.
type lateFailureStand struct {
	reg    *session.Reg
	tabs   recordingTabs
	enrol  *workerEnrolments
	store  *failableStore
	sup    *failableSupervisor
	record *workers.Registrar
}

// newLateFailureStandWithTabs is newLateFailureStand parameterised on the
// tab double, so a test that needs to inspect DeleteTab's own ctx (the
// cancelled-context case below) can swap in ctxSpyTabs without duplicating
// the rest of the wiring.
func newLateFailureStandWithTabs(t *testing.T, tabs recordingTabs) *lateFailureStand {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
	})
	opener := &fakeAxisOpener{reg: reg}
	enrol := newWorkerEnrolments(logger, reg)
	spawner := &workerSpawner{
		layout: tabs, opener: opener, sessions: reg,
		enrolments: enrol, workspace: "ws-test", log: logger,
	}
	store := &failableStore{Store: workers.NewMemoryStore()}
	sup := &failableSupervisor{Supervisor: &workerSupervisor{sessions: reg, log: logger}}
	record := workers.NewRegistrar(store, spawner, enrol, sup,
		workers.WithEnrolmentDeadline(2*time.Second),
		workers.WithCloser(&workerCloser{sessions: reg, log: logger}),
	)
	return &lateFailureStand{reg: reg, tabs: tabs, enrol: enrol, store: store, sup: sup, record: record}
}

func newLateFailureStand(t *testing.T) *lateFailureStand {
	t.Helper()
	return newLateFailureStandWithTabs(t, &fakeAxisTabs{})
}

// register runs a registration and supplies the enrolment as soon as the
// session exists — worker_test.go's registerWithEnrolment, against this
// file's own (differently composed) stand rather than *workerStand.
func (s *lateFailureStand) register(t *testing.T) (workers.Participant, error) {
	t.Helper()
	ctx := context.Background()
	type outcome struct {
		p   workers.Participant
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		p, err := s.record.Register(ctx, workers.RegisterRequest{
			Group: "worker-1", CoordinatorSession: "sess-coordinator",
			Role: workers.RoleWorker, Task: "t", Command: "claude",
		})
		done <- outcome{p, err}
	}()
	var sid session.ID
	waittest.WaitFor(t, "the participant's session to exist", func() bool {
		for _, sess := range s.reg.List() {
			sid = sess.ID()
			return true
		}
		return false
	})
	s.enrol.enrolled(sid, "lane-participant")
	got := <-done
	return got.p, got.err
}

// assertFullyUndone is the shared assertion: the tab nocx-ui8q6.4 already
// covers for the early failures, the session Kill used to leave standing for
// the late ones, and the participant left with nothing addressable.
func (s *lateFailureStand) assertFullyUndone(t *testing.T, p workers.Participant) {
	t.Helper()
	created, deleted := s.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 1 || created[0] != deleted[0] {
		t.Fatalf("tabs created=%v deleted=%v, want the one created tab deleted and nothing else", created, deleted)
	}
	waittest.WaitFor(t, "the participant's session to be closed", func() bool {
		return len(s.reg.List()) == 0
	})
	stored, err := s.store.Participant(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("read back the participant: %v", err)
	}
	if !stored.State.Terminal() {
		t.Fatalf("participant state = %q, want terminal (nothing left addressable)", stored.State)
	}
}

// Criterion: a refusal at PutDelegation — reached only after the enrolment
// has already arrived — leaves no tab, no session and no live participant.
func TestARegistrationRefusedAtDelegationLeavesNoTabNoSessionNoParticipant(t *testing.T) {
	stand := newLateFailureStand(t)
	stand.store.failDelegation = true

	p, err := stand.register(t)
	if err == nil || !strings.Contains(err.Error(), "delegation") {
		t.Fatalf("register err = %v, want a delegation failure", err)
	}
	stand.assertFullyUndone(t, p)
}

// Criterion: a refusal at MarkLive — one step later, delegation already
// committed — leaves the same nothing behind.
func TestARegistrationRefusedAtMarkLiveLeavesNoTabNoSessionNoParticipant(t *testing.T) {
	stand := newLateFailureStand(t)
	stand.store.failMarkLive = true

	p, err := stand.register(t)
	if err == nil || !strings.Contains(err.Error(), "mark live") {
		t.Fatalf("register err = %v, want a mark-live failure", err)
	}
	stand.assertFullyUndone(t, p)
}

// Criterion: this is the assertion nocx-4gj5w exists for. Registrar.compensate
// hands Kill the OUTER ctx Register was called with (registrar.go's own
// compensate), and on the live stand this bead was filed from that ctx was
// already context.DeadlineExceeded by the time a late failure reached it —
// awaitFreeText had spent the whole spawn budget waiting, so the ctx
// compensateSpawn (and, one level up, Registrar.compensate) received was
// already dead, and DeleteTab failed with exactly that error, leaving the
// tab and the participant behind for a person and a coordinator to clean up
// by hand.
//
// This reproduces the shape directly: the ctx Register is given is
// cancelled from INSIDE the injected mark-live failure, synchronously,
// before compensate ever runs — not raced against a background goroutine on
// a guess about timing. ctxSpyTabs then proves DeleteTab was handed a
// context that was NOT already done, which is what Kill's own derived
// compensation context (killContext, workers.go) guarantees regardless of
// what the caller's ctx was doing.
func TestARegistrationRefusedAtMarkLiveCompensatesEvenWithAnAlreadyCancelledContext(t *testing.T) {
	tabs := &ctxSpyTabs{}
	stand := newLateFailureStandWithTabs(t, tabs)
	stand.store.failMarkLive = true

	ctx, cancel := context.WithCancel(context.Background())
	stand.store.beforeMarkLiveFailure = cancel

	type outcome struct {
		p   workers.Participant
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		p, err := stand.record.Register(ctx, workers.RegisterRequest{
			Group: "worker-1", CoordinatorSession: "sess-coordinator",
			Role: workers.RoleWorker, Task: "t", Command: "claude",
		})
		done <- outcome{p, err}
	}()
	var sid session.ID
	waittest.WaitFor(t, "the participant's session to exist", func() bool {
		for _, sess := range stand.reg.List() {
			sid = sess.ID()
			return true
		}
		return false
	})
	stand.enrol.enrolled(sid, "lane-participant")
	got := <-done

	if got.err == nil || !strings.Contains(got.err.Error(), "mark live") {
		t.Fatalf("register err = %v, want a mark-live failure", got.err)
	}
	if ctx.Err() == nil {
		t.Fatal("test setup: ctx should already be cancelled by the time Register returned")
	}
	stand.assertFullyUndone(t, got.p)

	_, deleted := tabs.snapshot()
	if len(deleted) != 1 {
		t.Fatalf("DeleteTab calls = %v, want exactly one", deleted)
	}
	if tabs.deleteCtxDone[0] {
		t.Fatal("DeleteTab was handed a context that was already done; " +
			"Kill's own compensation context must not inherit the caller's cancelled one")
	}
}

// Criterion: a refusal at Supervisor.Attach — the very last step, with the
// record already marked live — leaves the same nothing behind. This is the
// failure the bug report names directly: before nocx-ui8q6.7, Kill closed
// only the session here and the tab was the one thing that survived.
func TestARegistrationRefusedAtAttachSupervisionLeavesNoTabNoSessionNoParticipant(t *testing.T) {
	stand := newLateFailureStand(t)
	stand.sup.fail = true

	p, err := stand.register(t)
	if err == nil || !strings.Contains(err.Error(), "attach supervision") {
		t.Fatalf("register err = %v, want an attach-supervision failure", err)
	}
	stand.assertFullyUndone(t, p)
}

// Kill's own doc names this property: compensateSpawn may already have run
// for an early failure, and a compensation that itself fails is retried by
// Registrar.compensate's own contract — so Kill has to tolerate being asked
// to undo a spawn a second time without turning that into a fresh error.
// This drives it directly rather than through Register, because Register's
// own compensate() only ever calls Kill once per attempt; a second call is a
// property of Kill itself, not of one registration's ordinary path.
func TestKillIsIdempotent(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
	})
	tabs := &fakeAxisTabs{}
	if _, err := tabs.CreateTab(context.Background(),
		content.Tab{ID: "tab-idem"}, content.Pane{ID: "pane-idem"}); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	sess, err := reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: "pane-idem",
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	sp := spawnedParticipant{tabID: "tab-idem", sess: sess, sessions: reg, layout: tabs}
	if err := sp.Kill(context.Background()); err != nil {
		t.Fatalf("first Kill: %v", err)
	}
	if err := sp.Kill(context.Background()); err != nil {
		t.Fatalf("second Kill (idempotency) reported an error: %v", err)
	}
	if _, getErr := reg.Get(sess.ID()); getErr == nil {
		t.Fatal("the session is still in the registry after Kill")
	}
}
