package agentdriver_test

// The region a rule names as the TRANSCRIPT: the rows the agent printed, read
// off the same frame the verdict is decided from (nocx-tnx44).
//
// Two facts have to hold for the third facet to measure anything, and both are
// asserted here over the committed corpus rather than over chrome built to
// match what the rule currently reads:
//
//   - the CHROME is not in the yield. A spinner's timer ticks every second and
//     a hung agent's spinner ticks forever, so a yield that contained the
//     status stack would report the very pane the facet exists to catch as
//     moving.
//   - the pane's own output IS in the yield, and the yield changes when it
//     grows. A region that read nothing, or read the wrong rows, would report
//     moving forever, which is the same failure in the other direction.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
)

// claude-working@15000 and @17000 are the same turn two seconds apart: the
// spinner the TUI draws above the box has ticked ("* Misting… (1s)" to
// "(2s)"), and the agent has printed nothing at all since the user's prompt.
//
// The frames therefore DIFFER and the transcript does not, which is exactly the
// case "did the frame change" gets wrong — and the yield has to see it the
// right way round.
func TestALiveSpinnerIsNotPartOfTheTranscriptYield(t *testing.T) {
	first := observe(t, replay(t, "claude-working", 15000))
	second := observe(t, replay(t, "claude-working", 17000))

	rows := first.Transcript()
	if len(rows) == 0 {
		t.Fatal("the region read nothing at all on a pane with a transcript, so this test asserts nothing")
	}
	for _, row := range rows {
		if strings.Contains(row, "Misting") {
			t.Fatalf("the status stack is in the yield: %q", row)
		}
	}
	if !reflect.DeepEqual(rows, second.Transcript()) {
		t.Fatalf("the transcript is unchanged and the yield is not:\n%q\n%q", rows, second.Transcript())
	}
	// And the spinner really did move, so the equality above is a fact about
	// the region rather than about two identical frames.
	if first.State != agentdriver.StateWorking || second.State != agentdriver.StateWorking {
		t.Fatalf("both frames are a turn in flight: %q and %q", first.State, second.State)
	}
}

// The other half, over the assembled reply on claude-2.1.266-turn: the yield
// GROWS when the agent prints. The status stack is erased outright once Claude
// starts streaming, so this is the pane's own output moving and nothing else.
func TestTheTranscriptYieldGrowsWithThePanesOwnOutput(t *testing.T) {
	at79 := observe(t, replay(t, "claude-2.1.266-turn", 79000))
	at80 := observe(t, replay(t, "claude-2.1.266-turn", 80000))

	if at79.State != agentdriver.StateWorking || at80.State != agentdriver.StateWorking {
		t.Fatalf("both frames are a turn in flight: %q and %q", at79.State, at80.State)
	}
	if reflect.DeepEqual(at79.Transcript(), at80.Transcript()) {
		t.Fatalf("a second of streaming left the yield identical: %q", at80.Transcript())
	}
	// The yield is the transcript read from the box UPWARDS, so the row the
	// agent printed LAST is the first entry — the growth edge, which is the
	// row a comparison has to be able to see.
	if got := at80.Transcript()[0]; !strings.Contains(got, "against the dark") {
		t.Errorf("the yield does not begin at the last row the agent printed: %q", got)
	}
}

// The status stack is where the rule says it is, read off the corpus: a pane
// whose turn is in flight, whose token meter is drawn, and whose yield still
// begins at the transcript's own last row.
//
// claude-2.1.266-subagent-finished@80000 is the shape that made the bound
// necessary: the stack there is TWO rows — the spinner and a tip line under it
// — and the tip row carries no status glyph at all.
func TestTheWholeStatusStackIsSteppedOver(t *testing.T) {
	o := observe(t, replay(t, "claude-2.1.266-subagent-finished", 80000))
	if o.State != agentdriver.StateWorking {
		t.Fatalf("state = %q, want %q", o.State, agentdriver.StateWorking)
	}
	rows := o.Transcript()
	if len(rows) == 0 {
		t.Fatal("the region read nothing on a pane with a transcript")
	}
	for _, row := range rows {
		for _, chrome := range []string{"Brewing", "Tip: Start with small features"} {
			if strings.Contains(row, chrome) {
				t.Fatalf("%q is chrome drawn above the box and it is in the yield", row)
			}
		}
	}
	if got := rows[0]; !strings.Contains(got, "finding to the user.") {
		t.Errorf("the yield does not begin at the last row the agent printed: %q", got)
	}
}

// A pane whose transcript the region cannot reach yields NOTHING, and an
// absent yield is not an empty one: no measurement is not the same claim as a
// transcript that stood still.
func TestAPaneWithNothingAboveTheBoxYieldsNoTranscript(t *testing.T) {
	rows := observe(t, replay(t, "claude-trust", 11000)).Transcript()
	if len(rows) != 0 {
		t.Fatalf("a frame with no input box and no meter yielded %d rows: %+v", len(rows), rows)
	}
	// And the extractor contributes no entry at all, rather than an empty one.
	for _, e := range observe(t, replay(t, "claude-trust", 11000)).Extras {
		if e.Name == agentdriver.TranscriptExtra {
			t.Fatalf("an extractor that matched nothing was present: %+v", e)
		}
	}
}
