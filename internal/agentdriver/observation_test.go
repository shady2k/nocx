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

// The motivating case, and the proof that the shape can carry it. Between 25s
// and 38s of claude-subagent the task panel is drawn below the mode line, one
// row per agent, and the scalar for the whole panel is the single word
// "working". Here the rows come out whole.
func TestTheSubagentPanelIsReadRowByRow(t *testing.T) {
	o := observe(t, replay(t, "claude-subagent", 30000))
	if o.State != agentdriver.StateWorking {
		t.Fatalf("task panel drawn = %q, want %q", o.State, agentdriver.StateWorking)
	}
	rows := extraRows(t, o, "subagents")
	if len(rows) != 2 {
		t.Fatalf("task panel yielded %d rows, want 2: %+v", len(rows), rows)
	}
	want := []map[string]string{
		{"glyph": "●", "name": "main"},
		{"glyph": "◯", "name": "Explore", "task": "List files in directory", "elapsed": "7s", "flow": "↓", "tokens": "11.6k"},
	}
	for i, w := range want {
		for field, value := range w {
			if got := rows[i][field]; got != value {
				t.Errorf("row %d field %q = %q, want %q (whole row: %+v)", i, field, got, value, rows[i])
			}
		}
	}
}

// The same panel later in the same capture: the elapsed time has moved and the
// extraction follows it. This is what makes the extras a reading of the screen
// rather than a constant somebody wrote down.
func TestTheExtractedRowsFollowTheScreen(t *testing.T) {
	at30 := extraRows(t, observe(t, replay(t, "claude-subagent", 30000)), "subagents")
	at38 := extraRows(t, observe(t, replay(t, "claude-subagent", 38000)), "subagents")
	if at30[1]["elapsed"] == at38[1]["elapsed"] {
		t.Fatalf("the child's elapsed time did not move between 30s and 38s: %q", at30[1]["elapsed"])
	}
	if at38[1]["elapsed"] != "15s" {
		t.Errorf("elapsed at 38s = %q, want %q", at38[1]["elapsed"], "15s")
	}
}

// A field the screen did not carry is ABSENT, not empty. At 23s the child has
// been running for under a second and no tokens have flowed, so the row carries
// a name, a task and an elapsed time and nothing else — and what comes out says
// exactly that, rather than claiming a flow of zero in a direction nobody chose.
func TestAFieldTheRowDoesNotCarryIsAbsentRatherThanEmpty(t *testing.T) {
	rows := extraRows(t, observe(t, replay(t, "claude-subagent", 23000)), "subagents")
	if len(rows) != 2 {
		t.Fatalf("captured %d rows, want 2: %+v", len(rows), rows)
	}
	child := rows[1]
	if child["name"] != "Explore" || child["task"] != "List files in directory" || child["elapsed"] != "0s" {
		t.Errorf("child row = %+v", child)
	}
	for _, field := range []string{"flow", "tokens"} {
		if v, ok := child[field]; ok {
			t.Errorf("field %q is not on the screen yet and was reported as %q", field, v)
		}
	}
	// And the parent row carries a name and nothing else at all.
	if len(rows[0]) != 2 {
		t.Errorf("the parent row invented fields: %+v", rows[0])
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
