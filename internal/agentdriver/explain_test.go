package agentdriver_test

// What the RULE SEES, asserted against the same corpus the verdict is.
//
// The view this feeds exists because "a rule the user must write blind is a
// dead rule", so the thing under test is not that an explanation is produced —
// it is that the explanation is a reading of the SAME evaluation the product
// acts on. Every test here is either that identity or one of the four things a
// person repairing a rule has to be able to see: where an anchor bound, which
// branch answered, which predicate a branch stopped at, and which rows are
// chrome.

import (
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/panegrid"
)

// TestExplainAgreesWithClassifyOverTheCorpus is the identity, and it is the
// whole reason Explain is a recording of the walk rather than a second walk.
// Two evaluations of one question is how the two come to disagree on the frame
// nobody tried; this asserts they cannot, over every capture and every moment
// the closed-set sweep uses.
func TestExplainAgreesWithClassifyOverTheCorpus(t *testing.T) {
	reg := registry(t)
	for _, name := range captureNames {
		for _, at := range []int64{0, 500, 2_000, 5_000, 20_000, 45_000, 70_000} {
			f := replay(t, name, at)
			want := reg.Classify("claude", f)
			got := reg.Explain("claude", f)
			if got.State != want {
				t.Fatalf("%s@%dms: Explain state = %q, Classify = %q", name, at, got.State, want)
			}
			if !got.HasRule {
				t.Fatalf("%s@%dms: claude has a rule and the explanation says it does not", name, at)
			}
			assertMatchedBranchProduces(t, name, at, got)
		}
	}
}

// assertMatchedBranchProduces is the second half of the identity: the branch
// the explanation NAMES is a branch that answers the state it reports, and
// when no branch matched the state is the document's own default. A view that
// named the wrong branch would send a person to repair a branch that is
// working.
func assertMatchedBranchProduces(t *testing.T, name string, at int64, e agentdriver.Explanation) {
	t.Helper()
	if e.Matched < 0 {
		if e.State != e.Default {
			t.Fatalf("%s@%dms: no branch matched but state %q is not the default %q", name, at, e.State, e.Default)
		}
		return
	}
	if e.Matched >= len(e.Branches) {
		t.Fatalf("%s@%dms: matched branch %d is out of range (%d branches)", name, at, e.Matched, len(e.Branches))
	}
	b := e.Branches[e.Matched]
	if !b.Matched {
		t.Fatalf("%s@%dms: branch %d is named as the match and is not marked matched", name, at, e.Matched)
	}
	if b.State != e.State {
		t.Fatalf("%s@%dms: branch %d answers %q, state is %q", name, at, e.Matched, b.State, e.State)
	}
}

// TestExplainNamesTheBranchThatAnswered pins one moment whose verdict is
// already asserted elsewhere, so a person is told WHICH branch to look at
// rather than only what it decided.
func TestExplainNamesTheBranchThatAnswered(t *testing.T) {
	e := registry(t).Explain("claude", replay(t, "claude-permission", 20_000))
	if e.State != agentdriver.StatePermissionChoice {
		t.Fatalf("state = %q, want permission_choice", e.State)
	}
	if e.Matched != 0 {
		t.Fatalf("matched branch = %d, want 0 (the permission branch is first, and its order is the safety property)", e.Matched)
	}
	if len(e.Branches[0].Predicates) != 5 {
		t.Fatalf("permission branch reports %d predicates, want 5", len(e.Branches[0].Predicates))
	}
	for i, p := range e.Branches[0].Predicates {
		if !p.Evaluated || !p.Held {
			t.Fatalf("predicate %d (%s) of the matching branch: evaluated=%v held=%v", i, p.Kind, p.Evaluated, p.Held)
		}
	}
}

// TestExplainSaysWhereEachBranchStopped is the acceptance criterion's own
// sentence. A conjunction short-circuits, and the predicate it stopped at is
// exactly the fact a person repairing a rule needs: everything after it was
// never asked, and reporting those as "false" would send them to the wrong
// line.
func TestExplainSaysWhereEachBranchStopped(t *testing.T) {
	e := registry(t).Explain("claude", replay(t, "claude-idle", 20_000))
	if e.State != agentdriver.StateFreeText {
		t.Fatalf("state = %q, want free_text", e.State)
	}
	if e.Matched != -1 {
		t.Fatalf("matched = %d, want -1: an idle screen falls through every branch to the default", e.Matched)
	}
	first := e.Branches[0]
	if first.Matched {
		t.Fatal("the permission branch matched on an idle screen")
	}
	stopped := -1
	for i, p := range first.Predicates {
		if !p.Evaluated {
			continue
		}
		if !p.Held {
			stopped = i
			break
		}
	}
	if stopped < 0 {
		t.Fatal("the permission branch did not match and no predicate of it is reported as failing")
	}
	for i := stopped + 1; i < len(first.Predicates); i++ {
		if first.Predicates[i].Evaluated {
			t.Fatalf("predicate %d was reported evaluated after the branch stopped at %d", i, stopped)
		}
	}
}

// TestExplainStopsAtTheBranchThatMatched: order is a safety property of the
// document, so the reading has to show that the branches after the match were
// never reached. A person who cannot see that will "fix" a later branch that
// could never have run.
func TestExplainStopsAtTheBranchThatMatched(t *testing.T) {
	e := registry(t).Explain("claude", replay(t, "claude-permission", 20_000))
	for i, b := range e.Branches {
		switch {
		case i < e.Matched && !b.Reached:
			t.Fatalf("branch %d before the match is reported unreached", i)
		case i == e.Matched && !b.Reached:
			t.Fatalf("the matching branch %d is reported unreached", i)
		case i > e.Matched && b.Reached:
			t.Fatalf("branch %d after the match is reported reached", i)
		}
	}
}

// TestExplainReportsWhereAnchorsBound: the anchors are the arithmetic, and
// they are where a rule breaks when a TUI moves its chrome. A row number is
// what makes "the box did not bind" repairable.
func TestExplainReportsWhereAnchorsBound(t *testing.T) {
	f := replay(t, "claude-idle", 20_000)
	e := registry(t).Explain("claude", f)
	byName := map[string]agentdriver.AnchorReading{}
	for _, a := range e.Anchors {
		byName[a.Name] = a
	}
	prompt, ok := byName["prompt"]
	if !ok {
		t.Fatalf("no reading for the prompt anchor; got %v", e.Anchors)
	}
	if !prompt.Bound {
		t.Fatal("the prompt anchor did not bind on an idle claude screen")
	}
	if prompt.Row <= 0 || prompt.Row >= f.Rows {
		t.Fatalf("prompt bound at row %d, outside the frame's %d rows", prompt.Row, f.Rows)
	}
	// The document's own order, so a reader sees a later anchor beneath the
	// one it is computed from rather than in map order.
	if e.Anchors[0].Name != "bottomRule" {
		t.Fatalf("first anchor reading is %q, want the document's first", e.Anchors[0].Name)
	}
}

// TestExplainMarksAnUnboundAnchorAbsent, from the other side: an anchor that
// did not bind is ABSENT rather than bound at row zero, because row zero is a
// real row and a view that draws one there points at the wrong line.
func TestExplainMarksAnUnboundAnchorAbsent(t *testing.T) {
	f := screen(t, 40, 6, []string{"nothing here is claude chrome"}, 0, 1)
	e := registry(t).Explain("claude", f)
	for _, a := range e.Anchors {
		if a.Bound {
			t.Fatalf("anchor %q bound at row %d on a screen with no claude chrome", a.Name, a.Row)
		}
	}
	if e.State != agentdriver.StateUnknown {
		t.Fatalf("state = %q, want unknown", e.State)
	}
}

// TestExplainReadsTheRowsAsChrome: which rows are full-width rules is what the
// input box is FOUND by, so it is the first thing a person comparing a screen
// against a rule has to see.
func TestExplainReadsTheRowsAsChrome(t *testing.T) {
	f := replay(t, "claude-idle", 20_000)
	e := registry(t).Explain("claude", f)
	if len(e.Rows) != f.Rows {
		t.Fatalf("%d row readings for a %d-row frame", len(e.Rows), f.Rows)
	}
	rules := 0
	for i, r := range e.Rows {
		if r.RuleGlyph == "" {
			continue
		}
		rules++
		if r.RuleGlyph != "─" {
			t.Fatalf("row %d reported as a full-width rule of %q", i, r.RuleGlyph)
		}
	}
	if rules < 2 {
		t.Fatalf("%d full-width rules found; the input box is bounded by two", rules)
	}
	// A blank row opens nowhere, and saying it opens at column zero would
	// make every blank row look like chrome.
	blank := false
	for _, r := range e.Rows {
		if r.OpensAt < 0 {
			blank = true
		}
	}
	if !blank {
		t.Fatal("no row reported as blank on a claude screen, which has several")
	}
}

// TestExplainReportsTheExtractorsYield: the extractors are the other half of
// what a rule reads, and a person calibrating one needs to see what it read
// rather than only what the verdict was.
func TestExplainReportsTheExtractorsYield(t *testing.T) {
	e := registry(t).Explain("claude", replay(t, "claude-subagent", 30_000))
	var subs *agentdriver.ExtractorReading
	for i := range e.Extractors {
		if e.Extractors[i].Name == agentdriver.SubagentsExtra {
			subs = &e.Extractors[i]
		}
	}
	if subs == nil {
		t.Fatalf("no subagents extractor in the explanation; got %v", e.Extractors)
	}
	if subs.Region == nil {
		t.Fatal("the subagents extractor reports no region while its anchor bound")
	}
	if len(subs.Rows) == 0 {
		t.Fatal("the subagents extractor is reported with no rows while the panel is on screen")
	}
}

// TestExplainForAnAgentWithNoRule is the distinction the whole surface rests
// on: "this agent's rule could not read the screen" and "nocx has no rule for
// this agent" both answer unknown, and only the second is permanent.
func TestExplainForAnAgentWithNoRule(t *testing.T) {
	f := replay(t, "claude-idle", 20_000)
	e := registry(t).Explain("nothing-registered", f)
	if e.HasRule {
		t.Fatal("an agent with no driver is reported as having a rule")
	}
	if e.State != agentdriver.StateUnknown {
		t.Fatalf("state = %q, want unknown", e.State)
	}
	if len(e.Branches) != 0 || len(e.Anchors) != 0 {
		t.Fatalf("an agent with no rule reports %d branches and %d anchors", len(e.Branches), len(e.Anchors))
	}
	if len(e.Rows) != f.Rows {
		t.Fatalf("%d row readings without a rule; the FRAME is readable whether or not a rule is", len(e.Rows))
	}
}

// TestExplainDoesNotDecideAnything: the reading is a read-out, and adding it
// may not move a verdict. Asserted by construction — the same frame classified
// before and after being explained.
func TestExplainDoesNotDecideAnything(t *testing.T) {
	reg := registry(t)
	f := replay(t, "claude-working", 20_000)
	before := reg.Classify("claude", f)
	_ = reg.Explain("claude", f)
	if after := reg.Classify("claude", f); after != before {
		t.Fatalf("classification moved from %q to %q across an Explain", before, after)
	}
}

func registry(t *testing.T) *agentdriver.Registry {
	t.Helper()
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg
}

var _ = panegrid.Frame{}
