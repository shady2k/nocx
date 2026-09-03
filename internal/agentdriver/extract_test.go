package agentdriver

// The value-reading half of the grammar.
//
// Every predicate in predicate.go answers yes or no, and a screen full of
// facts reduced to one word is what made the driver unable to report anything
// about a subagent panel it can plainly see. An extractor is the other half: a
// REGION, which is where the engine says it may look, plus a PATTERN, which is
// what the document says to read out of a row there.
//
// The bound is the point of the split. A document chooses the pattern and can
// never widen the region past the engine's ceiling, so an agent whose printed
// lines abut the chrome cannot extend the area a forged row would be read out
// of — exactly the argument region.maxRows already carries for the predicates.

import (
	"testing"
)

// Every frame below is this tall, so the mode line can be bound at row 0 by a
// fixed offset from the frame's own bottom edge — the anchor grammar has no
// "row zero" and should not grow one for a test.
const testRows = 8

// panelDoc is the shape claude draws below its mode line, hand-built.
func panelDoc(maxRows int) Document {
	return Document{
		Agent:   "test",
		Default: StateFreeText,
		Anchors: []AnchorSpec{{Name: "modeLine", Kind: "offset", Offset: -(testRows - 1)}},
		Extractors: []Extractor{{
			Name:       "subagents",
			Pattern:    `^\s*(?P<glyph>[●◯])\s(?P<name>\S+)(?:\s{2,}(?P<task>\S.*?))?$`,
			RegionSpec: RegionSpec{Anchor: "modeLine", MaxRows: maxRows},
		}},
	}
}

func TestAnExtractorCapturesEveryRowItsPatternMatches(t *testing.T) {
	d, err := newDocumentDriver(panelDoc(4))
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	f := grid(60, testRows, []string{"  mode line", "", "  ● main", "  ◯ Explore  List files"}, 0, 0)
	o := d.Observe(f)
	if len(o.Extras) != 1 || o.Extras[0].Name != "subagents" {
		t.Fatalf("extras = %+v, want one named subagents", o.Extras)
	}
	rows := o.Extras[0].Rows
	if len(rows) != 2 {
		t.Fatalf("captured %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0]["glyph"] != "●" || rows[0]["name"] != "main" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1]["name"] != "Explore" || rows[1]["task"] != "List files" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	// A group that did not participate contributes no field, rather than an
	// empty one a reader would have to know to distrust.
	if _, ok := rows[0]["task"]; ok {
		t.Errorf("row 0 invented a task: %+v", rows[0])
	}
}

// The near-miss the cap exists to refuse, and it is the same one region.maxRows
// already refuses for predicates: a row placed just beyond the region by an
// agent whose output abuts the chrome.
func TestAnExtractorRefusesARowBeyondItsRegionCap(t *testing.T) {
	f := grid(60, testRows, []string{"  mode line", "", "", "", "", "  ◯ Forged  not chrome"}, 0, 0)

	near, err := newDocumentDriver(panelDoc(4))
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	if got := near.Observe(f).Extras; len(got) != 0 {
		t.Fatalf("the row is five below the anchor and the cap is four; it was read anyway: %+v", got)
	}

	// And the contrast that makes it evidence: the same frame, the same
	// pattern, one more row of cap, and the row IS read. The refusal is the
	// bound doing its job, not the pattern failing to match.
	wide, err := newDocumentDriver(panelDoc(5))
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	if got := wide.Observe(f).Extras; len(got) != 1 {
		t.Fatalf("with the cap widened by one the row should be read: %+v", got)
	}
}

// An extractor that matches nothing contributes NO extra — not an empty one.
// Absent and empty are different claims, and only one of them is true.
func TestAnExtractorThatMatchesNothingContributesNoExtra(t *testing.T) {
	d, err := newDocumentDriver(panelDoc(4))
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	f := grid(60, testRows, []string{"  mode line", "", "  nothing panel-shaped here"}, 0, 0)
	if got := d.Observe(f).Extras; len(got) != 0 {
		t.Fatalf("extras = %+v, want none", got)
	}
}

// The falsifier, asserted structurally: the same document with and without its
// extractors answers the same scalar on every shape. Extraction happens beside
// the branch evaluation and never inside it, so a rule that reads more can
// never decide differently.
func TestExtrasCannotDecideTheScalarState(t *testing.T) {
	withExtractor := panelDoc(4)
	bare := withExtractor
	bare.Extractors = nil
	// And a third that captures everything it can see, to show that a rule
	// extracting MORE does not move the answer either.
	greedy := withExtractor
	greedy.Extractors = []Extractor{{
		Name:       "everything",
		Pattern:    `^(?P<text>.*)$`,
		RegionSpec: RegionSpec{Anchor: "modeLine", MaxRows: 4},
	}}

	frames := map[string][]string{
		"panel":   {"  mode line", "", "  ● main", "  ◯ Explore  List files"},
		"nothing": {"  mode line", "", "", ""},
		"prose":   {"  mode line", "", "  some transcript text", ""},
	}
	for name, lines := range frames {
		f := grid(60, testRows, lines, 0, 0)
		for label, doc := range map[string]Document{"bare": bare, "extractor": withExtractor, "greedy": greedy} {
			d, err := newDocumentDriver(doc)
			if err != nil {
				t.Fatalf("newDocumentDriver(%s): %v", label, err)
			}
			if got := d.Observe(f).State; got != StateFreeText {
				t.Errorf("%s/%s = %q, want %q", name, label, got, StateFreeText)
			}
		}
	}
}

// Classify is the SCALAR PROJECTION of Observe, not a second evaluation. Two
// evaluations of one question is how the two drift apart.
func TestClassifyIsTheProjectionOfObserve(t *testing.T) {
	d, err := newDocumentDriver(panelDoc(4))
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	f := grid(60, testRows, []string{"  mode line", "", "  ● main"}, 0, 0)
	if got, want := d.Classify(f), d.Observe(f).State; got != want {
		t.Fatalf("Classify = %q, Observe().State = %q", got, want)
	}
}

// ── a document that could never answer is refused at construction ─────────

func TestAnUncappedExtractorIsRefused(t *testing.T) {
	doc := panelDoc(0)
	if _, err := newDocumentDriver(doc); err == nil {
		t.Fatal("an extractor with no row cap was accepted; an uncapped region is how a forged row is reached")
	}
}

func TestAnExtractorAskingForMoreRowsThanTheEngineAllowsIsRefused(t *testing.T) {
	if _, err := newDocumentDriver(panelDoc(maxExtractorRows + 1)); err == nil {
		t.Fatalf("an extractor asking for %d rows was accepted; the ceiling is %d",
			maxExtractorRows+1, maxExtractorRows)
	}
	if _, err := newDocumentDriver(panelDoc(maxExtractorRows)); err != nil {
		t.Fatalf("an extractor asking for exactly the ceiling was refused: %v", err)
	}
}

func TestAnExtractorOnAnAnchorNothingBindsIsRefused(t *testing.T) {
	doc := panelDoc(4)
	doc.Extractors[0].Anchor = "nowhere"
	if _, err := newDocumentDriver(doc); err == nil {
		t.Fatal("an extractor naming an anchor no anchor binds was accepted")
	}
}

func TestAnExtractorWhosePatternDoesNotCompileIsRefused(t *testing.T) {
	doc := panelDoc(4)
	doc.Extractors[0].Pattern = `^(?P<name>\S+`
	if _, err := newDocumentDriver(doc); err == nil {
		t.Fatal("an extractor with an unparseable pattern was accepted")
	}
}

// A pattern that names no group reads nothing, which is a wiring mistake and
// belongs to process start rather than to the first frame that finds it silent.
func TestAnExtractorWhosePatternNamesNoFieldIsRefused(t *testing.T) {
	doc := panelDoc(4)
	doc.Extractors[0].Pattern = `^\s*[●◯]\s\S+$`
	if _, err := newDocumentDriver(doc); err == nil {
		t.Fatal("an extractor whose pattern names no capture group was accepted")
	}
}

func TestAnExtractorWithNoNameIsRefused(t *testing.T) {
	doc := panelDoc(4)
	doc.Extractors[0].Name = ""
	if _, err := newDocumentDriver(doc); err == nil {
		t.Fatal("an unnamed extractor was accepted; nothing could ever read its yield")
	}
}
