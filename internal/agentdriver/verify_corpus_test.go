package agentdriver_test

// THE SHIPPED RULE IS NOT EXEMPT (nocx-jse6x).
//
// Typing authority is earned by replaying a rule against every frame a person
// labelled, and the claude rule earns it the same way anybody else's does.
// This is that verification run against the real corpus in testdata/captures —
// real bytes off a real PTY at 120x40 — assembled into a labelled set exactly
// as a calibration would write one: a capture, and one mark per label.
//
// The corpus is READ and never changed here. What this test adds is the second
// half of the round trip: a calibration stores the bytes that REPRODUCE a
// frame rather than the frame, so a labelled set is only evidence if
// paint-then-replay hands back a screen the rule reads the same way. Six labels
// classifying correctly through that round trip is what says it does.

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
)

// corpusMoment is one label and the frame in the corpus that shows it. Every
// mark here is one claude_test.go already asserts a state for, so this file
// introduces no new claim about what the corpus contains.
type corpusMoment struct {
	label   agentcalib.Label
	capture string
	atMs    int64
}

var corpusMoments = []corpusMoment{
	{agentcalib.LabelIdle, "claude-idle", 11000},
	{agentcalib.LabelWorking, "claude-working", 17000},
	{agentcalib.LabelAsksYou, "claude-permission", 49000},
	{agentcalib.LabelWaitingOnChild, "claude-subagent", 30000},
	{agentcalib.LabelError, "claude-error", 30000},
	{agentcalib.LabelMenuOpen, "claude-modal", 20000},
}

// memSet is a calibration store holding one set in memory. The set under test
// is assembled from the corpus rather than walked, so there is nothing on disk
// to hold — and a file store here would be testing the filesystem.
type memSet struct {
	set   agentcalib.Set
	found bool
}

func (m *memSet) Load(agent string) (agentcalib.Set, bool, error) {
	if !m.found || m.set.Agent != agent {
		return agentcalib.Set{}, false, nil
	}
	return m.set, true, nil
}

func (m *memSet) Save(set agentcalib.Set) error {
	m.set, m.found = set, true
	return nil
}

// corpusSet paints each moment into a capture and marks it, which is what a
// calibration does when a person says "now".
func corpusSet(t *testing.T, moments []corpusMoment) agentcalib.Set {
	t.Helper()
	set := agentcalib.Set{
		Agent:  "claude",
		Header: agentcapture.Header{Agent: "claude", Argv: []string{"claude"}, Cols: 120, Rows: 40},
	}
	for _, m := range moments {
		mark := int64(len(set.Chunks))
		set.Chunks = append(set.Chunks, agentcapture.Chunk{
			AtMs:   mark,
			Offset: agentcapture.EndOffset(set.Chunks, len(set.Chunks)),
			Data:   string(agentcapture.Paint(replay(t, m.capture, m.atMs))),
		})
		at := mark
		set.Labels = append(set.Labels, agentcalib.Record{Label: m.label, AtMs: &at})
	}
	return set
}

func corpusVerdict(t *testing.T, set agentcalib.Set) agentcalib.Verdict {
	t.Helper()
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	store := &memSet{}
	if err := store.Save(set); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Screens is nil deliberately: verification reads a stored set and never a
	// live pane, and passing one would suggest it could.
	return agentcalib.New(log.NewSlogAdapter(nil), nil, store, rules).Verify("claude")
}

// TestTheShippedClaudeRuleEarnsItsTypingAuthority is the happy path of the
// whole design, on the rule that actually ships.
func TestTheShippedClaudeRuleEarnsItsTypingAuthority(t *testing.T) {
	v := corpusVerdict(t, corpusSet(t, corpusMoments))
	if !v.MayType() {
		t.Fatalf("the shipped rule did not earn typing authority: reason=%q disagreements=%+v",
			v.Reason, v.Disagreements)
	}
	if v.Labelled != len(corpusMoments) || v.Agreed != v.Labelled {
		t.Fatalf("verdict is %d of %d, want all %d labelled states",
			v.Agreed, v.Labelled, len(corpusMoments))
	}
}

// And the same rule loses it when the labels stop classifying. Nothing about
// the rule moves here — one label is put on a frame that is not what it says —
// which is the shape an agent's update takes: the chrome changes, the person's
// old set stops classifying, and nocx goes back to lighting a dot.
func TestTheShippedClaudeRuleLosesItWhenALabelStopsClassifying(t *testing.T) {
	moments := append([]corpusMoment(nil), corpusMoments...)
	// The expensive direction, stated as the test: the tool-approval dialog
	// labelled idle. A rule verified against this set would be typing its
	// text and a submit key into a dialog whose first option is Yes.
	moments[0].capture, moments[0].atMs = "claude-permission", 49000

	v := corpusVerdict(t, corpusSet(t, moments))
	if v.MayType() {
		t.Fatal("a set whose idle label shows an approval dialog kept its typing authority")
	}
	if len(v.Disagreements) != 1 {
		t.Fatalf("disagreements = %+v, want the one label that stopped classifying", v.Disagreements)
	}
	d := v.Disagreements[0]
	if d.Label != agentcalib.LabelIdle ||
		d.Expected != agentdriver.StateFreeText || d.Got != agentdriver.StatePermissionChoice {
		t.Fatalf("disagreement = %+v, want idle expected free_text got permission_choice", d)
	}
}

// A labelled set stores the BYTES that reproduce a frame, not the frame. So
// the frame a rule is verified against is a replay, and this is the assertion
// that makes that sound at the level of the individual screen: the round trip
// through Paint answers exactly what the live frame answered.
func TestPaintingAndReplayingAFrameDoesNotMoveTheVerdict(t *testing.T) {
	d := agentdriver.Claude()
	for _, m := range corpusMoments {
		t.Run(fmt.Sprintf("%s@%d", m.capture, m.atMs), func(t *testing.T) {
			live := replay(t, m.capture, m.atMs)
			set := corpusSet(t, []corpusMoment{m})
			frames, err := set.Frames(log.NewSlogAdapter(nil))
			if err != nil {
				t.Fatalf("replay the painted set: %v", err)
			}
			if got, want := d.Classify(frames[0].Frame), d.Classify(live); got != want {
				t.Fatalf("painted and replayed = %q, live = %q", got, want)
			}
		})
	}
}
