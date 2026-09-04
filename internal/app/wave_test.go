package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/wave"
	"github.com/shady2k/nocx/internal/workspace"
)

// waveTestPTYFactory gives every participant a stub PTY, so the test drives
// the real session registry rather than a double of it: what is being asserted
// is that a real session's exit reaches the record, and a fake session would
// assert only that the fake was called.
type waveTestPTYFactory struct {
	log  log.Logger
	mu   sync.Mutex
	made []*recordingPTY
}

// last is the pty of the most recently opened session, which in these tests is
// the one the caller just opened.
func (f *waveTestPTYFactory) last() *recordingPTY {
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
func (f *waveTestPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
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

// waveStand is the composition this file asserts: the real record, the real
// durable store, the real session registry, the real session opener, and the
// two adapters that carry the facts.
type waveStand struct {
	db     content.ContentDB
	dir    string
	ptys   *waveTestPTYFactory
	tp     *transport.WSServer
	reg    *session.Reg
	enrol  *waveEnrolments
	lanes  *sessionRegistry
	report *waveReporter
	record *wave.Registrar
}

func newWaveStand(t *testing.T, opts ...wave.Option) *waveStand {
	t.Helper()
	ctx := context.Background()
	logger := log.NewSlogAdapter(nil)

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

	ptys := &waveTestPTYFactory{log: logger}
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
			open, err := db.Waves().AllNonTerminal(ctx)
			return err != nil || len(open) == 0
		})
		_ = tp.Stop(ctx)
	})

	enrol := newWaveEnrolments(logger, reg)
	lanes := newSessionRegistry()
	report := &waveReporter{
		lanes: lanes, enrol: enrol, log: logger,
		now: func() time.Time { return time.UnixMilli(1_700_000_000_000).UTC() },
	}
	sup := &waveSupervisor{sessions: reg, log: logger}
	record := wave.NewRegistrar(
		db.Waves(),
		&waveSpawner{
			layout: db.Layout(), opener: tp, sessions: reg,
			enrolments: enrol, workspace: string(workspace.Default), log: logger,
		},
		enrol, sup,
		append([]wave.Option{
			// Short, because every test here supplies the enrolment itself or
			// deliberately withholds it; the number bounds the withheld case
			// and decides nothing about the others.
			wave.WithEnrolmentDeadline(2 * time.Second),
		}, opts...)...,
	)
	report.declare = func(ctx context.Context, id wave.ParticipantID, l wave.Liveness, d wave.Declaration) error {
		_, err := record.Declared(ctx, id, l, d)
		return err
	}
	sup.exited = func(ctx context.Context, id wave.ParticipantID, l wave.Liveness, e wave.Exit) {
		if _, err := record.Exited(ctx, id, l, e); err != nil {
			t.Logf("recording exit for %s: %v", id, err)
		}
	}

	if err := db.Waves().EnsureWave(ctx, "wave-1", "sess-coordinator"); err != nil {
		t.Fatalf("ensure wave: %v", err)
	}
	return &waveStand{
		db: db, dir: dir, ptys: ptys, tp: tp, reg: reg,
		enrol: enrol, lanes: lanes, report: report, record: record,
	}
}

// registerWithEnrolment runs a registration and supplies the enrolment the
// launcher would have sent, as soon as the session exists.
//
// It waits on an observable STATE — the session appearing in the registry —
// and never on a duration, because a registration that needed a sleep to be
// seen would be one whose ordering is not actually guaranteed.
func (w *waveStand) registerWithEnrolment(t *testing.T, task string) wave.Participant {
	t.Helper()
	ctx := context.Background()

	type outcome struct {
		p   wave.Participant
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		p, err := w.record.Register(ctx, wave.RegisterRequest{
			Wave: "wave-1", CoordinatorSession: "sess-coordinator",
			Role: wave.RoleWorker, Task: task, Command: "claude",
		})
		done <- outcome{p, err}
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
	stand := newWaveStand(t)

	p := stand.registerWithEnrolment(t, "read AGENTS.md and report")
	if p.State != wave.StateLive {
		t.Fatalf("state = %q, want %q", p.State, wave.StateLive)
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

	// And the record is durable: a fresh reader over the same file sees it.
	stored, err := stand.db.Waves().Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.State != wave.StateLive || stored.Task != "read AGENTS.md and report" {
		t.Fatalf("stored = %+v", stored)
	}
}

// Live is entered on an enrolment that ARRIVED. A launcher that never enrols
// leaves no participant anything may address, and no session behind it.
func TestAParticipantThatNeverEnrolsIsTerminalizedAndItsSessionClosed(t *testing.T) {
	ctx := context.Background()
	stand := newWaveStand(t)

	p, err := stand.record.Register(ctx, wave.RegisterRequest{
		Wave: "wave-1", CoordinatorSession: "sess-coordinator",
		Role: wave.RoleWorker, Task: "never starts", Command: "claude",
	})
	if !errors.Is(err, wave.ErrEnrolmentNeverArrived) {
		t.Fatalf("register err = %v, want ErrEnrolmentNeverArrived", err)
	}
	stored, err := stand.db.Waves().Participant(ctx, p.ID)
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

// THE TWO FACTS, through the real carriers. The session exiting is the
// observed one; nothing on a screen took part.
func TestTheRealSessionExitReachesTheRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("an exit with no declaration is abandoned", func(t *testing.T) {
		stand := newWaveStand(t)
		p := stand.registerWithEnrolment(t, "exits without saying anything")

		if err := stand.reg.Close(session.ID(p.Liveness.SessionID)); err != nil {
			t.Fatalf("close session: %v", err)
		}
		waittest.WaitFor(t, "the exit to reach the record", func() bool {
			stored, err := stand.db.Waves().Participant(ctx, p.ID)
			return err == nil && stored.State == wave.StateAbandoned
		})
	})

	t.Run("a declaration then an exit completes", func(t *testing.T) {
		stand := newWaveStand(t)
		p := stand.registerWithEnrolment(t, "says what it did")

		// The declaration alone must NOT terminalize: the agent said it
		// finished and its process is still there.
		declared, err := stand.record.Declared(ctx, p.ID, p.Liveness,
			wave.Declaration{OK: true, Summary: "read it", At: time.Now()})
		if err != nil {
			t.Fatalf("declare: %v", err)
		}
		if declared.State != wave.StateLive {
			t.Fatalf("state after a declaration alone = %q, want %q", declared.State, wave.StateLive)
		}

		if err := stand.reg.Close(session.ID(p.Liveness.SessionID)); err != nil {
			t.Fatalf("close session: %v", err)
		}
		waittest.WaitFor(t, "the conjunction to complete", func() bool {
			stored, perr := stand.db.Waves().Participant(ctx, p.ID)
			return perr == nil && stored.State == wave.StateCompleted
		})
	})
}

// D3, through the real store: the coordinator asks its SESSION and is told
// what it holds, by name and with the task it was given.
func TestAFreshCoordinatorIsToldWhatItsSessionHolds(t *testing.T) {
	ctx := context.Background()
	stand := newWaveStand(t)
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

// A restart closes what this backend can no longer judge, and never adopts.
func TestTheStartupSweepInterruptsWhatTheBackendCannotJudge(t *testing.T) {
	ctx := context.Background()
	stand := newWaveStand(t)
	p := stand.registerWithEnrolment(t, "outlives nothing")

	if err := stand.record.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stored, err := stand.db.Waves().Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.State != wave.StateInterrupted {
		t.Fatalf("state = %q, want %q", stored.State, wave.StateInterrupted)
	}
}

// An enrolment for a session no wave is waiting on is the ORDINARY case — a
// person running an agent in their own tab — and must not be mistaken for a
// participant.
func TestAnEnrolmentNobodyIsWaitingForIsIgnored(t *testing.T) {
	stand := newWaveStand(t)
	stand.enrol.enrolled(session.ID("some-other-session"), "lane-x")
	if _, err := stand.enrol.Await(context.Background(), "no-such-participant"); err == nil {
		t.Fatalf("an unrelated enrolment satisfied a participant that was never expected")
	}
}

// The declaration reaches the record over the authenticated channel, and the
// two facts still refuse to complete on their own.
func TestADeclarationOverTheChannelReachesTheRecord(t *testing.T) {
	ctx := context.Background()
	stand := newWaveStand(t)
	p := stand.registerWithEnrolment(t, "says what it did")

	if err := stand.report.Report("lane-participant", true, "read it"); err != nil {
		t.Fatalf("report: %v", err)
	}
	stored, err := stand.db.Waves().Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.Declared == nil {
		t.Fatalf("the declaration did not reach the record")
	}
	if !stored.Declared.OK || stored.Declared.Summary != "read it" {
		t.Fatalf("declaration = %+v", *stored.Declared)
	}
	// Still live: the agent said it finished and its process is still there.
	if stored.State != wave.StateLive {
		t.Fatalf("state = %q, want %q", stored.State, wave.StateLive)
	}

	if err := stand.reg.Close(session.ID(p.Liveness.SessionID)); err != nil {
		t.Fatalf("close session: %v", err)
	}
	waittest.WaitFor(t, "the conjunction to complete", func() bool {
		got, perr := stand.db.Waves().Participant(ctx, p.ID)
		return perr == nil && got.State == wave.StateCompleted
	})
}

// A pane that is not a participant is told so, rather than having its
// declaration accepted into nowhere. This is the ordinary case: a person's own
// agent may be integrated and enrolled and belongs to no wave.
func TestAReportFromAPaneThatIsNotAParticipantIsRefused(t *testing.T) {
	stand := newWaveStand(t)
	stand.lanes.register("lane-someones-own-tab", "some-session")

	err := stand.report.Report("lane-someones-own-tab", true, "done")
	if err == nil {
		t.Fatalf("a report from a pane in no wave was accepted")
	}
	if !strings.Contains(err.Error(), "not part of a wave") {
		t.Fatalf("err = %v, want a sentence naming the cause", err)
	}
}

// A lane that maps to no session at all is refused with the sentence the
// enroller uses for the same state, because it IS the same state.
func TestAReportOnALaneThatNamesNoSessionIsRefused(t *testing.T) {
	stand := newWaveStand(t)
	if err := stand.report.Report("lane-nobody", true, "done"); err == nil {
		t.Fatalf("a report on an unmapped lane was accepted")
	}
}

// THE EPIC'S HAPPY PATH, in one sequence and in order (nocx-dkawo.2).
//
// It runs on the product's own objects — the real encrypted store, the real
// session registry, the real session opener, the real record and both real
// carriers — rather than on a harness beside them. What it does not have is a
// model: a coordinator RUN needs an endpoint, so the coordinator's two calls
// are exercised where they live (internal/assistant) and the sequence they
// drive is exercised here.
func TestOneCoordinatorStartsOneWorkerAndIsToldWhatItCameTo(t *testing.T) {
	ctx := context.Background()
	stand := newWaveStand(t)

	// 1. The coordinator starts one worker and gives it a task.
	worker := stand.registerWithEnrolment(t, "read AGENTS.md and report")
	if worker.State != wave.StateLive {
		t.Fatalf("the worker is %q, want %q", worker.State, wave.StateLive)
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
	if held[0].State != wave.StateLive {
		t.Fatalf("the worker reads %q to a fresh coordinator", held[0].State)
	}

	// 4. The worker declares what it produced. Still live: it said it
	//    finished and its process is still there.
	if reportErr := stand.report.Report("lane-participant", true, "read it; nothing to change"); reportErr != nil {
		t.Fatalf("report: %v", reportErr)
	}
	after, err := stand.db.Waves().Participant(ctx, worker.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.State != wave.StateLive {
		t.Fatalf("a declaration alone moved the worker to %q", after.State)
	}

	// 5. Its process exits. Only now, and only because BOTH facts are in, is
	//    it complete.
	if closeErr := stand.reg.Close(session.ID(worker.Liveness.SessionID)); closeErr != nil {
		t.Fatalf("close the worker's session: %v", closeErr)
	}
	waittest.WaitFor(t, "the worker to complete", func() bool {
		got, perr := stand.db.Waves().Participant(ctx, worker.ID)
		return perr == nil && got.State == wave.StateCompleted
	})

	// 6. And the coordinator is told what it came to, in the worker's own
	//    words, without having held anything across the turn.
	held, err = stand.record.HeldBy(ctx, "sess-coordinator")
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 1 || held[0].State != wave.StateCompleted {
		t.Fatalf("held = %v, want the worker completed", held)
	}
	if held[0].Declared == nil || held[0].Declared.Summary != "read it; nothing to change" {
		t.Fatalf("the coordinator was not told what the worker produced: %+v", held[0].Declared)
	}
}
