package app

// The delivery nocx-66gd0 closes: workers.spawn's schema promised the task
// would reach the worker, and the code only ever recorded it — the worker's
// pane ran a bare interactive agent nobody had spoken to. These tests exercise
// workerSpawner.Spawn's own delivery step, deliberately through the SAME
// gates a wake and agent.type go through (internal/agenttyping's grant/look),
// for the reason worker_wake_test.go's header states: the screen is what
// decides whether a keystroke is safe, and a test that faked it would only be
// asserting that a fake permits typing.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// taskDeliveryStand is a real session registry over stub ptys, a recorded-
// calls tab store, and the real observation seam a production spawner is
// wired to (app.go's readiness: paneWatch) — so what is asserted here is
// what the shipped wait does, not what a double of it was told to do. It
// builds no TaskQueue: Task 11 moved actual delivery to
// internal/workers.Registrar (see workers.TaskQueue's own doc for why), and
// these tests exercise workerSpawner.Spawn/deliverTask directly, never
// through a Registrar — that seam has its own coverage
// (internal/workers/registrar_taskqueue_test.go) and
// internal/app/pane_messages_test.go's own delivery mechanics.
type taskDeliveryStand struct {
	reg     *session.Reg
	ptys    *workerTestPTYFactory
	tabs    *fakeAxisTabs
	opener  *fakeAxisOpener
	grid    *paneviewtest.Views
	rules   *agentdriver.Registry
	watch   *paneobserve.Watcher
	enrol   *workerEnrolments
	spawner *workerSpawner
	sup     *workerSupervisor
	log     log.Logger
}

func newTaskDeliveryStand(t *testing.T) *taskDeliveryStand {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
	})
	tabs := &fakeAxisTabs{}
	opener := &fakeAxisOpener{reg: reg}
	grid := paneviewtest.NewViews(logger)
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("driver registry: %v", err)
	}
	watch := paneobserve.New(logger, grid.Store, rules, paneobserve.Config{})
	// A sweep does nothing at all until an emitter exists (paneobserve's own
	// doc on SetEmitter) — production binds the transport here; this stand's
	// tests read Snapshot directly, so a recording emitter is enough to make
	// Sweep commit a classification.
	watch.SetEmitter(func(paneobserve.Observation) {})
	enrol := newWorkerEnrolments(logger, reg)
	sup := &workerSupervisor{sessions: reg, log: logger}
	spawner := &workerSpawner{
		layout:     tabs,
		opener:     opener,
		sessions:   reg,
		enrolments: enrol,
		readiness:  watch,
		workspace:  "ws-test",
		log:        logger,
	}
	return &taskDeliveryStand{
		reg: reg, ptys: ptys, tabs: tabs, opener: opener,
		grid: grid, rules: rules, watch: watch, enrol: enrol, spawner: spawner,
		sup: sup, log: logger,
	}
}

// enrolWorkerPane opens the pane's grid and observation exactly as
// paneenrol.go's Enrol does for a real agent_enrol — the two effects of one
// act, in that order.
func (s *taskDeliveryStand) enrolWorkerPane(t *testing.T, sid session.ID) {
	t.Helper()
	if err := s.grid.Watch(string(sid), participantCols, participantRows); err != nil {
		t.Fatalf("enrol the worker's pane: %v", err)
	}
	t.Cleanup(func() { s.grid.Withdraw(string(sid)) })
	s.watch.Watch(string(sid), wakeAgent)
}

// feedCapture replays name's own bytes into sid's grid up to atMs, the same
// corpus worker_wake_test.go's driveTo and verifiedClaude use, so the frame
// this typist reads is a screen the shipped rule was written against.
func (s *taskDeliveryStand) feedCapture(t *testing.T, sid session.ID, name string, atMs int64) {
	t.Helper()
	//nolint:gosec // The path is joined literals naming a corpus in the tree.
	header, chunks, err := agentcapture.Read(
		filepath.Join("..", "agentdriver", "testdata", "captures", name+".jsonl"))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if header.Cols != participantCols || header.Rows != participantRows {
		t.Fatalf("the capture is %dx%d and the pane is %dx%d; the frame would be wrapped differently",
			header.Cols, header.Rows, participantCols, participantRows)
	}
	through := agentcapture.ChunksThrough(chunks, atMs, 0)
	for _, c := range chunks[:through] {
		s.grid.Feed(string(sid), []byte(c.Data))
	}
}

// waitForNewSession blocks until a session other than any already known
// appears in the registry — the same pattern worker_spawn_axis_test.go and
// worker_wake_test.go's register use, because the pane's own id is minted
// inside Spawn and cannot be predicted by the caller.
func waitForNewSession(t *testing.T, reg *session.Reg) session.ID {
	t.Helper()
	var sid session.ID
	waittest.WaitFor(t, "the participant's session to exist", func() bool {
		for _, s := range reg.List() {
			sid = s.ID()
			return true
		}
		return false
	})
	return sid
}

// fixedStateReadiness answers every Snapshot with the SAME state, forever —
// what a pane looks like when nocx is reading it just fine and it simply
// never reaches free_text (state=working), or when the driver never
// recognises its screen at all (state=unknown, nocx-qddv8). fakePaneReadiness
// above cannot express either: its ready/not-ready pair only ever answers
// free_text or "not observed".
type fixedStateReadiness struct{ state agentdriver.State }

func (f fixedStateReadiness) Snapshot(string) (paneobserve.Observation, bool) {
	return paneobserve.Observation{State: f.state}, true
}

// neverObservedReadiness never answers for any pane at all — the grid
// genuinely has no reading for it, as distinct from fixedStateReadiness's
// "observed, and stuck".
type neverObservedReadiness struct{}

func (neverObservedReadiness) Snapshot(string) (paneobserve.Observation, bool) {
	return paneobserve.Observation{}, false
}

// newBufferLogger is a log.Logger that writes to an in-memory buffer, so a
// test can assert on the STRUCTURED log line awaitFreeText's own refusal
// writes, beside the error text a coordinator reads — the two are asserted
// separately because they serve different readers (workers.go's
// refusePaneNeverTypable).
func newBufferLogger(lvl slog.Level) (*log.SlogAdapter, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: lvl})
	return log.NewSlogAdapter(slog.New(h)), &buf
}

// ctxSpyTabs and hangingTabs are declared in worker_late_failure_test.go and
// used here too: both files are package app, and the compensation bound this
// bead adds (killContext, workers.go) is exercised from both the early-failure
// path (compensateSpawn, here) and the late-failure path (Registrar.compensate,
// there) — one double serves both rather than two that could drift.

// hangingTabs blocks DeleteTab until its own ctx ends, to prove Kill's own
// compensation bound (killTimeout, workers.go) — not the caller's, which by
// the time a compensation runs may already be expired or long gone — is what
// ends a compensation whose store hangs (AGENTS.md testing rule 3: every
// external call gets a test where it fails, and a call that never returns is
// the sharpest version of that).
type hangingTabs struct{}

func (hangingTabs) CreateTabAfter(_ context.Context, _ content.Tab, _ content.Pane, _ string) (content.Created[content.NewTab], error) {
	return content.Created[content.NewTab]{}, nil
}

// TabForPane answers nothing, which is the anchorless case: no tab is named,
// so the participant's tab goes last and no second method can hang.
func (hangingTabs) TabForPane(context.Context, string) (string, error) { return "", nil }

func (hangingTabs) DeleteTab(ctx context.Context, _ string, _ content.Replacement) error {
	<-ctx.Done()
	return ctx.Err()
}

// PaneCwd does not hang: this double exists for the DeleteTab bound, and a
// second hanging method would only make which one a test is measuring
// ambiguous.
func (hangingTabs) PaneCwd(context.Context, string) (string, error) { return "", nil }

// Criterion: a spawn whose pane becomes free_text succeeds, live, with its
// task delivery reported as the zero value — workerSpawner.Spawn no longer
// types anything itself (design §9, Task 11: the actual paste and Enter run
// later, through internal/workers.Registrar's TaskQueue, once this
// participant is live — see workers.TaskQueue's own doc for why that cannot
// happen from inside Spawn). This test only has workerSpawner in isolation
// (no Registrar, no TaskQueue), so it asserts what THIS layer does: nothing
// is written to the pty beyond the command line. The end-to-end delivery —
// the task actually reaching the pane once it is free — is
// internal/workers/registrar_taskqueue_test.go's
// TestRegisterEnqueuesTheTaskExactlyOnceAfterTheParticipantIsLive (the hook
// fires) and internal/app/pane_messages_test.go's
// TestAFreeMessageIsDeliveredWhenTheAgentIsFree (the queue delivers it).
func TestASpawnWhosePaneBecomesFreeTextReportsNoDeliveryOfItsOwn(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "please leave a summary of what you read when you are done"

	type outcome struct {
		sp  workers.Spawned
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		sp, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
			Participant: "p-deliver", Group: "worker-1", Task: task, Command: "claude",
		})
		done <- outcome{sp, err}
	}()

	sid := waitForNewSession(t, stand.reg)
	stand.enrolWorkerPane(t, sid)
	stand.feedCapture(t, sid, "claude-idle", 11000)
	waittest.WaitFor(t, "the worker's pane to be classified free_text", func() bool {
		stand.watch.Sweep()
		o, ok := stand.watch.Snapshot(string(sid))
		return ok && o.State == agentdriver.StateFreeText
	})

	got := <-done
	if got.err != nil {
		t.Fatalf("Spawn: %v", got.err)
	}
	if got.sp == nil {
		t.Fatal("Spawn returned no participant on a pane that reached free_text")
	}
	deliverer, ok := got.sp.(workers.TaskDeliverer)
	if !ok {
		t.Fatalf("the spawned participant (%T) does not say what became of its task", got.sp)
	}
	if d := deliverer.TaskDelivery(); d != (workers.TaskDelivery{}) {
		t.Fatalf("delivery = %+v, want the zero value: Spawn no longer types the task itself", d)
	}

	full := stand.ptys.last().read()
	if strings.Contains(full, task) {
		t.Fatalf("the task reached the pty from Spawn alone, with no Registrar/TaskQueue wired: %q", full)
	}
	if cmdIdx := strings.Index(full, "claude\n"); cmdIdx < 0 {
		t.Fatalf("the command line never reached the pty: %q", full)
	}
}

// Criterion: a pane that never becomes typable inside the budget fails the
// spawn, compensates (no tab, no session, no participant), and says it was
// the WAIT that failed rather than the submit.
func TestASpawnWhosePaneNeverBecomesTypableFailsAndCompensates(t *testing.T) {
	stand := newTaskDeliveryStand(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := stand.spawner.Spawn(ctx, workers.SpawnRequest{
		Participant: "p-never-typable", Group: "worker-1", Task: "do the thing", Command: "claude",
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Spawn succeeded for a pane nocx never watched, let alone one that became typable")
	}
	if elapsed > time.Second {
		t.Fatalf("Spawn took %s to give up on a pane that never becomes typable; it should stop at the given ctx bound", elapsed)
	}
	if !errors.Is(err, workers.ErrPaneNeverTypable) {
		t.Fatalf("error %v does not wrap ErrPaneNeverTypable", err)
	}
	if errors.Is(err, workers.ErrTaskSubmitRefused) {
		t.Fatalf("error %v reads like the submit-refused case; the two need different fixes and different sentences", err)
	}

	created, deleted := stand.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 1 || created[0] != deleted[0] {
		t.Fatalf("tabs created=%v deleted=%v, want the one created tab deleted and nothing else", created, deleted)
	}
	if sid := stand.opener.lastSessionID(); sid != "" {
		if _, getErr := stand.reg.Get(sid); getErr == nil {
			t.Fatalf("session %s is still in the registry after a never-typable refusal", sid)
		}
	}
}

// TestASubmitTheRealGateRefusesIsNotDefeatedByAFalseReadySignal and
// TestOutcomeTypedFromDeliveryIsARefusalNotADelivery used to assert that
// deliverTask's own call into agenttyping.Typist.Submit could not be
// defeated by a caller's wrong belief that a pane was ready, and that
// OutcomeTyped (the text landed, the submit key did not) is a refusal and
// not a delivery. deliverTask no longer calls Submit at all (design §9, Task
// 11: delivery is now internal/workers.Registrar's TaskQueue, over
// PaneKeys/PaneMessages, never agenttyping) — those two properties are
// re-expressed where the mechanisms that still exist actually own them:
//
//   - "a caller's belief that a pane is ready is not enough to write into
//     it" is agenttyping.Typist.Submit's own re-verification guarantee,
//     asserted directly against the real gate in internal/agenttyping's own
//     suite (TestAScreenThatChangesAfterTheTextStopsTheSubmitKey,
//     TestAWriteIsRefusedWhenTheRuntimeCannotVouchForTheScreen) — untouched
//     by this bead, since Submit itself is unchanged and workerWaker
//     (workers.go) still reaches it for a wake.
//   - "a step that only moves the box without confirming is not a
//     submission" is deliverOne's own Enter-step handling
//     (internal/app/pane_messages.go): an Enter whose result is not
//     "executed" commits PhasePartial, never PhaseSubmitted — covered by
//     internal/app/pane_messages_test.go's TestAMenuBetweenPasteAndEnterRefusesTheEnter
//     and the general deliverOne path TestAFreeMessageIsDeliveredWhenTheAgentIsFree
//     exercises for the confirmed case.

// Criterion: a spawner nobody wired a readiness seam into proceeds without
// attempting to confirm the pane became typable, exactly like
// integrationAwaiterSeam's own absence case — the axis-gate and tab-bookkeeping
// tests in worker_spawn_axis_test.go depend on this staying true.
func TestASpawnerWithNoReadinessSeamProceedsWithoutConfirmingTypability(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	stand.spawner.readiness = nil

	sp, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-no-seam", Group: "worker-1", Task: "do the thing", Command: "claude",
	})
	if err != nil {
		t.Fatalf("Spawn refused a request with no typing seam wired at all: %v", err)
	}
	if sp == nil {
		t.Fatal("Spawn returned no participant")
	}
	pty := stand.ptys.last()
	waittest.WaitFor(t, "the command line to reach the pty", func() bool {
		return pty.read() == "claude\n"
	})
}

// Criterion: a spawn whose task delivery fails compensates COMPLETELY even
// when the CALLER's own context is already done by the time compensation
// runs (nocx-4gj5w). On the live stand this bead was filed from,
// awaitFreeText spent the whole of Spawn's own budget waiting, so the ctx
// compensateSpawn was handed was already context.DeadlineExceeded the moment
// it mattered, and DeleteTab failed with exactly that error — leaving the
// tab and its session behind for a person to find and a coordinator to close
// by hand. Assert all three: no tab, no session, no live participant — and,
// the part fakeAxisTabs cannot prove because it never looks at ctx at all,
// that DeleteTab itself was handed a context that was NOT already done.
func TestASpawnCompensatesCompletelyEvenWithAnAlreadyExpiredContext(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	spy := &ctxSpyTabs{}
	stand.spawner.layout = spy

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := stand.spawner.Spawn(ctx, workers.SpawnRequest{
		Participant: "p-expired-ctx", Group: "worker-1", Task: "do the thing", Command: "claude",
	})
	if err == nil {
		t.Fatal("Spawn succeeded for a pane nocx never watched, let alone one that became typable")
	}
	if ctx.Err() == nil {
		t.Fatal("test setup: the caller's own context should already be done by the time Spawn returns")
	}

	created, deleted := spy.snapshot()
	if len(created) != 1 || len(deleted) != 1 || created[0] != deleted[0] {
		t.Fatalf("tabs created=%v deleted=%v, want the one created tab deleted and nothing else", created, deleted)
	}
	if len(spy.deleteCtxDone) != 1 || spy.deleteCtxDone[0] {
		t.Fatalf("DeleteTab was handed a context that was already done (%v); "+
			"Kill's own compensation context must not inherit the caller's expired one", spy.deleteCtxDone)
	}
	if sid := stand.opener.lastSessionID(); sid != "" {
		if _, getErr := stand.reg.Get(sid); getErr == nil {
			t.Fatalf("session %s is still in the registry after compensating with an expired context", sid)
		}
	}
}

// Criterion: the compensation's own bound applies (AGENTS.md testing rule 3:
// every external call gets a test where it fails, and a hang is the sharpest
// version of that). A store whose DeleteTab hangs must not be able to hang
// the spawn — killTimeout, not the caller's ctx, is what has to end it.
func TestASpawnsCompensationIsNotHungByAHangingStore(t *testing.T) {
	orig := killTimeout
	killTimeout = 50 * time.Millisecond
	t.Cleanup(func() { killTimeout = orig })

	stand := newTaskDeliveryStand(t)
	stand.spawner.layout = hangingTabs{}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := stand.spawner.Spawn(ctx, workers.SpawnRequest{
		Participant: "p-hanging-store", Group: "worker-1", Task: "do the thing", Command: "claude",
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Spawn succeeded despite a pane that never becomes typable")
	}
	if elapsed > time.Second {
		t.Fatalf("Spawn took %s to return; a hanging compensation store must not be able to hang it (killTimeout=%s)", elapsed, killTimeout)
	}
	if sid := stand.opener.lastSessionID(); sid != "" {
		if _, getErr := stand.reg.Get(sid); getErr == nil {
			t.Fatalf("session %s is still in the registry after a compensation whose store hung", sid)
		}
	}
}

// Criterion: a pane the driver reads just fine, that simply never reaches
// free_text, names its last state and says nothing about the driver failing
// to recognise the screen — that sentence belongs only to StateUnknown.
func TestAPaneStuckWorkingForTheWholeBudgetNamesItsLastState(t *testing.T) {
	logger, buf := newBufferLogger(slog.LevelDebug)
	stand := newTaskDeliveryStand(t)
	stand.spawner.readiness = fixedStateReadiness{state: agentdriver.StateWorking}
	stand.spawner.log = logger

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := stand.spawner.deliverTask(ctx, "pane-working", "do the thing")
	if err == nil {
		t.Fatal("deliverTask succeeded for a pane stuck at working")
	}
	if !errors.Is(err, workers.ErrPaneNeverTypable) {
		t.Fatalf("error %v does not wrap ErrPaneNeverTypable", err)
	}
	if !strings.Contains(err.Error(), `"working"`) {
		t.Fatalf("error %q does not name the pane's last state", err)
	}
	if strings.Contains(err.Error(), "did not recognise") {
		t.Fatalf("error %q reads like the unknown case; a screen the driver read fine must not say that", err)
	}
	if got := buf.String(); !strings.Contains(got, "last_state=working") || !strings.Contains(got, "held_ms=") {
		t.Fatalf("log line = %q, want it to name last_state=working and a held_ms duration", got)
	}
}

// Criterion: `unknown` for the whole budget is a DIFFERENT sentence from
// `working` for the whole budget (nocx-qddv8, nocx-4gj5w) — a rule that
// cannot identify the screen is a different failure from an agent that is
// simply busy, and only the former will never resolve on its own.
func TestAPaneStuckUnknownForTheWholeBudgetNamesTheUnrecognisedScreen(t *testing.T) {
	logger, buf := newBufferLogger(slog.LevelDebug)
	stand := newTaskDeliveryStand(t)
	stand.spawner.readiness = fixedStateReadiness{state: agentdriver.StateUnknown}
	stand.spawner.log = logger

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := stand.spawner.deliverTask(ctx, "pane-unknown", "do the thing")
	if err == nil {
		t.Fatal("deliverTask succeeded for a pane stuck at unknown")
	}
	if !errors.Is(err, workers.ErrPaneNeverTypable) {
		t.Fatalf("error %v does not wrap ErrPaneNeverTypable", err)
	}
	if !strings.Contains(err.Error(), "did not recognise") {
		t.Fatalf("error %q does not say the driver could not recognise the screen", err)
	}
	if got := buf.String(); !strings.Contains(got, "last_state=unknown") {
		t.Fatalf("log line = %q, want it to name last_state=unknown", got)
	}
}

// Criterion: a pane nocx never observed at all says exactly that, and names
// no state — the grid not being fed is a third failure, distinct from both
// of the above (nocx-4gj5w).
func TestAPaneNeverObservedAtAllSaysSoRatherThanNamingAState(t *testing.T) {
	logger, buf := newBufferLogger(slog.LevelDebug)
	stand := newTaskDeliveryStand(t)
	stand.spawner.readiness = neverObservedReadiness{}
	stand.spawner.log = logger

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := stand.spawner.deliverTask(ctx, "pane-unwatched", "do the thing")
	if err == nil {
		t.Fatal("deliverTask succeeded for a pane nocx never observed")
	}
	if !errors.Is(err, workers.ErrPaneNeverTypable) {
		t.Fatalf("error %v does not wrap ErrPaneNeverTypable", err)
	}
	if !strings.Contains(err.Error(), "never observed") {
		t.Fatalf("error %q does not say the pane was never observed", err)
	}
	if strings.Contains(err.Error(), "held state") {
		t.Fatalf("error %q names a state that was never seen", err)
	}
	if got := buf.String(); !strings.Contains(got, "no observation ever recorded") {
		t.Fatalf("log line = %q, want it to say no observation was ever recorded", got)
	}
}
