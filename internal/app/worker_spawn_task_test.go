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
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// taskDeliveryStand is a real session registry over stub ptys, a recorded-
// calls tab store, and the real observation + typing seams a production
// spawner is wired to (app.go's readiness: paneWatch, typist: paneTyping) —
// so what is asserted here is what the shipped gate does, not what a double
// of it was told to do.
type taskDeliveryStand struct {
	reg     *session.Reg
	ptys    *workerTestPTYFactory
	tabs    *fakeAxisTabs
	opener  *fakeAxisOpener
	grid    *panegrid.Store
	rules   *agentdriver.Registry
	watch   *paneobserve.Watcher
	enrol   *workerEnrolments
	spawner *workerSpawner
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
	grid := panegrid.New(logger)
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("driver registry: %v", err)
	}
	watch := paneobserve.New(logger, grid, rules)
	// A sweep does nothing at all until an emitter exists (paneobserve's own
	// doc on SetEmitter) — production binds the transport here; this stand's
	// tests read Snapshot directly, so a recording emitter is enough to make
	// Sweep commit a classification.
	watch.SetEmitter(func(paneobserve.Observation) {})
	typist := newPaneTypist(logger, grid, rules, verifiedClaude(t), watch, reg)
	enrol := newWorkerEnrolments(logger, reg)
	spawner := &workerSpawner{
		layout:     tabs,
		opener:     opener,
		sessions:   reg,
		enrolments: enrol,
		readiness:  watch,
		typist:     typist,
		workspace:  "ws-test",
		log:        logger,
	}
	return &taskDeliveryStand{
		reg: reg, ptys: ptys, tabs: tabs, opener: opener,
		grid: grid, rules: rules, watch: watch, enrol: enrol, spawner: spawner,
	}
}

// enrolWorkerPane opens the pane's grid and observation exactly as
// paneenrol.go's Enrol does for a real agent_enrol — the two effects of one
// act, in that order.
func (s *taskDeliveryStand) enrolWorkerPane(t *testing.T, sid session.ID) {
	t.Helper()
	if err := s.grid.Enrol(string(sid), participantCols, participantRows); err != nil {
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

// fakePaneReadiness answers instantly, and its ready flag lies on purpose in
// TestASubmitTheRealGateRefusesIsNotDefeatedByAFalseReadySignal — the whole
// point of that test is that a caller's own (wrong) belief that a pane is
// ready must not be enough to get bytes past agenttyping's own re-check.
type fakePaneReadiness struct{ ready bool }

func (f fakePaneReadiness) Snapshot(string) (paneobserve.Observation, bool) {
	if !f.ready {
		return paneobserve.Observation{}, false
	}
	return paneobserve.Observation{State: agentdriver.StateFreeText}, true
}

// fixedOutcomeTypist answers every Submit with a canned Result, for asserting
// how workerSpawner reacts to an outcome without needing the real gate to
// produce it.
type fixedOutcomeTypist struct{ res agenttyping.Result }

func (f fixedOutcomeTypist) Submit(string, string) agenttyping.Result { return f.res }

// Criterion: a spawn whose pane becomes free_text submits the task text into
// that pane, AFTER the command line and never in the same write.
func TestASpawnWhosePaneBecomesFreeTextDeliversTheTask(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "please write to NOCX_AGENT_REPORT when you are done"

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

	pty := stand.ptys.last()
	waittest.WaitFor(t, "the task text to reach the pty", func() bool {
		return strings.Contains(pty.read(), task)
	})
	full := pty.read()
	cmdIdx := strings.Index(full, "claude\n")
	taskIdx := strings.Index(full, task)
	if cmdIdx < 0 {
		t.Fatalf("the command line never reached the pty: %q", full)
	}
	if taskIdx < 0 || taskIdx < cmdIdx {
		t.Fatalf("the task did not arrive strictly after the command line: %q", full)
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

// Criterion: the input seam decides, not the caller's own belief about
// readiness. readiness here LIES and says the pane is free_text the instant
// it is asked; the real grid shows the worker's agent asking for a permission
// approval. The real gate must still refuse, and — the part a log line cannot
// prove — nothing at all reaches the session's own pty.
//
// This exercises deliverTask directly, against a session opened and enrolled
// up front, rather than going through the full Spawn: the fake readiness
// answers instantly, and racing it against Spawn's own session-minting and
// compensation (which closes the session the moment the refusal is seen) left
// nothing for a concurrent enrolment step to enrol before the call was
// already over. Nothing here is faked past that: the grid, the rules, the
// calibration and the typist are the same real gate agent.type and a wake
// reach, and deliverTask is the same method Spawn calls.
func TestASubmitTheRealGateRefusesIsNotDefeatedByAFalseReadySignal(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	stand.spawner.readiness = fakePaneReadiness{ready: true}

	sess, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: participantCols, Rows: participantRows,
	})
	if err != nil {
		t.Fatalf("open a session for the pane: %v", err)
	}
	sid := sess.ID()
	stand.enrolWorkerPane(t, sid)
	stand.feedCapture(t, sid, "claude-permission", 49000)
	f, ferr := stand.grid.Frame(string(sid))
	if ferr != nil || stand.rules.Classify(wakeAgent, f) != agentdriver.StatePermissionChoice {
		t.Fatalf("test setup did not reach a permission menu: frame err=%v state=%v",
			ferr, stand.rules.Classify(wakeAgent, f))
	}

	deliverErr := stand.spawner.deliverTask(context.Background(), string(sid), "do the thing")
	if deliverErr == nil {
		t.Fatal("deliverTask succeeded even though the real gate should have refused the submit")
	}
	if !errors.Is(deliverErr, workers.ErrTaskSubmitRefused) {
		t.Fatalf("error %v does not wrap ErrTaskSubmitRefused", deliverErr)
	}
	if errors.Is(deliverErr, workers.ErrPaneNeverTypable) {
		t.Fatalf("error %v reads like the never-typable case; a false ready signal must not produce that sentence", deliverErr)
	}

	// THE INPUT SEAM, not a log line: nothing reached the pty at all — a
	// refused grant never calls Accept, so there is no write in flight to
	// race by waiting.
	pty := stand.ptys.last()
	if full := pty.read(); full != "" {
		t.Fatalf("bytes reached the pty despite a real permission menu: %q", full)
	}
}

// Criterion: OutcomeTyped is a refusal, not a delivery, and the spawn's error
// carries the reason the submit failed.
func TestOutcomeTypedFromDeliveryIsARefusalNotADelivery(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	stand.spawner.readiness = fakePaneReadiness{ready: true}
	stand.spawner.typist = fixedOutcomeTypist{res: agenttyping.Result{
		Outcome: agenttyping.OutcomeTyped,
		Reason:  "the text reached the input region and the submit key did not",
	}}

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-typed-only", Group: "worker-1", Task: "do the thing", Command: "claude",
	})
	if err == nil {
		t.Fatal("Spawn succeeded on an OutcomeTyped submission, which starts no turn")
	}
	if !errors.Is(err, workers.ErrTaskSubmitRefused) {
		t.Fatalf("error %v does not wrap ErrTaskSubmitRefused", err)
	}
	if !strings.Contains(err.Error(), "submit key did not") {
		t.Fatalf("error %q does not carry the reason the submit failed", err)
	}

	created, deleted := stand.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 1 || created[0] != deleted[0] {
		t.Fatalf("tabs created=%v deleted=%v, want the one created tab deleted and nothing else", created, deleted)
	}
}

// Criterion: a spawner nobody wired a typing seam into (readiness and typist
// both nil) proceeds without attempting delivery, exactly like
// integrationAwaiterSeam's own absence case — the axis-gate and tab-bookkeeping
// tests in worker_spawn_axis_test.go depend on this staying true.
func TestASpawnerWithNoTypingSeamProceedsWithoutDeliveringTheTask(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	stand.spawner.readiness = nil
	stand.spawner.typist = nil

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
