package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
	"github.com/shady2k/nocx/internal/workspace"
)

// workerTestPTYFactory gives every participant a stub PTY, so the test drives
// the real session registry rather than a double of it: what is being asserted
// is that a real session's exit reaches the record, and a fake session would
// assert only that the fake was called.
type workerTestPTYFactory struct {
	log  log.Logger
	mu   sync.Mutex
	made []*recordingPTY
}

// last is the pty of the most recently opened session, which in these tests is
// the one the caller just opened.
func (f *workerTestPTYFactory) last() *recordingPTY {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.made) == 0 {
		return nil
	}
	return f.made[len(f.made)-1]
}

// A FRESH pty per session, not one shared: a pty is what a session IS, so a
// shared one would make closing any participant's session close every other
// participant's too, and the exit assertions below would pass for the wrong
// reason.
//
// It RECORDS what is written to it, because one thing the product does is
// write into a pane a person is not looking at — the participant's first
// command line, and the wake that starts an idle coordinator's turn — and a
// stub that discarded them could only be asserted against by asking the code
// what it believed it had done.
func (f *workerTestPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	p := &recordingPTY{Stub: pty.NewStub(f.log)}
	f.mu.Lock()
	f.made = append(f.made, p)
	f.mu.Unlock()
	return p, nil
}

// recordingPTY is a pty.Stub that keeps what was written to it.
type recordingPTY struct {
	*pty.Stub
	mu      sync.Mutex
	written []byte
}

func (p *recordingPTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.written = append(p.written, b...)
	p.mu.Unlock()
	return p.Stub.Write(b)
}

func (p *recordingPTY) read() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.written)
}

// workerStand is the composition this file asserts: the real record, the real
// content store behind the layout chain, the real session registry, the real
// session opener, and the two adapters that carry the facts.
type workerStand struct {
	db          content.ContentDB
	workerStore workers.Store
	// workerIDs is every worker this stand opened. The record answers "what is
	// still open" per WORKER — there is no record-wide read, because nothing
	// in the product asks that question — so teardown has to know which
	// workerStore it made. ensureWorker is the one place a worker is opened, which is
	// what keeps the list from drifting from the record.
	workerIDs []workers.ID
	dir       string
	ptys      *workerTestPTYFactory
	tp        *transport.WSServer
	reg       *session.Reg
	enrol     *workerEnrolments
	lanes     *sessionRegistry
	record    *workers.Registrar
	// The pieces record was built from, kept so a test can build a SECOND
	// record over the same seams with one of them swapped — the shape a test
	// needs to make a single half of a close fail while everything else is
	// the product's (nocx-xn63t.4.6).
	spawner *workerSpawner
	sup     *workerSupervisor
	tabs    *workerTabs
	log     log.Logger
	mu      sync.Mutex
}

func newWorkerStand(t *testing.T, opts ...workers.Option) *workerStand {
	t.Helper()
	ctx := context.Background()
	logger := log.NewSlogAdapter(nil)

	// Declared before the cleanup below, which reads the workerStore it opened;
	// the fields are filled in at the end of this function.
	stand := &workerStand{}

	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// The record, empty: it holds this backend's own participants and they
	// die with it, so there is nothing to load and nothing to sweep.
	workerStore := workers.NewMemoryStore()

	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	tp := transport.NewWSServer(logger, reg)
	// Registered AFTER the store's cleanup so it runs BEFORE it: a session
	// torn down by the harness reports its exit on its own goroutine, and a
	// store already closed underneath that report turns an ordinary teardown
	// into a log line that reads like a defect.
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
		// The condition is the RECORD settling, not the registry emptying:
		// Close returns before the supervisor's watcher has seen Done and
		// reported the exit, so waiting on the registry would still leave a
		// write racing the store's own teardown.
		waittest.WaitFor(t, "every participant to reach a terminal state", func() bool {
			for _, id := range stand.openedWorkers() {
				open, err := workerStore.NonTerminal(ctx, id)
				if err == nil && len(open) > 0 {
					return false
				}
			}
			return true
		})
		_ = tp.Stop(ctx)
	})

	enrol := newWorkerEnrolments(logger, reg)
	lanes := newSessionRegistry()
	sup := &workerSupervisor{sessions: reg, log: logger}
	tabs := newWorkerTabs()
	spawner := &workerSpawner{
		layout: db.Layout(), opener: tp, sessions: reg,
		enrolments: enrol, workspace: string(workspace.Default),
		// announce is the product's own push to a connected renderer
		// (nocx-ui8q6.3), and its tab-close counterpart below is the same
		// server: a stand that wired a recording double instead would prove
		// nothing about the frame a window actually reads.
		announce: tp,
		tabs:     tabs,
		log:      logger,
	}
	record := workers.NewRegistrar(
		workerStore,
		spawner,
		enrol, sup,
		append([]workers.Option{
			// Short, because every test here supplies the enrolment itself or
			// deliberately withholds it; the number bounds the withheld case
			// and decides nothing about the others.
			workers.WithEnrolmentDeadline(2 * time.Second),
			// The product's closer, over the real registry and the real
			// layout: a close here has to end a real session, take the
			// participant's tab out of the window, and let the exit reach the
			// record by the ordinary path — which is what makes "close writes
			// no state in the RECORD" checkable (nocx-xn63t.4.6 added the
			// tab half; the record is still not this closer's to write).
			workers.WithCloser(&workerCloser{
				sessions: reg, layout: db.Layout(), tabs: tabs, announce: tp, log: logger,
			}),
		}, opts...)...,
	)
	sup.exited = func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, e workers.Exit) {
		if _, err := record.Exited(ctx, id, l, e); err != nil {
			// A close writes the state that says why, so the exit it caused is
			// refused as already accounted for — the same case app.go's own
			// supervisor names rather than warning about.
			if errors.Is(err, workers.ErrTerminal) {
				return
			}
			t.Logf("recording exit for %s: %v", id, err)
		}
	}

	*stand = workerStand{
		db: db, workerStore: workerStore, dir: dir, ptys: ptys, tp: tp, reg: reg,
		enrol: enrol, lanes: lanes, record: record,
		spawner: spawner, sup: sup, tabs: tabs, log: logger,
	}
	stand.ensureWorker(t, "worker-1", "sess-coordinator")
	return stand
}

// ensureWorker opens a worker and remembers it, so teardown can ask that worker
// whether everything in it has settled.
func (w *workerStand) ensureWorker(t *testing.T, id workers.ID, coordinatorSession string) {
	t.Helper()
	if err := w.workerStore.EnsureGroup(context.Background(), id, coordinatorSession); err != nil {
		t.Fatalf("ensure worker %q: %v", id, err)
	}
	w.mu.Lock()
	w.workerIDs = append(w.workerIDs, id)
	w.mu.Unlock()
}

// openedWorkers is what teardown reads. Under the lock because a worker may be
// opened from a test goroutine while the cleanup runs on the test's own.
func (w *workerStand) openedWorkers() []workers.ID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]workers.ID(nil), w.workerIDs...)
}

// registerWithEnrolment runs a registration and supplies the enrolment the
// launcher would have sent, as soon as the session exists.
//
// It waits on an observable STATE — the session appearing in the registry —
// and never on a duration, because a registration that needed a sleep to be
// seen would be one whose ordering is not actually guaranteed.
func (w *workerStand) registerWithEnrolment(t *testing.T, task string) workers.Participant {
	t.Helper()
	ctx := context.Background()

	type outcome struct {
		p   workers.Participant
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		p, err := w.record.Register(ctx, workers.RegisterRequest{
			Group: "worker-1", CoordinatorSession: "sess-coordinator",
			Role: workers.RoleWorker, Task: task, Command: "claude",
		})
		done <- outcome{p.Participant, err}
	}()

	var sid session.ID
	waittest.WaitFor(t, "the participant's session to exist", func() bool {
		for _, s := range w.reg.List() {
			sid = s.ID()
			return true
		}
		return false
	})
	w.lanes.register("lane-participant", string(sid))
	w.enrol.enrolled(sid, "lane-participant")

	got := <-done
	if got.err != nil {
		t.Fatalf("register: %v", got.err)
	}
	return got.p
}

// The register interval, end to end through the real seams: a participant gets
// a pane of its own, a session in it, and reaches live only once the enrolment
// arrived.
func TestAParticipantGetsAPaneASessionAndGoesLiveOnItsEnrolment(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)

	p := stand.registerWithEnrolment(t, "read AGENTS.md and report")
	if p.State != workers.StateLive {
		t.Fatalf("state = %q, want %q", p.State, workers.StateLive)
	}

	// The pane is real and the session is the pipe of it, which is what makes
	// the participant something a person can switch to and a block can be
	// anchored on.
	sess, err := stand.reg.Get(session.ID(p.Liveness.SessionID))
	if err != nil {
		t.Fatalf("the participant's session is not in the registry: %v", err)
	}
	if sess.PaneID() == "" {
		t.Fatalf("the participant's session is the pipe of no pane")
	}
	snap, err := stand.db.Layout().Snapshot(ctx)
	if err != nil {
		t.Fatalf("layout snapshot: %v", err)
	}
	found := false
	for _, pane := range snap.Panes {
		if pane.ID == sess.PaneID() {
			found = true
		}
	}
	if !found {
		t.Fatalf("the pane the session names is not in the layout chain")
	}

	// And the record holds it: what the registrar returned is what a reader
	// of the record is told afterwards.
	stored, err := stand.workerStore.Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.State != workers.StateLive || stored.Task != "read AGENTS.md and report" {
		t.Fatalf("stored = %+v", stored)
	}
}

// Live is entered on an enrolment that ARRIVED. A launcher that never enrols
// leaves no participant anything may address, and no session behind it.
func TestAParticipantThatNeverEnrolsIsTerminalizedAndItsSessionClosed(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)

	p, err := stand.record.Register(ctx, workers.RegisterRequest{
		Group: "worker-1", CoordinatorSession: "sess-coordinator",
		Role: workers.RoleWorker, Task: "never starts", Command: "claude",
	})
	if !errors.Is(err, workers.ErrEnrolmentNeverArrived) {
		t.Fatalf("register err = %v, want ErrEnrolmentNeverArrived", err)
	}
	stored, err := stand.workerStore.Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !stored.State.Terminal() {
		t.Fatalf("state = %q, want terminal", stored.State)
	}
	waittest.WaitFor(t, "the participant's session to be closed", func() bool {
		return len(stand.reg.List()) == 0
	})
}

// THE PROCESS FACT, through the real carrier, and the one state that is the
// coordinator's own. Nothing on a screen takes part in either.
func TestTheRealSessionExitReachesTheRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("the process ending reads exited", func(t *testing.T) {
		stand := newWorkerStand(t)
		p := stand.registerWithEnrolment(t, "exits")

		if err := stand.reg.Close(session.ID(p.Liveness.SessionID)); err != nil {
			t.Fatalf("close session: %v", err)
		}
		waittest.WaitFor(t, "the exit to reach the record", func() bool {
			stored, err := stand.workerStore.Participant(ctx, p.ID)
			return err == nil && stored.State == workers.StateExited
		})
	})

	// The coordinator ending it is a DIFFERENT fact, and the record keeps it:
	// the exit the close causes races the close's own write, and the record
	// reads `closed` whichever way that falls (ADR-0070 decision 3).
	t.Run("a coordinator's close reads closed", func(t *testing.T) {
		stand := newWorkerStand(t)
		p := stand.registerWithEnrolment(t, "told to stop")

		if _, err := stand.record.Close(ctx, "sess-coordinator", p.ID); err != nil {
			t.Fatalf("close: %v", err)
		}
		waittest.WaitFor(t, "the record to say why it ended", func() bool {
			stored, err := stand.workerStore.Participant(ctx, p.ID)
			return err == nil && stored.State == workers.StateClosed
		})
	})
}

// D3, through the real store: the coordinator asks its SESSION and is told
// what it holds, by name and with the task it was given.
func TestAFreshCoordinatorIsToldWhatItsSessionHolds(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)
	p := stand.registerWithEnrolment(t, "read AGENTS.md and report")

	held, err := stand.record.HeldBy(ctx, "sess-coordinator")
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 1 || held[0].ID != p.ID {
		t.Fatalf("held = %v, want exactly %q", held, p.ID)
	}
	if held[0].Task != "read AGENTS.md and report" {
		t.Fatalf("the coordinator is told an id and not a task: %q", held[0].Task)
	}
}

// An enrolment for a session no worker is waiting on is the ORDINARY case — a
// person running an agent in their own tab — and must not be mistaken for a
// participant.
func TestAnEnrolmentNobodyIsWaitingForIsIgnored(t *testing.T) {
	stand := newWorkerStand(t)
	stand.enrol.enrolled(session.ID("some-other-session"), "lane-x")
	if _, err := stand.enrol.Await(context.Background(), "no-such-participant"); err == nil {
		t.Fatalf("an unrelated enrolment satisfied a participant that was never expected")
	}
}

// THE EPIC'S HAPPY PATH, in one sequence and in order (nocx-dkawo.2).
//
// It runs on the product's own objects — the real encrypted store, the real
// session registry, the real session opener, the real record and the real
// carrier — rather than on a harness beside them. What it does not have is a
// model: a coordinator RUN needs an endpoint, so the coordinator's calls are
// exercised where they live (internal/assistant) and the sequence they drive
// is exercised here.
//
// WHAT THE SEQUENCE IS NOW (ADR-0070): the coordinator starts a worker and is
// told what its session holds by name and by task; it ends the worker; and the
// record says WHY it ended. Nothing here declares a verdict, because nocx
// records none — a worker's own words reach the coordinator through
// workers.report, which worker_report_test.go drives over the real socket.
func TestOneCoordinatorStartsOneWorkerAndIsToldWhatHoldsIt(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)

	// 1. The coordinator starts one worker and gives it a task.
	worker := stand.registerWithEnrolment(t, "read AGENTS.md and report")
	if worker.State != workers.StateLive {
		t.Fatalf("the worker is %q, want %q", worker.State, workers.StateLive)
	}

	// 2. The coordinator goes idle. Nothing here stands in for that, and
	//    that is the assertion: no lease is renewed, no call is outstanding,
	//    and the steps below hold anyway because the BACKEND is what watches.

	// 3. A fresh coordinator — a new run of the same session, holding none of
	//    the previous run's context — asks what its session holds and is told
	//    by name and by task.
	held, err := stand.record.HeldBy(ctx, "sess-coordinator")
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 1 || held[0].ID != worker.ID {
		t.Fatalf("held = %v, want exactly the worker %q", held, worker.ID)
	}
	if held[0].Task != "read AGENTS.md and report" {
		t.Fatalf("the coordinator is told an id and not a task: %q", held[0].Task)
	}
	if held[0].State != workers.StateLive {
		t.Fatalf("the worker reads %q to a fresh coordinator", held[0].State)
	}

	// 4. The coordinator ends it.
	if _, closeErr := stand.record.Close(ctx, "sess-coordinator", worker.ID); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	waittest.WaitFor(t, "the close to reach the record", func() bool {
		got, perr := stand.workerStore.Participant(ctx, worker.ID)
		return perr == nil && got.State == workers.StateClosed
	})

	// 5. And the coordinator is told why, without having held anything across
	//    the turn.
	held, err = stand.record.HeldBy(ctx, "sess-coordinator")
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 1 || held[0].State != workers.StateClosed {
		t.Fatalf("held = %v, want the worker closed by its coordinator", held)
	}
}
