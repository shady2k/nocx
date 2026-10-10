package content

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecyclecommit"
	"github.com/shady2k/nocx/internal/log"

	"github.com/shady2k/nocx/internal/waittest"
)

// ONE FRAME, ONE TRANSACTION (ADR-0077). A lifecycle frame's writes — here
// the start fact's own: the entry, the environment it ran in, the
// observation it pins, its execution — and the session's cursor commit
// together or not at all. A fault anywhere inside the frame leaves the store
// exactly as it was, and the frame applied again afterwards lands once.

const frameSession = "0123456789abcdef0123456789abcdef"

// frameTimers is the store's frame timer, driven by the test: a hold bound
// fires only when the test fires it, and every pause between attempts fires
// at once and is recorded — no test here waits on a duration.
type frameTimers struct {
	mu     sync.Mutex
	holds  []*frameTimer
	armed  chan *frameTimer
	pauses []time.Duration
	// onPause, when set, runs during each pause, before it ends: the
	// connection is free then, and the test may change the store.
	onPause func()
}

type frameTimer struct {
	mu   sync.Mutex
	fire func()
	done bool
}

func newFrameTimers() *frameTimers { return &frameTimers{armed: make(chan *frameTimer, 16)} }

func (ft *frameTimers) timer(d time.Duration, fire func()) func() bool {
	if d != LifecycleFrameMaxHold {
		ft.mu.Lock()
		ft.pauses = append(ft.pauses, d)
		onPause := ft.onPause
		ft.mu.Unlock()
		go func() {
			if onPause != nil {
				onPause()
			}
			fire()
		}()
		return func() bool { return false }
	}
	t := &frameTimer{fire: fire}
	ft.mu.Lock()
	ft.holds = append(ft.holds, t)
	ft.mu.Unlock()
	ft.armed <- t
	return func() bool {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.done {
			return false
		}
		t.done = true
		return true
	}
}

// fireNow expires the hold bound it stands for, unless the frame already
// ended.
func (t *frameTimer) fireNow() {
	t.mu.Lock()
	if t.done {
		t.mu.Unlock()
		return
	}
	t.done = true
	t.mu.Unlock()
	t.fire()
}

func (ft *frameTimers) recordedPauses() []time.Duration {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return append([]time.Duration(nil), ft.pauses...)
}

func openFrameStore(t *testing.T) (*sqliteContent, *sql.DB) {
	t.Helper()
	s, raw, _ := openFrameStoreTimed(t)
	return s, raw
}

func openFrameStoreTimed(t *testing.T) (*sqliteContent, *sql.DB, *frameTimers) {
	t.Helper()
	timers := newFrameTimers()
	db, err := Open(context.Background(), Config{
		Path:       filepath.Join(t.TempDir(), "content.db"),
		Key:        testKeyInternal(),
		Budget:     testBudgetInternal(),
		Logger:     log.NewSlogAdapter(nil),
		FrameTimer: timers.timer,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, ok := db.(*sqliteContent)
	if !ok {
		t.Fatalf("Open returned %T, want the sqlite store", db)
	}
	ctx := context.Background()
	if _, err := s.Layout().CreateWorkspace(ctx,
		Workspace{ID: "ws-frame", Name: "frame"},
		Tab{ID: "tab-frame", WorkspaceID: "ws-frame", Layout: LayoutRow},
		Pane{ID: "pane-frame", TabID: "tab-frame", Cwd: "/", Kind: PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	zero := uint64(0)
	if err := s.Ledger().CreateSession(ctx, Session{ID: frameSession, WorkspaceID: "ws-frame", LifecycleApplied: &zero}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drain the hold timers the setup's own writes never armed: none do,
	// since no frame ran; the channel starts empty for the test.
	return s, s.db, timers
}

// startFrame is the start fact's writes, as the transport makes them.
func startFrame(s *sqliteContent, entry string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		led := s.Ledger()
		env := Environment{ID: "local", Kind: EnvLocal}
		if err := led.EnsureEnvironment(ctx, env); err != nil {
			return err
		}
		sessionID := frameSession
		if _, err := led.Submit(ctx, SubmitEntry{
			ID: entry, Client: "lifecycle-shell", EnvironmentID: env.ID, Cwd: "/", Intent: "make build",
			Kind: EntryShell, Source: SourceUser, SessionID: &sessionID,
		}); err != nil {
			return err
		}
		// Read-your-writes inside the frame: the entry just submitted is
		// what the execution below pins against.
		if row, err := led.Entry(ctx, entry); err != nil || row == nil {
			return errors.Join(errors.New("the frame cannot read its own entry"), err)
		}
		if _, err := led.RecordObservation(ctx, Observation{EnvironmentID: env.ID, Confidence: "{}", Criticality: CriticalityRoutine, Payload: "{}"}); err != nil {
			return err
		}
		_, err := led.StartExecution(ctx, StartExecution{EntryID: entry})
		return err
	}
}

// storedFrame is what a frame left behind: the entry's executions (-1 when
// there is no entry) and the session's cursor.
func storedFrame(t *testing.T, s *sqliteContent, entry string) (executions int, cursor uint64) {
	t.Helper()
	ctx := context.Background()
	row, err := s.Ledger().Entry(ctx, entry)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	executions = -1
	if row != nil {
		executions = len(row.Executions)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT json_extract(payload, '$.lifecycleApplied') FROM sessions WHERE id = ?`, frameSession).Scan(&cursor); err != nil {
		t.Fatalf("reading the cursor: %v", err)
	}
	return executions, cursor
}

func TestALifecycleFrameCommitsItsRowsAndTheCursorTogether(t *testing.T) {
	s, _ := openFrameStore(t)
	if err := s.ApplyLifecycleFrame(context.Background(), frameSession, 120, startFrame(s, "s-dom-frame-0")); err != nil {
		t.Fatalf("ApplyLifecycleFrame: %v", err)
	}
	if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != 1 || cursor != 120 {
		t.Fatalf("after the frame: %d executions, cursor %d — want 1 and 120", executions, cursor)
	}
}

func TestAFaultInsideALifecycleFrameLeavesNeitherItsRowsNorTheCursor(t *testing.T) {
	for _, fault := range []struct {
		name, trigger string
	}{
		// Inside the frame's writes: the execution, its last row.
		{"inside the frame's writes", `CREATE TEMP TRIGGER fault BEFORE INSERT ON executions BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		// After the frame's last row, at the cursor.
		{"between the last row and the cursor", `CREATE TEMP TRIGGER fault BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'injected'); END`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			s, raw := openFrameStore(t)
			ctx := context.Background()
			// TEMP triggers belong to the connection that made them, and
			// the store has exactly one.
			if _, err := raw.ExecContext(ctx, fault.trigger); err != nil {
				t.Fatalf("injecting the fault: %v", err)
			}
			err := s.ApplyLifecycleFrame(ctx, frameSession, 120, startFrame(s, "s-dom-frame-0"))
			var failed *FrameFailedError
			if !errors.Is(err, ErrLifecycleFrameFailed) || !errors.As(err, &failed) || failed.Attempts != 4 {
				t.Fatalf("ApplyLifecycleFrame = %v, want a FrameFailedError after 4 attempts: the fault persists, so every attempt fails", err)
			}
			if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != -1 || cursor != 0 {
				t.Fatalf("after the failed frame: executions %d (entry present: %v), cursor %d — want no entry and the cursor where it was",
					executions, executions != -1, cursor)
			}

			// The next coordinator, resuming from the cursor, applies the
			// same frame again: once.
			if _, err := raw.ExecContext(ctx, `DROP TRIGGER fault`); err != nil {
				t.Fatalf("clearing the fault: %v", err)
			}
			if err := s.ApplyLifecycleFrame(ctx, frameSession, 120, startFrame(s, "s-dom-frame-0")); err != nil {
				t.Fatalf("the frame applied again: %v", err)
			}
			if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != 1 || cursor != 120 {
				t.Fatalf("after the frame applied again: %d executions, cursor %d — want 1 and 120", executions, cursor)
			}
		})
	}
}

// telling applies the frame and queues one effect that tells somebody of it,
// as the publisher's ACCEPT and the renderer's block notification do; told
// records what the effect was run with.
func telling(apply func(context.Context) error, told *[]bool) func(context.Context) error {
	return func(ctx context.Context) error {
		lifecyclecommit.After(ctx, nil, func(committed bool) { *told = append(*told, committed) })
		return apply(ctx)
	}
}

// A frame that fails every attempt tells nobody it happened, and a frame that
// commits tells once (ADR-0077 decision 12). The queue's own tests prove it
// runs or drops what it is given; this proves the store ENDS it with the
// frame's real outcome — without it the shell would be sent an ACCEPT for a
// frame the store never holds, and the next coordinator would send it again.
func TestAFrameThatFailsEveryAttemptTellsNobodyAndACommittedOneTellsOnce(t *testing.T) {
	s, raw := openFrameStore(t)
	ctx := context.Background()
	if _, err := raw.ExecContext(ctx, `CREATE TEMP TRIGGER fault BEFORE INSERT ON executions BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatalf("injecting the fault: %v", err)
	}
	var told []bool
	if err := s.ApplyLifecycleFrame(ctx, frameSession, 120, telling(startFrame(s, "s-dom-frame-0"), &told)); !errors.Is(err, ErrLifecycleFrameFailed) {
		t.Fatalf("ApplyLifecycleFrame = %v, want the frame to fail every attempt", err)
	}
	for _, committed := range told {
		if committed {
			t.Fatalf("a frame that failed every attempt ran its effects as committed: %v", told)
		}
	}

	if _, err := raw.ExecContext(ctx, `DROP TRIGGER fault`); err != nil {
		t.Fatalf("clearing the fault: %v", err)
	}
	told = nil
	if err := s.ApplyLifecycleFrame(ctx, frameSession, 120, telling(startFrame(s, "s-dom-frame-0"), &told)); err != nil {
		t.Fatalf("the frame applied again: %v", err)
	}
	if len(told) != 1 || !told[0] {
		t.Fatalf("a committed frame ran its effect %v, want once, as committed", told)
	}
}

// A write another goroutine makes while a frame holds the connection is not
// the frame's: it waits for the frame to end and is stored whether the frame
// commits or not.
func TestAFrameThatFailsEveryAttemptPausesBetweenThemAndStoresNothing(t *testing.T) {
	s, raw, timers := openFrameStoreTimed(t)
	ctx := context.Background()
	if _, err := raw.ExecContext(ctx, `CREATE TEMP TRIGGER fault BEFORE INSERT ON executions BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatalf("injecting the fault: %v", err)
	}
	err := s.ApplyLifecycleFrame(ctx, frameSession, 120, startFrame(s, "s-dom-frame-0"))
	var failed *FrameFailedError
	if !errors.As(err, &failed) || failed.Attempts != 4 {
		t.Fatalf("ApplyLifecycleFrame = %v, want a FrameFailedError after 4 attempts", err)
	}
	want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	if got := timers.recordedPauses(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("the pauses between attempts were %v, want %v", got, want)
	}
	if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != -1 || cursor != 0 {
		t.Fatalf("after every attempt failed: executions %d, cursor %d — want nothing stored and the cursor where it was", executions, cursor)
	}
}

// ONE EXPIRY, THEN SUCCESS (the owner's retry, 2026-09-30). The frame's first
// attempt is still inside its projection when its hold bound fires: it is
// rolled back where it stands, and the connection is free again at once — a
// reader outside the frame reads without waiting for the frame to finish. The
// same coordinator then replays the frame's writes in a fresh transaction,
// and the frame lands exactly once, cursor included.
func TestAFrameRolledBackByItsHoldBoundIsAppliedOnceByTheSameCoordinator(t *testing.T) {
	s, _, timers := openFrameStoreTimed(t)
	ctx := context.Background()
	holding := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.ApplyLifecycleFrame(ctx, frameSession, 120, func(fctx context.Context) error {
			if err := startFrame(s, "s-dom-frame-0")(fctx); err != nil {
				return err
			}
			close(holding)
			<-release // the projection is still running when the bound fires
			return nil
		})
	}()
	<-holding
	first := <-timers.armed
	first.fireNow()
	// Rolled back and the connection given back while the frame's own
	// goroutine is still inside it: this read would wait for the frame
	// otherwise.
	if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != -1 || cursor != 0 {
		t.Fatalf("after the bound fired: executions %d, cursor %d — want the attempt rolled back", executions, cursor)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("ApplyLifecycleFrame = %v, want the frame applied on its second attempt", err)
	}
	if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != 1 || cursor != 120 {
		t.Fatalf("after the second attempt: %d executions, cursor %d — want 1 and 120", executions, cursor)
	}
	if got := timers.recordedPauses(); len(got) != 1 || got[0] != 50*time.Millisecond {
		t.Fatalf("pauses %v, want the one 50ms pause before the second attempt", got)
	}
}

func TestAWriteOutsideAFrameIsNotRolledBackWithIt(t *testing.T) {
	s, raw := openFrameStore(t)
	ctx := context.Background()
	// Every attempt of the frame fails at its cursor; the outside write
	// touches no binding row.
	if _, err := raw.ExecContext(ctx, `CREATE TEMP TRIGGER fault BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatalf("injecting the fault: %v", err)
	}
	outside := make(chan error, 1)
	err := s.ApplyLifecycleFrame(ctx, frameSession, 120, func(fctx context.Context) error {
		if err := startFrame(s, "s-dom-frame-0")(fctx); err != nil {
			return err
		}
		go func() {
			_, err := s.Ledger().RecordCompleted(ctx, aRecordedCommand("outside the frame"))
			outside <- err
		}()
		return nil
	})
	if !errors.Is(err, ErrLifecycleFrameFailed) {
		t.Fatalf("ApplyLifecycleFrame = %v, want ErrLifecycleFrameFailed", err)
	}
	if werr := <-outside; werr != nil {
		t.Fatalf("the write outside the frame: %v", werr)
	}
	if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != -1 || cursor != 0 {
		t.Fatalf("the failed frame left %d executions and cursor %d", executions, cursor)
	}
	waittest.WaitFor(t, "the outside write to be stored", func() bool {
		page, err := s.Ledger().ListEntries(ctx, 10)
		return err == nil && len(page) == 1 && page[0].Intent == "outside the frame"
	})
}

// A FAILED WRITE IS NEVER SEEN BY THE FRAME THAT MADE IT (ADR-0077 decision
// 9). The frame's execution fails on its first transaction; the store rolls
// the transaction back, pauses — the fault is gone by the time the pause
// ends — begins a fresh one, replays the entry and the rest, and runs the
// execution again. The frame's own code is told the execution was stored,
// and it is: once, with the cursor.
func TestAWriteThatFailsOnceAnswersItsFrameAsIfItHadNot(t *testing.T) {
	s, raw, timers := openFrameStoreTimed(t)
	ctx := context.Background()
	if _, err := raw.ExecContext(ctx, `CREATE TEMP TRIGGER fault BEFORE INSERT ON executions BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatalf("injecting the fault: %v", err)
	}
	timers.onPause = func() {
		if _, err := raw.ExecContext(ctx, `DROP TRIGGER IF EXISTS fault`); err != nil {
			t.Errorf("clearing the fault during the pause: %v", err)
		}
	}
	var seen error
	if err := s.ApplyLifecycleFrame(ctx, frameSession, 120, func(fctx context.Context) error {
		seen = startFrame(s, "s-dom-frame-0")(fctx)
		return nil
	}); err != nil {
		t.Fatalf("ApplyLifecycleFrame = %v, want the frame applied", err)
	}
	if seen != nil {
		t.Fatalf("the frame's own writes answered %v, want the failed execution's second run to answer it", seen)
	}
	if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != 1 || cursor != 120 {
		t.Fatalf("after the frame: %d executions, cursor %d — want 1 and 120", executions, cursor)
	}
	if got := timers.recordedPauses(); len(got) != 1 || got[0] != 50*time.Millisecond {
		t.Fatalf("pauses %v, want the one 50ms pause before the second transaction", got)
	}
}

// A WRITE THE STORE REFUSES IS ITS ANSWER, AND THE FRAME GOES ON. A block
// opened for an entry the store does not hold is refused (ErrNoSuchEntry)
// — the answer the caller has always branched on — and the frame's other
// writes and its cursor commit.
func TestAWriteTheStoreRefusesIsItsAnswerAndTheFrameCommits(t *testing.T) {
	s, _ := openFrameStore(t)
	ctx := context.Background()
	var refused error
	if err := s.ApplyLifecycleFrame(ctx, frameSession, 120, func(fctx context.Context) error {
		if err := startFrame(s, "s-dom-frame-0")(fctx); err != nil {
			return err
		}
		_, refused = s.OpenBlockOutput(fctx, OpenBlockOutput{EntryID: "no-such-entry", ArtifactID: "art-refused"})
		return nil
	}); err != nil {
		t.Fatalf("ApplyLifecycleFrame = %v, want the frame committed around the refusal", err)
	}
	if !errors.Is(refused, ErrNoSuchEntry) {
		t.Fatalf("the open for a missing entry answered %v, want ErrNoSuchEntry", refused)
	}
	if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != 1 || cursor != 120 {
		t.Fatalf("after the frame: %d executions, cursor %d — want 1 and 120", executions, cursor)
	}
}

// A REPLAY THE STORE ANSWERS DIFFERENTLY FAILS THE FRAME. The frame opens a
// block under an artifact id; its transaction is rolled back by the bound,
// and while it pauses another writer takes that id for another entry. The
// replayed open would now be refused — the block the frame's caller already
// installed is not the one the store would hold — so the frame fails and
// stores nothing, rather than committing a frame nobody decided on.
func TestAReplayTheStoreAnswersDifferentlyFailsTheFrame(t *testing.T) {
	s, _, timers := openFrameStoreTimed(t)
	ctx := context.Background()
	for _, entry := range []string{"s-dom-frame-0", "s-dom-frame-1"} {
		if err := startFrame(s, entry)(ctx); err != nil {
			t.Fatalf("starting %s: %v", entry, err)
		}
	}
	timers.onPause = func() {
		if _, err := s.OpenBlockOutput(ctx, OpenBlockOutput{EntryID: "s-dom-frame-1", ArtifactID: "art-contested"}); err != nil {
			t.Errorf("the other writer's open: %v", err)
		}
	}
	err := s.ApplyLifecycleFrame(ctx, frameSession, 120, func(fctx context.Context) error {
		if _, err := s.OpenBlockOutput(fctx, OpenBlockOutput{EntryID: "s-dom-frame-0", ArtifactID: "art-contested"}); err != nil {
			return err
		}
		(<-timers.armed).fireNow()
		return nil
	})
	if !errors.Is(err, ErrLifecycleFrameFailed) || !errors.Is(err, ErrFrameReplayDiverged) {
		t.Fatalf("ApplyLifecycleFrame = %v, want the frame failed on its diverged replay", err)
	}
	if _, cursor := storedFrame(t, s, "s-dom-frame-0"); cursor != 0 {
		t.Fatalf("the failed frame moved the cursor to %d", cursor)
	}
	row, err := s.Ledger().Entry(ctx, "s-dom-frame-0")
	if err != nil || row == nil {
		t.Fatalf("Entry: %v", err)
	}
	for _, ex := range row.Executions {
		if len(ex.Artifacts) != 0 {
			t.Fatalf("the failed frame's block was stored: %+v", ex.Artifacts)
		}
	}
}

// A TRANSACTION WHOSE BOUND FIRED IS NEVER COMMITTED (codex review of the
// run, finding 1). The bound's callback is released exactly as the commit
// begins: disarming it then fails, as time.AfterFunc's Stop does once its
// function has started, and the callback's rollback races the commit. The
// commit must wait for the callback and go by what it did: the expired
// transaction is rolled back, and the frame commits on its second one.
func TestATransactionWhoseBoundFiredAsTheCommitBeganIsNotCommitted(t *testing.T) {
	var mu sync.Mutex
	var pauses []time.Duration
	armed := 0
	timer := func(d time.Duration, fire func()) func() bool {
		if d != LifecycleFrameMaxHold {
			mu.Lock()
			pauses = append(pauses, d)
			mu.Unlock()
			go fire()
			return func() bool { return false }
		}
		mu.Lock()
		armed++
		first := armed == 1
		mu.Unlock()
		if !first {
			return func() bool { return true }
		}
		// The first transaction's bound fires the moment its commit
		// begins disarming it: Stop loses, and the callback is running.
		return func() bool {
			go fire()
			return false
		}
	}
	db, err := Open(context.Background(), Config{
		Path: filepath.Join(t.TempDir(), "content.db"), Key: testKeyInternal(), Budget: testBudgetInternal(),
		Logger: log.NewSlogAdapter(nil), FrameTimer: timer,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, ok := db.(*sqliteContent)
	if !ok {
		t.Fatalf("Open returned %T, want the sqlite store", db)
	}
	ctx := context.Background()
	if _, err := s.Layout().CreateWorkspace(ctx,
		Workspace{ID: "ws-frame", Name: "frame"},
		Tab{ID: "tab-frame", WorkspaceID: "ws-frame", Layout: LayoutRow},
		Pane{ID: "pane-frame", TabID: "tab-frame", Cwd: "/", Kind: PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	zero := uint64(0)
	if err := s.Ledger().CreateSession(ctx, Session{ID: frameSession, WorkspaceID: "ws-frame", LifecycleApplied: &zero}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.ApplyLifecycleFrame(ctx, frameSession, 120, startFrame(s, "s-dom-frame-0")); err != nil {
		t.Fatalf("ApplyLifecycleFrame = %v, want the frame applied on its second transaction", err)
	}
	mu.Lock()
	got := append([]time.Duration(nil), pauses...)
	mu.Unlock()
	if len(got) != 1 || got[0] != 50*time.Millisecond {
		t.Fatalf("pauses %v: the transaction whose bound fired as its commit began was committed — want it rolled back and the frame committed after one 50ms pause", got)
	}
	if executions, cursor := storedFrame(t, s, "s-dom-frame-0"); executions != 1 || cursor != 120 {
		t.Fatalf("after the frame: %d executions, cursor %d — want 1 and 120", executions, cursor)
	}
}

// A CURSOR THAT CANNOT BE WRITTEN FAILS THE FRAME; ONE ALREADY PAST IT IS A
// NO-OP (codex review of the run, finding 2). The cursor is the frame's
// proof that it was applied, so a write that lands on no binding — the row
// is gone, or it records no cursor — may not read as success. A binding
// whose cursor is already at or past the frame is the legitimate no-op.
func TestACursorWriteThatLandsNowhereFailsTheFrame(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, s *sqliteContent, raw *sql.DB)
	}{
		{"the binding is gone", func(t *testing.T, s *sqliteContent, raw *sql.DB) {
			if _, err := raw.ExecContext(context.Background(), `DELETE FROM sessions WHERE id = ?`, frameSession); err != nil {
				t.Fatal(err)
			}
		}},
		{"the binding records no cursor", func(t *testing.T, s *sqliteContent, raw *sql.DB) {
			if _, err := raw.ExecContext(context.Background(),
				`UPDATE sessions SET payload = json_remove(payload, '$.lifecycleApplied') WHERE id = ?`, frameSession); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, raw := openFrameStore(t)
			tc.setup(t, s, raw)
			err := s.ApplyLifecycleFrame(context.Background(), frameSession, 120, startFrame(s, "s-dom-frame-0"))
			if !errors.Is(err, ErrLifecycleFrameFailed) || !errors.Is(err, ErrLifecycleCursorMissing) {
				t.Fatalf("ApplyLifecycleFrame = %v, want the frame failed with ErrLifecycleCursorMissing", err)
			}
			if row, rerr := s.Ledger().Entry(context.Background(), "s-dom-frame-0"); rerr != nil || row != nil {
				t.Fatalf("the failed frame stored its entry: %+v, %v", row, rerr)
			}
		})
	}
	t.Run("the cursor is already past the frame", func(t *testing.T) {
		s, _ := openFrameStore(t)
		ctx := context.Background()
		if err := s.ApplyLifecycleFrame(ctx, frameSession, 500, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("moving the cursor to 500: %v", err)
		}
		if err := s.ApplyLifecycleFrame(ctx, frameSession, 120, startFrame(s, "s-dom-frame-0")); err != nil {
			t.Fatalf("a frame behind the cursor = %v, want a no-op success", err)
		}
		if _, cursor := storedFrame(t, s, "s-dom-frame-0"); cursor != 500 {
			t.Fatalf("the cursor moved back to %d", cursor)
		}
	})
}

// A REPLAYED SUBMIT THAT WOULD MINT WHAT THE FIRST DID NOT FAILS THE FRAME
// (codex review of the run, finding 3). History is off on the frame's first
// transaction, so its submit answers the zero result — no entry, and the
// frame's caller goes on believing there is none. History is turned on while
// the frame pauses; replayed, the same submit would mint the entry nobody
// decided on. The replay's answer differs, so the frame fails.
func TestAReplayedSubmitThatWouldMintAnEntryFailsTheFrame(t *testing.T) {
	s, _, timers := openFrameStoreTimed(t)
	ctx := context.Background()
	s.cfg.Policy.SetEnabled(false)
	timers.onPause = func() { s.cfg.Policy.SetEnabled(true) }
	sessionID := frameSession
	var first SubmitResult
	err := s.ApplyLifecycleFrame(ctx, frameSession, 120, func(fctx context.Context) error {
		led := s.Ledger()
		if err := led.EnsureEnvironment(fctx, Environment{ID: "local", Kind: EnvLocal}); err != nil {
			return err
		}
		var err error
		first, err = led.Submit(fctx, SubmitEntry{
			ID: "s-dom-frame-0", Client: "lifecycle-shell", EnvironmentID: "local", Cwd: "/", Intent: "make build",
			Kind: EntryShell, Source: SourceUser, SessionID: &sessionID,
		})
		if err != nil {
			return err
		}
		(<-timers.armed).fireNow()
		return nil
	})
	if first.ID != "" {
		t.Fatalf("history off answered %+v, want the zero result", first)
	}
	if !errors.Is(err, ErrFrameReplayDiverged) {
		t.Fatalf("ApplyLifecycleFrame = %v, want the frame failed on its diverged replay", err)
	}
	if row, rerr := s.Ledger().Entry(ctx, "s-dom-frame-0"); rerr != nil || row != nil {
		t.Fatalf("the replay minted the entry: %+v, %v", row, rerr)
	}
}
