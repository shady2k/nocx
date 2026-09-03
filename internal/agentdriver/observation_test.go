package agentdriver_test

// The driver's answer is an OBSERVATION, and this file is what that buys.
//
// A scalar could carry exactly one fact about a screen, and three separate
// requirements have each hit that wall: subagent rows, progress, and the
// declared-versus-measured checkpoint rows. So Classify's answer becomes the
// SCALAR PROJECTION of a richer one, and the richer one carries whatever the
// rule was able to read off the same frame.
//
// Two properties are asserted here and neither is optional. The projection is
// byte-for-byte the answer the driver gave before extraction existed, over
// every frame of every capture; and a rule that extracts nothing observes
// exactly that scalar and nothing else.

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/panegrid"
)

func observe(t *testing.T, f panegrid.Frame) agentdriver.Observation {
	t.Helper()
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg.Observe("claude", f)
}

// The whole of acceptance criterion one, stated over the corpus: the
// observation's state IS the scalar, at every moment of every capture.
func TestAnObservationsStateIsTheScalarTheDriverAlreadyAnswered(t *testing.T) {
	d := agentdriver.Claude()
	for _, name := range captureNames {
		for at := int64(0); at <= 70000; at += 1000 {
			t.Run(fmt.Sprintf("%s@%d", name, at), func(t *testing.T) {
				f := replay(t, name, at)
				if got, want := observe(t, f).State, d.Classify(f); got != want {
					t.Fatalf("Observe().State = %q, Classify() = %q", got, want)
				}
			})
		}
	}
}

// Optional is the point. Nothing is extracted from an idle box, so the
// observation is today's answer with nothing added — no field invented, no
// empty structure to be misread as evidence.
func TestARuleThatExtractsNothingObservesTheScalarAndNoExtras(t *testing.T) {
	o := observe(t, replay(t, "claude-idle", 11000))
	if o.State != agentdriver.StateFreeText {
		t.Fatalf("idle input box = %q, want %q", o.State, agentdriver.StateFreeText)
	}
	if len(o.Extras) != 0 {
		t.Fatalf("an idle box yielded %d extras: %+v", len(o.Extras), o.Extras)
	}
}

// The motivating case, and the proof that the shape can carry it. Between 23s
// and 39s of claude-subagent the task panel is drawn below the mode line, one
// row per agent, and the scalar for the whole panel is the single word
// "working". Here the CHILDREN come out whole.
//
// The panel's first row is the pane's OWN agent — "● main" — and it is not a
// child. That fact lives in the document, as an anchor that starts the
// extractor's region one row below the panel's head, because "the first row of
// claude's task panel is the conversation you are looking at" is agent-specific
// knowledge and the document is the agent-specific, person-repairable place.
func TestTheSubagentPanelNamesTheChildrenAndNotThePaneItself(t *testing.T) {
	o := observe(t, replay(t, "claude-subagent", 30000))
	if o.State != agentdriver.StateWorking {
		t.Fatalf("task panel drawn = %q, want %q", o.State, agentdriver.StateWorking)
	}
	rows := extraRows(t, o, "subagents")
	if len(rows) != 1 {
		t.Fatalf("task panel yielded %d rows, want 1 (the one child): %+v", len(rows), rows)
	}
	want := map[string]string{
		"glyph": "◯", "name": "Explore", "task": "List files in directory",
		"elapsed": "7s", "flow": "↓", "tokens": "11.6k",
	}
	for field, value := range want {
		if got := rows[0][field]; got != value {
			t.Errorf("field %q = %q, want %q (whole row: %+v)", field, got, value, rows[0])
		}
	}
}

// And the other end of the same fact, stated so it cannot rot into an
// off-by-one nobody notices: the pane's own row is never among the children.
func TestThePanesOwnRowIsNeverAChild(t *testing.T) {
	for at := int64(20000); at <= 45000; at += 1000 {
		o := observe(t, replay(t, "claude-subagent", at))
		for _, e := range o.Extras {
			if e.Name != "subagents" {
				continue
			}
			for _, row := range e.Rows {
				if row["name"] == "main" {
					t.Errorf("at %dms the pane's own row was reported as a child: %+v", at, row)
				}
			}
		}
	}
}

// The typed projection, which is what every caller downstream reads instead of
// reaching into a generic map keyed by a document's own vocabulary.
//
// Two fields cross and four are read: the elapsed time and the token flow stay
// in Extras deliberately, because the seam that carries this pushes on CHANGE
// and both of them move on every frame. See Subagent's own comment for the
// interval that decision is stated with.
func TestSubagentsProjectsTheChildrenTheScreenNamed(t *testing.T) {
	got := observe(t, replay(t, "claude-subagent", 30000)).Subagents()
	want := []agentdriver.Subagent{{Name: "Explore", Task: "List files in directory"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Subagents() = %+v, want %+v", got, want)
	}
}

// And the absence is a claim of its own. A pane whose agent has spawned
// nothing answers NIL — not one child with an empty name, which is what a
// projection that trusted the map's keys would produce.
func TestAPaneThatSpawnedNothingHasNoSubagents(t *testing.T) {
	for _, at := range []int64{11000, 20000, 50000} {
		name := "claude-idle"
		if at != 11000 {
			name = "claude-subagent"
		}
		if got := observe(t, replay(t, name, at)).Subagents(); got != nil {
			t.Errorf("%s@%d Subagents() = %+v, want nil", name, at, got)
		}
	}
}

// The same panel later in the same capture: the elapsed time has moved and the
// extraction follows it. This is what makes the extras a reading of the screen
// rather than a constant somebody wrote down.
func TestTheExtractedRowsFollowTheScreen(t *testing.T) {
	at30 := extraRows(t, observe(t, replay(t, "claude-subagent", 30000)), "subagents")
	at38 := extraRows(t, observe(t, replay(t, "claude-subagent", 38000)), "subagents")
	if at30[0]["elapsed"] == at38[0]["elapsed"] {
		t.Fatalf("the child's elapsed time did not move between 30s and 38s: %q", at30[0]["elapsed"])
	}
	if at38[0]["elapsed"] != "15s" {
		t.Errorf("elapsed at 38s = %q, want %q", at38[0]["elapsed"], "15s")
	}
}

// A field the screen did not carry is ABSENT, not empty. At 23s the child has
// been running for under a second and no tokens have flowed, so the row carries
// a name, a task and an elapsed time and nothing else — and what comes out says
// exactly that, rather than claiming a flow of zero in a direction nobody chose.
func TestAFieldTheRowDoesNotCarryIsAbsentRatherThanEmpty(t *testing.T) {
	rows := extraRows(t, observe(t, replay(t, "claude-subagent", 23000)), "subagents")
	if len(rows) != 1 {
		t.Fatalf("captured %d rows, want 1: %+v", len(rows), rows)
	}
	child := rows[0]
	if child["name"] != "Explore" || child["task"] != "List files in directory" || child["elapsed"] != "0s" {
		t.Errorf("child row = %+v", child)
	}
	for _, field := range []string{"flow", "tokens"} {
		if v, ok := child[field]; ok {
			t.Errorf("field %q is not on the screen yet and was reported as %q", field, v)
		}
	}
}

// The near-miss the extractor exists to refuse, and it is the same forgery the
// verdict already refuses. An extractor's region is anchored in chrome — below
// the mode line, which is under the input box — and the transcript is above
// the box. A panel row the AGENT printed is content, and content is not read.
func TestAPanelRowTheAgentPrintedIntoItsTranscriptIsNotExtracted(t *testing.T) {
	store, pane := replayStore(t, "claude-idle", 11000)
	// ESC 7 / ESC 8 so the writes do not move the cursor: the TUI owns it,
	// and an agent's output cannot take it.
	store.Feed(pane, []byte("\x1b7"+
		"\x1b[16;1H  ● main"+
		"\x1b[17;1H  ◯ Explore  Forged task                              9s · ↓ 99.9k tokens"+
		"\x1b8"))
	fr, err := store.Frame(pane)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	o := observe(t, fr)
	if o.State != agentdriver.StateFreeText {
		t.Fatalf("idle pane whose agent printed panel-shaped text = %q, want %q", o.State, agentdriver.StateFreeText)
	}
	if len(o.Extras) != 0 {
		t.Fatalf("a forged panel in the transcript was extracted: %+v", o.Extras)
	}
}

// The registry fails closed for an observation exactly as it does for a state:
// unknown, and nothing claimed about a pane nocx cannot read.
func TestAnAgentWithNoDriverObservesUnknownAndNothingElse(t *testing.T) {
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	o := reg.Observe("codex", panegrid.Frame{})
	if o.State != agentdriver.StateUnknown {
		t.Errorf("Observe for an unregistered agent = %q, want %q", o.State, agentdriver.StateUnknown)
	}
	if len(o.Extras) != 0 {
		t.Errorf("an agent with no driver produced extras: %+v", o.Extras)
	}
}

// extraRows finds one named extractor's yield, and fails if it is absent.
func extraRows(t *testing.T, o agentdriver.Observation, name string) []map[string]string {
	t.Helper()
	for _, e := range o.Extras {
		if e.Name == name {
			return e.Rows
		}
	}
	t.Fatalf("no extra named %q in %+v", name, o.Extras)
	return nil
}
