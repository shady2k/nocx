package paneobserve_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
)

type recorder struct {
	mu   sync.Mutex
	seen []paneobserve.Observation
}

func (r *recorder) emit(o paneobserve.Observation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, o)
}

func (r *recorder) drain() []paneobserve.Observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.seen
	r.seen = nil
	return out
}

// A screen with the chrome the Claude driver reads: two full-width rules
// around a row that opens with the input marker, and a mode line under it.
func idleScreen(cols int) string {
	rule := strings.Repeat("─", cols)
	return "\x1b[2J\x1b[7;1H              0 tokens" +
		"\x1b[8;1H" + rule + "\x1b[9;1H❯ \x1b[10;1H" + rule +
		"\x1b[12;1H  ⏵⏵ auto mode on\x1b[9;3H"
}

func workingScreen(cols int) string {
	// One row above the token meter, which is where the status stack starts.
	return idleScreen(cols) + "\x1b[6;1H* Ruminating… (3s)\x1b[9;3H"
}

func newFixture(t *testing.T) (*paneobserve.Watcher, *panegrid.Store, *recorder) {
	t.Helper()
	lg := log.NewSlogAdapter(nil)
	grid := panegrid.New(lg)
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	rec := &recorder{}
	w := paneobserve.New(lg, grid, reg)
	w.SetEmitter(rec.emit)
	return w, grid, rec
}

// The ordinary case, end to end through the real emulator: a watched pane
// showing an idle input box is reported as free text, once.
func TestAWatchedPaneIsReportedWhenItsScreenIsFirstRead(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()

	got := rec.drain()
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1: %+v", len(got), got)
	}
	if got[0].PaneID != "p1" || got[0].Agent != "claude" || got[0].State != agentdriver.StateFreeText {
		t.Errorf("observation = %+v, want p1/claude/free_text", got[0])
	}
}

// Only CHANGES are pushed. A pane whose screen has not moved is not news, and
// a renderer that receives an observation per sweep cannot tell a repaint from
// a state change.
func TestASweepWithNothingNewSaysNothing(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	w.Touch("p1")
	w.Sweep()
	w.Sweep()
	if got := rec.drain(); len(got) != 0 {
		t.Errorf("a pane that did not change was reported %d times: %+v", len(got), got)
	}
}

// And a real change is. This is the edge the indicator draws.
func TestAChangedScreenIsReportedAgain(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	grid.Feed("p1", []byte(workingScreen(40)))
	w.Touch("p1")
	w.Sweep()
	got := rec.drain()
	if len(got) != 1 || got[0].State != agentdriver.StateWorking {
		t.Fatalf("after the spinner appeared: %+v, want one working observation", got)
	}
}

// An agent nothing was written for is watched and answers unknown — which
// every caller treats as busy. It is reported rather than dropped, because a
// pane whose state nobody can read is exactly what the indicator has to say.
func TestAnAgentWithNoDriverIsReportedAsUnknown(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "some-other-agent")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()
	got := rec.drain()
	if len(got) != 1 || got[0].State != agentdriver.StateUnknown {
		t.Fatalf("unregistered agent: %+v, want one unknown observation", got)
	}
}

// The interval has both ends here too. After Unwatch the pane is not observed,
// and nothing remembers what it last was — so if it is watched again its first
// screen is news, rather than being compared against a previous incarnation's
// state.
func TestUnwatchEndsTheObservationAndForgetsTheLastState(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	w.Unwatch("p1")
	w.Touch("p1")
	w.Sweep()
	if got := rec.drain(); len(got) != 0 {
		t.Fatalf("an unwatched pane was still reported: %+v", got)
	}

	w.Watch("p1", "claude")
	w.Touch("p1")
	w.Sweep()
	got := rec.drain()
	if len(got) != 1 || got[0].State != agentdriver.StateFreeText {
		t.Fatalf("re-watched pane: %+v, want its state reported afresh", got)
	}
}

// Every external call this code makes has a test where it fails. The grid is
// the external call, and a pane whose grid has gone — the session ended while
// a sweep was in flight — is silent rather than reported as anything.
func TestAPaneWhoseGridHasGoneIsNotReported(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	grid.Withdraw("p1") // the session ended; the watcher has not been told yet
	w.Touch("p1")
	w.Sweep()
	if got := rec.drain(); len(got) != 0 {
		t.Fatalf("a pane with no grid was reported: %+v", got)
	}
}

// Touch is on the hot path of every session in the product. A pane nobody
// watches must cost nothing and must never reach the grid.
func TestTouchingAnUnwatchedPaneIsSilent(t *testing.T) {
	w, _, rec := newFixture(t)
	w.Touch("never-watched")
	w.Sweep()
	if got := rec.drain(); len(got) != 0 {
		t.Fatalf("an unwatched pane produced %+v", got)
	}
}

// Snapshot is what a reattaching client is answered with: a state, not an
// event. Without it a renderer that reconnects after the last change learns
// nothing until the next one, which for a settled idle pane is never.
func TestSnapshotAnswersTheCurrentStateForAReattachingClient(t *testing.T) {
	w, grid, _ := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()

	o, ok := w.Snapshot("p1")
	if !ok {
		t.Fatal("a watched pane has no snapshot")
	}
	if o.State != agentdriver.StateFreeText || o.Agent != "claude" {
		t.Errorf("snapshot = %+v, want claude/free_text", o)
	}
	if _, ok := w.Snapshot("p2"); ok {
		t.Error("an unwatched pane answered a snapshot")
	}
}

// A watcher with nowhere to report does not quietly consume the state it would
// have sent. The window between construction and SetEmitter is real — the
// transport is built after the publisher that enrols into this — and a sweep
// landing in it must not leave the pane looking already-reported.
func TestASweepWithNoEmitterYetLosesNothing(t *testing.T) {
	lg := log.NewSlogAdapter(nil)
	grid := panegrid.New(lg)
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	w := paneobserve.New(lg, grid, reg)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()

	rec := &recorder{}
	w.SetEmitter(rec.emit)
	w.Sweep()
	got := rec.drain()
	if len(got) != 1 || got[0].State != agentdriver.StateFreeText {
		t.Fatalf("after the destination existed: %+v, want the pane's state reported once", got)
	}
}

// The agent's own withdrawal is the one moment nocx knows a process is gone,
// and it is a fact about the PROCESS rather than about the screen — which is
// exactly why no driver may return it and why it has to be supplied here.
//
// Without this, a worker that finished simply stopped being reported and its
// tab fell back to whatever its title last said, which for an agent that exits
// without repainting is "working" forever.
func TestAnAgentThatExitedIsReportedAsExited(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	w.Exited("p1")
	got := rec.drain()
	if len(got) != 1 || got[0].State != agentdriver.StateExited {
		t.Fatalf("after the agent exited: %+v, want one exited observation", got)
	}
}

// And it is TERMINAL. The pane's screen does not stop moving when its agent
// leaves — the shell is still there and still repainting — and re-classifying
// what is left would report the shell's prompt as an agent waiting for input.
func TestAnExitedPaneIsNotClassifiedAgain(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	w.Exited("p1")
	rec.drain()

	grid.Feed("p1", []byte(idleScreen(40)))
	w.Touch("p1")
	w.Sweep()
	if got := rec.drain(); len(got) != 0 {
		t.Fatalf("an exited pane was classified again: %+v", got)
	}
	// And a client attaching afterwards is told the pane's last state rather
	// than nothing at all.
	o, ok := w.Snapshot("p1")
	if !ok || o.State != agentdriver.StateExited {
		t.Fatalf("snapshot after exit = %+v/%v, want an exited observation", o, ok)
	}
}

// Exiting twice says it once: the enroller's withdrawal and a session teardown
// can race, and a pane cannot exit again.
func TestExitingTwiceIsReportedOnce(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	w.Exited("p1")
	w.Exited("p1")
	if got := rec.drain(); len(got) != 1 {
		t.Fatalf("observations = %d, want 1: %+v", len(got), got)
	}
}

// A pane nobody watches did not exit — it was never observed. Saying it did
// would be a claim with no evidence behind it.
func TestAnUnwatchedPaneCannotExit(t *testing.T) {
	w, _, rec := newFixture(t)
	w.Exited("never-watched")
	if got := rec.drain(); len(got) != 0 {
		t.Fatalf("an unwatched pane reported %+v", got)
	}
}
