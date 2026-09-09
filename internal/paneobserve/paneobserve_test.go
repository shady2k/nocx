package paneobserve_test

import (
	"fmt"
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

// The task panel, drawn where claude draws it: below the mode line, the pane's
// OWN row first and one row per child under it. Painted onto the same idle
// chrome, because that is the trap — a backgrounded agent keeps the input box
// live and shows no spinner at all.
func panelScreen(cols int, children ...string) string {
	out := idleScreen(cols) + "\x1b[14;1H  ● main"
	for i, row := range children {
		out += fmt.Sprintf("\x1b[%d;1H  ◯ %s", 15+i, row)
	}
	return out + "\x1b[9;3H"
}

// The panel's other end, and the one herdr regressed on twice: the panel has
// collapsed and the children are gone from it, but the mode line says they are
// still alive. The pane is still working and the rows are simply no longer
// nameable.
func collapsedPanelScreen(cols int) string {
	return "\x1b[2J\x1b[7;1H              0 tokens" +
		"\x1b[8;1H" + strings.Repeat("─", cols) + "\x1b[9;1H❯ \x1b[10;1H" + strings.Repeat("─", cols) +
		"\x1b[12;1H  ⏵⏵ auto mode on · /tasks to see subagents\x1b[9;3H"
}

// childNames is what a reader of the wire would see, in order.
func childNames(o paneobserve.Observation) []string {
	out := make([]string, 0, len(o.Children))
	for _, c := range o.Children {
		out = append(out, c.Name)
	}
	return out
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

// ── The child rows, and the seam that carries them (nocx-o1v0h) ───────────

// The whole point: the panel's child rows travel with the observation, so a
// person watching a pane whose agent spawned children can be shown a row per
// child with its name and what it is doing. Read off the grid, with no vendor
// hook anywhere in the picture.
func TestTheChildRowsTravelWithTheObservation(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 60, 18); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(panelScreen(60, "Explore  List files in directory")))
	w.Touch("p1")
	w.Sweep()

	got := rec.drain()
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1: %+v", len(got), got)
	}
	if got[0].State != agentdriver.StateWorking {
		t.Errorf("state = %q, want %q", got[0].State, agentdriver.StateWorking)
	}
	want := []agentdriver.Subagent{{Name: "Explore", Task: "List files in directory"}}
	if len(got[0].Children) != 1 || got[0].Children[0] != want[0] {
		t.Fatalf("children = %+v, want %+v", got[0].Children, want)
	}
}

// THE EMIT SEAM, stated with both ends.
//
// A child row is on the wire from the first sweep in which the panel names it
// until the first sweep in which the panel does not — and nothing about a row
// that is still there is ever news again. That holds because what crosses is
// only what is stable for the life of a row: the elapsed time and the token
// flow move on every frame and are deliberately not carried (agentdriver.
// Subagent). So "emit when the answer changed" needs no exception for them,
// and a pane repainting its clock eight times a second says nothing at all.
func TestARepaintThatOnlyMovesAChildsClockIsNotNews(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 80, 18); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(panelScreen(80, "Explore  List files in directory                  7s · ↓ 11.6k tokens")))
	w.Touch("p1")
	w.Sweep()
	if got := rec.drain(); len(got) != 1 || len(got[0].Children) != 1 {
		t.Fatalf("first sweep = %+v, want one observation carrying one child", got)
	}

	// The same panel, four frames later. Everything the screen changed is a
	// measurement, and the driver reads all of it — the extractor's own test
	// asserts the elapsed time follows the screen.
	for _, elapsed := range []string{"8s", "9s", "10s", "11s"} {
		grid.Feed("p1", []byte(panelScreen(80, "Explore  List files in directory                 "+elapsed+" · ↓ 11.9k tokens")))
		w.Touch("p1")
		w.Sweep()
	}
	if got := rec.drain(); len(got) != 0 {
		t.Fatalf("a moving clock was reported %d times: %+v", len(got), got)
	}
}

// The other half of the same interval: the SET moving is news, in both
// directions. Without this the rows would be drawn once and then lie.
func TestAChildAppearingAndVanishingAreBothNews(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 60, 18); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(panelScreen(60, "Explore  List files")))
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	grid.Feed("p1", []byte(panelScreen(60, "Explore  List files", "Plan  Draft the change")))
	w.Touch("p1")
	w.Sweep()
	got := rec.drain()
	if len(got) != 1 {
		t.Fatalf("a second child appearing was reported %d times: %+v", len(got), got)
	}
	if names := childNames(got[0]); len(names) != 2 || names[0] != "Explore" || names[1] != "Plan" {
		t.Fatalf("children = %v, want [Explore Plan] in panel order", names)
	}

	grid.Feed("p1", []byte(panelScreen(60, "Plan  Draft the change")))
	w.Touch("p1")
	w.Sweep()
	got = rec.drain()
	if len(got) != 1 {
		t.Fatalf("a child vanishing was reported %d times: %+v", len(got), got)
	}
	if names := childNames(got[0]); len(names) != 1 || names[0] != "Plan" {
		t.Fatalf("children after one finished = %v, want [Plan]", names)
	}
}

// A CHILD FACT MAY NEVER DECIDE THE PARENT'S STATE.
//
// Bought twice elsewhere and carried over deliberately: herdr's claude hook
// discards subagent events by name because SubagentStop can arrive after the
// main turn already stopped and would never let an idle pane revive; orca's
// parent flips to done while its children still run.
//
// Two things are asserted, and neither is the same claim. First, that the
// state the watcher emits is byte-for-byte what the registry answers for the
// same frame — the children reach the wire beside the verdict and never
// through it. Second, the sharp middle cases: the panel gaining a second
// child, and its child being replaced by a differently-named one, are news for
// the rows and silence for the state.
//
// The panel's PRESENCE deciding "working" is a different thing and is correct:
// that is a branch reading the pane's own chrome, which is the only evidence
// on screen that a backgrounded agent is running at all. What may not happen
// is a row's CONTENT moving the answer.
func TestAChildAppearingOrVanishingDoesNotChangeTheParentsState(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 60, 18); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")

	grid.Feed("p1", []byte(panelScreen(60, "Explore  List files")))
	w.Touch("p1")
	w.Sweep()
	first := rec.drain()
	if len(first) != 1 || first[0].State != agentdriver.StateWorking {
		t.Fatalf("panel drawn = %+v, want one working observation", first)
	}

	for _, step := range []struct {
		what   string
		screen string
	}{
		{"a second child appears", panelScreen(60, "Explore  List files", "Plan  Draft the change")},
		{"the first child finishes", panelScreen(60, "Plan  Draft the change")},
		{"the child is replaced", panelScreen(60, "Review  Read the diff")},
	} {
		grid.Feed("p1", []byte(step.screen))
		w.Touch("p1")
		w.Sweep()
		got := rec.drain()
		if len(got) != 1 {
			t.Fatalf("%s: reported %d times, want 1: %+v", step.what, len(got), got)
		}
		if got[0].State != agentdriver.StateWorking {
			t.Errorf("%s: state moved to %q; a child fact decided the parent", step.what, got[0].State)
		}
	}

	// And the end herdr regressed on: the panel collapses, so no row can be
	// named — and the pane does NOT go back to inviting input, because the
	// mode line still says a background agent is alive.
	grid.Feed("p1", []byte(collapsedPanelScreen(60)))
	w.Touch("p1")
	w.Sweep()
	got := rec.drain()
	if len(got) != 1 {
		t.Fatalf("the panel collapsing was reported %d times: %+v", len(got), got)
	}
	if got[0].State != agentdriver.StateWorking {
		t.Errorf("every child row vanished and the pane became %q", got[0].State)
	}
	if len(got[0].Children) != 0 {
		t.Errorf("a collapsed panel still named children: %+v", got[0].Children)
	}
}

// The snapshot carries them too, for the same reason it carries the state: a
// client that attaches after the change that produced it would otherwise see a
// pane with an agent working and no rows under it, and wait forever for a
// transition that already happened.
func TestSnapshotCarriesTheChildRows(t *testing.T) {
	w, grid, _ := newFixture(t)
	if err := grid.Enrol("p1", 60, 18); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(panelScreen(60, "Explore  List files in directory")))
	w.Touch("p1")
	w.Sweep()

	o, ok := w.Snapshot("p1")
	if !ok {
		t.Fatal("a watched pane has no snapshot")
	}
	if names := childNames(o); len(names) != 1 || names[0] != "Explore" {
		t.Fatalf("snapshot children = %v, want [Explore]", names)
	}
}

// An agent that exited has no children on screen, because it has no screen.
// The terminal observation says so rather than leaving the last rows standing
// under a pane whose process is gone.
func TestAnExitedPaneNamesNoChildren(t *testing.T) {
	w, grid, rec := newFixture(t)
	if err := grid.Enrol("p1", 60, 18); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	grid.Feed("p1", []byte(panelScreen(60, "Explore  List files")))
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	w.Exited("p1")
	got := rec.drain()
	if len(got) != 1 || got[0].State != agentdriver.StateExited {
		t.Fatalf("after exit = %+v, want one exited observation", got)
	}
	if len(got[0].Children) != 0 {
		t.Errorf("an exited pane still named children: %+v", got[0].Children)
	}
	o, _ := w.Snapshot("p1")
	if len(o.Children) != 0 {
		t.Errorf("the retained snapshot still names children: %+v", o.Children)
	}
}

// WHICH PANES NOCX IS WATCHING, AND AS WHAT (nocx-02uci).
//
// The emitting view has to read a pane's frame through the rule that actually
// governs it, and the only place that fact exists is the enrolment act. It is
// read here rather than taken from the caller because a caller that names the
// agent is a second owner of which rule a pane is under, and the two would
// disagree the first time a pane was re-enrolled.
func TestWatchingListsEnrolledPanesBeforeAnySweep(t *testing.T) {
	w, grid, _ := newFixture(t)
	for _, id := range []string{"p2", "p1"} {
		if err := grid.Enrol(id, 40, 14); err != nil {
			t.Fatalf("enrol %s: %v", id, err)
		}
		defer grid.Withdraw(id)
		w.Watch(id, "claude")
	}

	// Before any sweep, deliberately: Snapshot answers nothing until a pane
	// has been classified once, and a view that had to wait for that would
	// show a person nothing on a settled screen.
	got := w.Watching()
	if len(got) != 2 {
		t.Fatalf("Watching() = %+v, want two panes", got)
	}
	// Sorted, because a person reads this list and a map's order would
	// reshuffle it under them on every poll.
	if got[0].PaneID != "p1" || got[1].PaneID != "p2" {
		t.Fatalf("Watching() = %+v, want p1 before p2", got)
	}
	if got[0].Agent != "claude" || got[1].Agent != "claude" {
		t.Fatalf("Watching() = %+v, want each pane named as claude", got)
	}
}

// And the interval has the other end. A pane nobody watches is not listed, so
// the view cannot be pointed at one nocx is not observing.
func TestAnUnwatchedPaneIsNotListed(t *testing.T) {
	w, grid, _ := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	if got := w.Watching(); len(got) != 0 {
		t.Fatalf("Watching() = %+v before Watch; want nothing", got)
	}
	w.Watch("p1", "claude")
	w.Unwatch("p1")
	if got := w.Watching(); len(got) != 0 {
		t.Fatalf("Watching() = %+v after Unwatch; want nothing", got)
	}
}

// An exited agent is still the agent this pane was enrolled as, and the pane
// stays listed. The observation is retained for a client that attaches
// afterwards, and taking the last screen away from the person working out what
// happened on it is the opposite of what this view is for.
func TestAnExitedPaneIsStillListed(t *testing.T) {
	w, grid, _ := newFixture(t)
	if err := grid.Enrol("p1", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer grid.Withdraw("p1")
	w.Watch("p1", "claude")
	w.Exited("p1")
	got := w.Watching()
	if len(got) != 1 || got[0].Agent != "claude" {
		t.Fatalf("Watching() = %+v after Exited; want p1/claude", got)
	}
}
