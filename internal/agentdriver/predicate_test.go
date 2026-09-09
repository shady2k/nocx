package agentdriver

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/panegrid"
)

// A predicate is a pure function of one panegrid.Frame, so these tests build
// frames directly rather than through a Store. That is deliberate and it is
// not the shortcut capture_test.go warns against: the fidelity of a Frame to a
// screen the product actually produces is what the capture corpus asserts, and
// it asserts it at the DRIVER level, over the real Store, fed from byte zero.
// What is under test here is narrower — given this grid, does this predicate
// say yes — and a hand-built grid states that question without a replay in the
// way of reading it.

// grid builds a frame from rows of runes, one cell per rune, no double-width.
func grid(cols, rows int, lines []string, cursorX, cursorY int) panegrid.Frame {
	f := panegrid.Frame{Cols: cols, Rows: rows, CursorX: cursorX, CursorY: cursorY}
	f.Lines = make([][]panegrid.Cell, rows)
	for y := 0; y < rows; y++ {
		row := make([]panegrid.Cell, cols)
		var runes []rune
		if y < len(lines) {
			runes = []rune(lines[y])
		}
		for x := 0; x < cols; x++ {
			if x < len(runes) {
				row[x] = panegrid.Cell{Text: string(runes[x]), Width: 1}
				continue
			}
			row[x] = panegrid.Cell{Text: " ", Width: 1}
		}
		f.Lines[y] = row
	}
	return f
}

func TestCursorOnMatchesTheCellTheCursorIsParkedOn(t *testing.T) {
	f := grid(10, 3, []string{"", " ❯ 1. Yes", ""}, 1, 1)
	if !cursorOn(f, "❯") {
		t.Fatal("cursor is on the marker and cursorOn said no")
	}
}

// The near-miss cursor_on exists to refuse: the same glyph printed into the
// transcript. Printed text cannot take the cursor, and that is the whole of
// why this predicate is the unforgeable one.
func TestCursorOnRefusesTheSameGlyphPrintedElsewhere(t *testing.T) {
	f := grid(10, 3, []string{"", " ❯ 1. Yes", ""}, 0, 0)
	if cursorOn(f, "❯") {
		t.Fatal("the marker is on row 1 and the cursor is on row 0; cursorOn said yes")
	}
}

func TestCursorOnRefusesAnOutOfRangeCursor(t *testing.T) {
	f := grid(10, 3, []string{"❯"}, 99, 99)
	if cursorOn(f, "❯") {
		t.Fatal("an out-of-range cursor matched")
	}
}

func TestRowOpensWithMatchesTheFirstNonBlankCell(t *testing.T) {
	f := grid(10, 2, []string{"   ● main"}, 0, 0)
	if !rowOpensWith(f, 0, "●") {
		t.Fatal("the row's first non-blank cell is the glyph and rowOpensWith said no")
	}
}

func TestRowOpensWithRefusesAGlyphThatDoesNotOpenTheRow(t *testing.T) {
	f := grid(20, 2, []string{"   text then ● here"}, 0, 0)
	if rowOpensWith(f, 0, "●") {
		t.Fatal("the glyph is mid-row and rowOpensWith said yes")
	}
}

// cellAtCol is NOT rowOpensWith, and keeping them apart is a safety property:
// the input box requires its marker at column 0 exactly, while the menu marker
// is allowed to open an indented row. Folding the first into the second would
// weaken one of the two markers the driver's safety argument rests on.
func TestCellAtColMatchesOnlyTheExactColumn(t *testing.T) {
	f := grid(10, 2, []string{"❯ hello"}, 0, 0)
	if !cellAtCol(f, 0, 0, "❯") {
		t.Fatal("the glyph is at column 0 and cellAtCol said no")
	}
}

func TestCellAtColRefusesAnIndentedGlyphThatRowOpensWithWouldAccept(t *testing.T) {
	f := grid(10, 2, []string{"   ❯ hello"}, 0, 0)
	if cellAtCol(f, 0, 0, "❯") {
		t.Fatal("the glyph is at column 3 and cellAtCol accepted it at column 0")
	}
	if !rowOpensWith(f, 0, "❯") {
		t.Fatal("rowOpensWith should accept the same row, which is what makes the two distinct")
	}
}

// And a predicate no branch of holds() could reach is not in the closed set at
// all — it is Go somebody wrote and no document can name. rowOpensWith was
// exactly that when the rule became a document: implemented, tested directly,
// and absent from the evaluator's switch, so the "closed set a rule composes"
// was one smaller than the file claimed. This asserts the composition, which is
// the only thing that makes a predicate part of the grammar.
func TestADocumentCanComposeRowOpensWith(t *testing.T) {
	doc := Document{
		Agent:   "test",
		Default: StateUnknown,
		// The frame's own bottom row, which is where the two grids below
		// put the row under test.
		Anchors: []AnchorSpec{{Name: "row", Kind: "offset", Offset: 0}},
		Branches: []Branch{{
			State: StateWorking,
			When:  []Pred{{Kind: "rowOpensWith", Glyph: "●", RegionSpec: RegionSpec{Anchor: "row"}}},
		}},
	}
	d, err := newDocumentDriver(doc)
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	if got := d.Classify(grid(20, 2, []string{"", "   ● main"}, 0, 0)); got != StateWorking {
		t.Errorf("an indented marker opening its row = %q, want %q", got, StateWorking)
	}
	if got := d.Classify(grid(20, 2, []string{"", "   text then ● here"}, 0, 0)); got != StateUnknown {
		t.Errorf("a mid-row glyph = %q, want %q", got, StateUnknown)
	}
}

func TestFullWidthRuleMatchesARowThatIsNothingElse(t *testing.T) {
	f := grid(6, 2, []string{"──────"}, 0, 0)
	if !fullWidthRule(f, 0, "─") {
		t.Fatal("a row of six rules in six columns did not match")
	}
}

func TestFullWidthRuleRefusesARowWithAnythingElseInIt(t *testing.T) {
	f := grid(6, 2, []string{"───x──"}, 0, 0)
	if fullWidthRule(f, 0, "─") {
		t.Fatal("a row with a non-rule cell matched")
	}
}

func TestFullWidthRuleRefusesARowShorterThanTheFrame(t *testing.T) {
	f := grid(6, 2, []string{"──────"}, 0, 0)
	f.Lines[0] = f.Lines[0][:4]
	if fullWidthRule(f, 0, "─") {
		t.Fatal("a short row matched")
	}
}

func TestRowContainsReadsTheAnchorsOwnRow(t *testing.T) {
	f := grid(40, 2, []string{"  auto mode on · /tasks to see subagents"}, 0, 0)
	if !rowContains(f, 0, "/tasks to see subagents") {
		t.Fatal("the text is on the anchor's row and rowContains said no")
	}
}

func TestRowContainsRefusesTextOnAnotherRow(t *testing.T) {
	f := grid(40, 3, []string{"", "  /tasks to see subagents"}, 0, 0)
	if rowContains(f, 0, "/tasks to see subagents") {
		t.Fatal("the text is on row 1 and rowContains was asked about row 0")
	}
}

func TestNearestNonBlankAboveSkipsBlanksAndReadsTheRow(t *testing.T) {
	f := grid(40, 5, []string{"", " Do you want to create note.txt?", "", "", " ❯ 1. Yes"}, 0, 0)
	got, ok := nearestNonBlankAbove(f, 4)
	if !ok {
		t.Fatal("there is a non-blank row above and it was not found")
	}
	if want := "Do you want to"; !strings.Contains(got, want) {
		t.Fatalf("nearest non-blank above row 4 = %q, want it to contain %q", got, want)
	}
}

func TestNearestNonBlankAboveRefusesWhenEverythingAboveIsBlank(t *testing.T) {
	f := grid(10, 3, []string{"", "", " ❯ 1. Yes"}, 0, 0)
	if _, ok := nearestNonBlankAbove(f, 2); ok {
		t.Fatal("every row above is blank and a row was returned anyway")
	}
}

// The spinner region: up from an anchor, capped, column-0 rows only, and it
// TERMINATES at the first blank row rather than skipping it. The terminator is
// not a detail — claude-subagent at 70s has a finished-turn summary at the
// anchor's own offset with a blank above it, and a region that skipped blanks
// would keep climbing into the transcript.
func TestRegionFindsAMatchWithinItsCap(t *testing.T) {
	f := grid(30, 6, []string{"", "", "* Misting… (2s)", "● high · /effort", "─────", ""}, 0, 0)
	r := region{anchor: 3, up: true, maxRows: 4, col0Only: true, stopAtBlank: true}
	if !r.anyRow(f, func(text string) bool { return strings.Contains(text, "… (") && strings.HasSuffix(text, ")") }) {
		t.Fatal("the spinner is one row above the anchor and the region did not find it")
	}
}

func TestRegionStopsAtTheFirstBlankRowRatherThanSkippingIt(t *testing.T) {
	f := grid(30, 6, []string{"* Misting… (2s)", "", "✻ Sautéed for 29s", "● high · /effort", "─────", ""}, 0, 0)
	r := region{anchor: 3, up: true, maxRows: 4, col0Only: true, stopAtBlank: true}
	if r.anyRow(f, func(text string) bool { return strings.Contains(text, "… (") && strings.HasSuffix(text, ")") }) {
		t.Fatal("a blank row sits between the anchor and the live spinner; the region climbed past it")
	}
}

// The near-miss region exists to refuse: an agent whose printed lines abut the
// chrome, trying to extend the region a forged marker is looked for in.
func TestRegionRefusesAMatchBeyondItsCap(t *testing.T) {
	lines := []string{"* Forged… (9s)", "a", "b", "c", "d", "● high · /effort"}
	f := grid(30, 7, lines, 0, 0)
	r := region{anchor: 5, up: true, maxRows: 4, col0Only: true, stopAtBlank: true}
	if r.anyRow(f, func(text string) bool { return strings.Contains(text, "… (") && strings.HasSuffix(text, ")") }) {
		t.Fatal("the forged spinner is five rows up and the cap is four; the region reached it")
	}
}

func TestRegionSkipsIndentedRowsWithoutTerminating(t *testing.T) {
	f := grid(30, 6, []string{"* Misting… (2s)", "   indented transcript", "● high · /effort"}, 0, 0)
	r := region{anchor: 2, up: true, maxRows: 4, col0Only: true, stopAtBlank: true}
	if !r.anyRow(f, func(text string) bool { return strings.Contains(text, "… (") && strings.HasSuffix(text, ")") }) {
		t.Fatal("an indented row should be skipped, not terminate the region")
	}
}

func TestNumberedOptionMatchesTheOptionGrammar(t *testing.T) {
	for _, s := range []string{" 1. Yes", "2. No", "  10. Something"} {
		if !numberedOption(s) {
			t.Fatalf("%q is a numbered option and was refused", s)
		}
	}
}

func TestNumberedOptionRefusesNearMisses(t *testing.T) {
	for _, s := range []string{"", "1.", "1.Yes", ". Yes", "x. Yes", "1 Yes"} {
		if numberedOption(s) {
			t.Fatalf("%q is not a numbered option and was accepted", s)
		}
	}
}

// below_mode_line_opens_only_with is THREE-valued, and a boolean would collapse
// two of the three in the expensive direction: a counterexample must answer
// unknown, while "nothing drawn down there at all" must fall through to the
// branches after it.
func TestBelowAnchorOpensOnlyWithDistinguishesItsThreeCases(t *testing.T) {
	panel := grid(20, 5, []string{"────", "  mode line", "  ● main", "  ◯ Explore"}, 0, 0)
	if got := belowAnchorOpensOnlyWith(panel, 0, []string{"●", "◯"}); got != belowAllMatched {
		t.Fatalf("a task panel = %v, want belowAllMatched", got)
	}

	other := grid(20, 5, []string{"────", "  mode line", "  ● main", "  ? what is this"}, 0, 0)
	if got := belowAnchorOpensOnlyWith(other, 0, []string{"●", "◯"}); got != belowCounterexample {
		t.Fatalf("an unrecognised row = %v, want belowCounterexample", got)
	}

	empty := grid(20, 5, []string{"────", "  mode line", "", ""}, 0, 0)
	if got := belowAnchorOpensOnlyWith(empty, 0, []string{"●", "◯"}); got != belowNothing {
		t.Fatalf("nothing below the mode line = %v, want belowNothing", got)
	}
}

func TestBelowAnchorOpensOnlyWithAnswersNothingWhenThereIsNoModeLine(t *testing.T) {
	f := grid(20, 3, []string{"────", "", ""}, 0, 0)
	if got := belowAnchorOpensOnlyWith(f, 0, []string{"●"}); got != belowNothing {
		t.Fatalf("no mode line at all = %v, want belowNothing", got)
	}
}
