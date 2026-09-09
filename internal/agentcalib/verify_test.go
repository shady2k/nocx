package agentcalib_test

// TYPING AUTHORITY IS EARNED, and the currency is the labelled set
// (nocx-jse6x).
//
// A mistimed keystroke does not merely fail to arrive: it ANSWERS whatever
// modal is on screen, and can approve a tool call the person never saw. So a
// rule may light an indicator on nothing but its author's confidence — a wrong
// dot costs nothing — and may gate typing only after it has classified every
// frame a person produced and labelled, each to the state they were asked for.
//
// These tests are about what the API makes IMPOSSIBLE rather than about what
// it answers when asked nicely. The authority is a value only Verify can
// produce, so every other path — a missing set, an unreadable one, an agent
// with no rule, a struct literal a caller wrote itself — denies typing without
// anybody having remembered to check.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/panegrid"
)

// fixedRule is a driver whose whole behaviour is a lookup from the text on row
// three to a state, which is exactly what `screens.drive` paints. It is a real
// Driver in a real Registry, so the registry's own fail-closed behaviour is
// under test with it rather than mocked away.
type fixedRule struct {
	agent string
	say   map[string]agentdriver.State
}

func (r fixedRule) Agent() string { return r.agent }

func (r fixedRule) Classify(f panegrid.Frame) agentdriver.State {
	for label, state := range r.say {
		if strings.Contains(f.Text(2), "state: "+label) {
			return state
		}
	}
	return agentdriver.StateUnknown
}

// correct is the rule that answers every label with the state its step says
// that label must classify to. It is BUILT from the step list rather than
// written out, so a state added to the walk cannot leave a stale copy here
// agreeing with nothing.
func correct() fixedRule {
	say := map[string]agentdriver.State{}
	for _, s := range agentcalib.Steps() {
		say[string(s.Label)] = s.Expect
	}
	return fixedRule{agent: agent, say: say}
}

func registryOf(t *testing.T, drivers ...agentdriver.Driver) *agentdriver.Registry {
	t.Helper()
	r, err := agentdriver.NewRegistry(drivers...)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

// ── the vocabulary is mapped in exactly one place, and it is total ────────

// Expect is the one place a calibration label meets a driver state. Every
// label a walk can ask for has one, because a label with no state to be
// checked against is a label no rule could ever be verified on — and a set
// carrying one is refused rather than skipped over.
func TestEveryLabelTheWalkAsksForMapsToADriverState(t *testing.T) {
	all := []agentcalib.Label{
		agentcalib.LabelIdle, agentcalib.LabelWorking, agentcalib.LabelAsksYou,
		agentcalib.LabelWaitingOnChild, agentcalib.LabelError, agentcalib.LabelMenuOpen,
	}
	for _, l := range all {
		state, ok := agentcalib.Expect(l)
		if !ok {
			t.Errorf("label %q maps to no driver state", l)
			continue
		}
		if !state.Valid() {
			t.Errorf("label %q maps to %q, which is not in the driver's closed set", l, state)
		}
		if state == agentdriver.StateUnknown || state == agentdriver.StateExited {
			t.Errorf("label %q maps to %q, which no screen can be verified against", l, state)
		}
	}
	if len(all) != len(agentcalib.Steps()) {
		t.Fatalf("the walk asks for %d states and this test knows %d; the mapping is no longer total",
			len(agentcalib.Steps()), len(all))
	}
	if _, ok := agentcalib.Expect("no-such-state"); ok {
		t.Error("a label nobody was ever asked for mapped to a state")
	}
}

// ── the gate ──────────────────────────────────────────────────────────────

// The happy path, and the sentence the product has to be able to say: every
// labelled frame classified to its label, so this rule may be typed against.
func TestARuleThatClassifiesEveryLabelledFrameMayGateTyping(t *testing.T) {
	c, sc, _, _ := newCalibrationsWith(t, registryOf(t, correct()))
	walkAll(t, c, sc, nil)

	v := c.Verify(agent)
	if !v.MayType() {
		t.Fatalf("a rule that classified every label may not type: %+v", v)
	}
	if v.Labelled != len(agentcalib.Steps()) || v.Agreed != v.Labelled {
		t.Fatalf("verdict counted %d of %d, want all %d labelled states",
			v.Agreed, v.Labelled, len(agentcalib.Steps()))
	}
	if len(v.Disagreements) != 0 {
		t.Fatalf("a verified rule reported disagreements: %+v", v.Disagreements)
	}
}

// A declined state is not a failure: it stays uncalibrated, which reads as
// unknown, which every consumer treats as busy. What is verified is what was
// produced.
func TestASkippedOptionalStateIsNotADisagreement(t *testing.T) {
	c, sc, _, _ := newCalibrationsWith(t, registryOf(t, correct()))
	skip := map[agentcalib.Label]bool{agentcalib.LabelError: true, agentcalib.LabelMenuOpen: true}
	walkAll(t, c, sc, skip)

	v := c.Verify(agent)
	if !v.MayType() {
		t.Fatalf("a rule verified against the states that WERE produced may not type: %+v", v)
	}
	if v.Labelled != len(agentcalib.Steps())-len(skip) {
		t.Fatalf("verdict counted %d labelled states, want the %d that were captured",
			v.Labelled, len(agentcalib.Steps())-len(skip))
	}
}

// The falsifier, at the seam it would have to be broken at: one label the rule
// answers differently, and the authority is gone. Not "a warning is logged" —
// the value that permits typing is not produced.
func TestOneMisclassifiedLabelRevokesTypingAuthority(t *testing.T) {
	broken := correct()
	broken.say[string(agentcalib.LabelAsksYou)] = agentdriver.StateFreeText

	c, sc, _, _ := newCalibrationsWith(t, registryOf(t, broken))
	walkAll(t, c, sc, nil)

	v := c.Verify(agent)
	if v.MayType() {
		t.Fatal("a rule that reads a tool-approval dialog as an input box may type into it")
	}
	if len(v.Disagreements) != 1 {
		t.Fatalf("disagreements = %+v, want exactly the one label the rule got wrong", v.Disagreements)
	}
	d := v.Disagreements[0]
	if d.Label != agentcalib.LabelAsksYou ||
		d.Expected != agentdriver.StatePermissionChoice || d.Got != agentdriver.StateFreeText {
		t.Fatalf("disagreement = %+v, want asks-you expected permission_choice got free_text", d)
	}
	if v.Agreed != v.Labelled-1 {
		t.Fatalf("verdict counted %d of %d agreeing, want one short", v.Agreed, v.Labelled)
	}
}

// A rule loses authority when the LABELS change under it, which is the same
// property seen from the other end and the one that makes this a property of
// the rule rather than of who wrote it. Nothing about the rule moved here;
// what it is checked against did.
func TestChangingALabelRevokesAVerifiedRule(t *testing.T) {
	c, sc, store, _ := newCalibrationsWith(t, registryOf(t, correct()))
	walkAll(t, c, sc, nil)
	if !c.Verify(agent).MayType() {
		t.Fatal("the rule did not verify before the set was changed")
	}

	set, found, err := store.Load(agent)
	if err != nil || !found {
		t.Fatalf("load: found=%v err=%v", found, err)
	}
	// The two LABELS swap places, and the marks stay where they were — so the
	// capture still replays forward and every frame in it is one the person
	// really produced. Only what they are said to BE has moved, which is the
	// smallest possible change and the one a rule cannot detect any other way.
	idle, working := -1, -1
	for i, rec := range set.Labels {
		switch rec.Label {
		case agentcalib.LabelIdle:
			idle = i
		case agentcalib.LabelWorking:
			working = i
		}
	}
	set.Labels[idle].Label, set.Labels[working].Label = agentcalib.LabelWorking, agentcalib.LabelIdle
	if err := store.Save(set); err != nil {
		t.Fatalf("save: %v", err)
	}

	v := c.Verify(agent)
	if v.MayType() {
		t.Fatal("a rule whose labels no longer classify kept its typing authority")
	}
	if len(v.Disagreements) != 2 {
		t.Fatalf("disagreements = %+v, want both swapped labels", v.Disagreements)
	}
}

// ── every other path denies, and none of them had to remember to ──────────

func TestAnUncalibratedAgentMayNotBeTypedInto(t *testing.T) {
	c, _, _, _ := newCalibrationsWith(t, registryOf(t, correct()))
	v := c.Verify(agent)
	if v.MayType() {
		t.Fatal("an agent nobody has calibrated may be typed into")
	}
	if v.Reason == "" {
		t.Fatal("an unverified verdict does not say why, so the product cannot state the consequence")
	}
	if v.Labelled != 0 || v.Agreed != 0 {
		t.Fatalf("verdict counted %d of %d for a set that does not exist", v.Agreed, v.Labelled)
	}
}

// The registry fails closed for an agent nothing was written for, and so does
// this: there is no rule, so there is nothing that could have earned anything.
func TestAnAgentWithNoRuleMayNotBeTypedInto(t *testing.T) {
	c, sc, _, _ := newCalibrationsWith(t, registryOf(t, fixedRule{agent: "someone-else"}))
	walkAll(t, c, sc, nil)

	v := c.Verify(agent)
	if v.MayType() {
		t.Fatal("an agent with no rule at all may be typed into")
	}
	if !strings.Contains(v.Reason, agent) {
		t.Fatalf("reason %q does not name the agent it is about", v.Reason)
	}
}

// A set whose capture cannot be replayed verifies nothing, and the direction
// of that failure is a refusal.
func TestASetThatCannotBeReplayedMayNotBeTypedAgainst(t *testing.T) {
	c, sc, _, root := newCalibrationsWith(t, registryOf(t, correct()))
	walkAll(t, c, sc, nil)
	if !c.Verify(agent).MayType() {
		t.Fatal("the rule did not verify before the capture was damaged")
	}

	path := filepath.Join(root, "agents", "calibration", agent, "capture.jsonl")
	if err := os.WriteFile(path, []byte("{\"agent\":\"claude\"}\n"), 0o600); err != nil {
		t.Fatalf("damage the capture: %v", err)
	}
	if v := c.Verify(agent); v.MayType() {
		t.Fatalf("a set whose capture is unreadable kept its authority: %+v", v)
	}
}

// The one place the two vocabularies meet is total over the labels a walk can
// ask for — and a set arrives from a file a person can edit. A label this
// build cannot map is refused rather than skipped, because skipping it would
// verify a rule against fewer states than the set claims to hold.
func TestALabelThisBuildDoesNotAskForRefusesTheWholeSet(t *testing.T) {
	c, sc, store, _ := newCalibrationsWith(t, registryOf(t, correct()))
	walkAll(t, c, sc, nil)

	set, _, err := store.Load(agent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	set.Labels[len(set.Labels)-1].Label = "invented-by-hand"
	if err := store.Save(set); err != nil {
		t.Fatalf("save: %v", err)
	}

	v := c.Verify(agent)
	if v.MayType() {
		t.Fatal("a set carrying a label this build cannot map kept its authority")
	}
	if !strings.Contains(v.Reason, "invented-by-hand") {
		t.Fatalf("reason %q does not name the label it could not map", v.Reason)
	}
}

// The structural half, stated as an assertion: the zero Verdict — what a
// caller gets from a struct literal, a nil map lookup or a field it forgot to
// fill — denies typing. There is no exported way to write true into it, so a
// caller cannot grant itself the authority it was supposed to ask for.
func TestAVerdictNobodyProducedDeniesTyping(t *testing.T) {
	if (agentcalib.Verdict{}).MayType() {
		t.Fatal("a verdict nobody produced permits typing")
	}
	if (agentcalib.Verdict{Agent: agent, Labelled: 3, Agreed: 3}).MayType() {
		t.Fatal("a verdict a caller filled in itself permits typing")
	}
}

// The verdict rides with the state a surface draws, so the page cannot show a
// set and a stale verdict of it: they are computed in one answer.
func TestStatusCarriesTheVerdictBesideTheSet(t *testing.T) {
	c, sc, _, _ := newCalibrationsWith(t, registryOf(t, correct()))
	walkAll(t, c, sc, nil)

	st, err := c.Status(pane, agent)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Stored == nil || !st.Stored.Complete {
		t.Fatalf("stored = %+v, want the complete set the walk wrote", st.Stored)
	}
	if !st.Verification.MayType() {
		t.Fatalf("status carries %+v, want the verdict Verify answers", st.Verification)
	}
	if st.Verification.Agent != agent {
		t.Fatalf("verdict is about %q, want %q", st.Verification.Agent, agent)
	}
}
