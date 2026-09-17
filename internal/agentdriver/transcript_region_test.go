package agentdriver

// The transcript REGION, from the engine's side: what a rule must be able to
// say so that the third facet can measure an agent's own output (nocx-tnx44).
//
// Two bounds were added to the region grammar for it, and both are asserted
// here with their refusals:
//
//   - skipStatusGlyphs, so a region anchored at the input box's own chrome can
//     step over the status stack instead of ending on the spinner or measuring
//     the spinner's timer. The end of the stack is found POSITIONALLY — the
//     topmost row of the leading non-blank run whose own FIRST CELL is one of
//     the glyphs — because the stack is drawn at column 0 and the agent's
//     transcript is indented.
//   - toEdge, so that region's bound can be the frame's top edge (there is
//     nothing above the first row to reach) rather than a row count.
//
// A rule that reads MORE off a screen must still answer the same thing about
// it, so the falsifier is asserted over the whole corpus at the bottom.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentcapture/replaylocal"
	"github.com/shady2k/nocx/internal/paneview"
)

// transcriptDoc is the shape the shipped Claude rule uses: a transcript read
// UP from the token meter, to the frame's top edge, stepping over the status
// stack the spinner is drawn in.
func transcriptDoc() Document {
	return Document{
		Agent:   "test",
		Default: StateUnknown,
		Anchors: []AnchorSpec{
			{Name: "bottomRule", Kind: "searchUp", RuleGlyph: "─", Floor: 0},
			{Name: "prompt", Kind: "offset", From: "bottomRule", Offset: -1, RequireCell: "❯", RequireCol: 0},
			{Name: "meter", Kind: "offset", From: "prompt", Offset: -2},
		},
		Extractors: []Extractor{{
			Name:       "transcript",
			Pattern:    `^(?P<text>.*)$`,
			RegionSpec: RegionSpec{Anchor: "meter", Up: true, ToEdge: true, SkipStatusGlyphs: []string{"✻", "✢", "✽", "✶", "·", "*"}},
		}},
	}
}

// transcriptScreen is the chrome the corpus shows, with the rows a test wants
// above it. Row 5 (0-based) is the row directly above the token meter, which
// is where a status stack begins.
func transcriptScreen(above []string) paneview.Frame {
	const cols = 20
	rule := strings.Repeat("─", cols)
	lines := make([]string, 14)
	copy(lines, above)
	lines[6] = "              0 tokens"
	lines[7] = rule
	lines[8] = "❯ "
	lines[9] = rule
	lines[11] = "  ⏵⏵ auto mode on"
	return grid(cols, 14, lines, 2, 8)
}

// The whole point of the bound: the row directly above the meter is a tip line
// and the row above THAT is a live spinner whose timer moves every second.
// Neither is part of the yield; the rows above the stack are.
func TestTheTranscriptRegionStepsOverTheStatusStack(t *testing.T) {
	d, err := newDocumentDriver(transcriptDoc())
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	// The two-row stack claude-2.1.266-subagent-finished@80000 shows: a tip
	// line just above the box, the spinner above it, and only the spinner
	// carrying a status glyph.
	f := transcriptScreen([]string{"❯ write a story", "", "", "", "✻ Burrowing… (3s · thinking)", "  ⎿  Tip: something"})
	rows := d.Observe(f).Transcript()
	if len(rows) != 1 || rows[0] != "❯ write a story" {
		t.Fatalf("yield = %q, want just the transcript row", rows)
	}
}

// A transcript that FILLS the pane abuts the box with no chrome between, and
// the region must read it whole: the leading run has no status glyph in it, so
// there is no stack to step over.
func TestATranscriptWithNoStatusStackIsReadWhole(t *testing.T) {
	d, err := newDocumentDriver(transcriptDoc())
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	f := transcriptScreen([]string{"  first line", "  second line", "  third line", "  fourth line", "  fifth line", "  sixth line"})
	rows := d.Observe(f).Transcript()
	if len(rows) != 6 {
		t.Fatalf("yield = %d rows, want all 6: %q", len(rows), rows)
	}
	// Bottom-most first: the region is anchored at the box and walks up.
	if rows[0] != "  sixth line" || rows[5] != "  first line" {
		t.Fatalf("yield order = %q", rows)
	}
}

// THE REGRESSION the positional check exists for, and the reason the stack is
// found from a row's own FIRST CELL rather than from its first non-blank one.
//
// "  * item" two columns into a markdown list is content. A bound that trimmed
// indentation would read it as a spinner, decide the stack began there, and
// hide the row — and everything under it — from the measurement.
func TestAnIndentedTranscriptRowThatOpensWithAStatusGlyphIsStillContent(t *testing.T) {
	d, err := newDocumentDriver(transcriptDoc())
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	// Directly above the meter, with no blank between, so the leading run
	// reaches it.
	f := transcriptScreen([]string{"❯ counts", "  * one", "  * two", "", "", "  * newest"})
	rows := d.Observe(f).Transcript()
	if len(rows) != 4 {
		t.Fatalf("yield = %d rows, want all 4: %q", len(rows), rows)
	}
	if rows[0] != "  * newest" {
		t.Fatalf("the row nearest the box is missing from the yield: %q", rows)
	}
}

// And the other direction, stated as the contrast that makes the rule above
// evidence rather than an accident: the SAME row text at column 0 IS a status
// row, and the region steps over it.
func TestTheSameRowAtColumnZeroIsStatusChrome(t *testing.T) {
	d, err := newDocumentDriver(transcriptDoc())
	if err != nil {
		t.Fatalf("newDocumentDriver: %v", err)
	}
	f := transcriptScreen([]string{"❯ counts", "", "", "", "", "* Misting… (2s)"})
	rows := d.Observe(f).Transcript()
	if len(rows) != 1 || rows[0] != "❯ counts" {
		t.Fatalf("yield = %q, want just the transcript row", rows)
	}
}

// ── the bounds are the engine's, and a document may not lift them ─────────

func TestATranscriptRegionThatReadsDownToTheFrameEdgeIsRefused(t *testing.T) {
	doc := transcriptDoc()
	doc.Extractors[0].Up = false
	if _, err := newDocumentDriver(doc); err == nil {
		t.Fatal("a region reading DOWN to the frame edge was accepted; the chrome below an anchor is what the row cap exists for")
	}
}

func TestARegionNamingBothACapAndTheFrameEdgeIsRefused(t *testing.T) {
	doc := transcriptDoc()
	doc.Extractors[0].MaxRows = 4
	if _, err := newDocumentDriver(doc); err == nil {
		t.Fatal("a region with two bounds was accepted; two bounds are how they come to disagree")
	}
}

func TestACapAndTheFrameEdgeAreEachAcceptedAlone(t *testing.T) {
	capped := transcriptDoc()
	capped.Extractors[0].ToEdge = false
	capped.Extractors[0].MaxRows = maxExtractorRows
	if _, err := newDocumentDriver(capped); err != nil {
		t.Fatalf("a capped region was refused: %v", err)
	}
	edge := transcriptDoc()
	if _, err := newDocumentDriver(edge); err != nil {
		t.Fatalf("a region bounded by the frame edge was refused: %v", err)
	}
}

func TestAStepOverTheStatusStackThatReadsDownIsRefused(t *testing.T) {
	doc := transcriptDoc()
	doc.Extractors[0].ToEdge = false
	doc.Extractors[0].MaxRows = 4
	doc.Extractors[0].Up = false
	if _, err := newDocumentDriver(doc); err == nil {
		t.Fatal("a region stepping over a status stack without reading up was accepted")
	}
}

// ── the falsifier, over the whole corpus ─────────────────────────────────

// ACCEPTANCE FOUR: a rule that reads more off a screen can never answer that
// screen differently.
//
// The shipped Claude rule is classified twice on every frame of every
// committed capture — once as it ships, once with its extractors removed
// entirely — and the two must agree on every one. This is asserted here, in
// the package that owns the document, because building "the rule without its
// extractors" is a thing only this package can do.
func TestTheTranscriptExtractorCannotChangeTheState(t *testing.T) {
	var shipped Document
	if err := json.Unmarshal(claudeRuleJSON, &shipped); err != nil {
		t.Fatalf("unmarshal the shipped rule: %v", err)
	}
	withExtractors, err := newDocumentDriver(shipped)
	if err != nil {
		t.Fatalf("newDocumentDriver(shipped): %v", err)
	}
	bare := shipped
	bare.Extractors = nil
	without, err := newDocumentDriver(bare)
	if err != nil {
		t.Fatalf("newDocumentDriver(without extractors): %v", err)
	}

	captures, err := filepath.Glob(filepath.Join("testdata", "captures", "*.jsonl"))
	if err != nil || len(captures) == 0 {
		t.Fatalf("no captures to sweep (%d, %v)", len(captures), err)
	}
	read := 0
	for _, path := range captures {
		header, chunks, err := agentcapture.Read(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for at := int64(0); at <= 130000; at += 2000 {
			moments, err := agentcapture.Frames(context.Background(), replaylocal.Replayer{}, header, chunks, []int64{at})
			if err != nil {
				t.Fatalf("replay %s@%dms: %v", path, at, err)
			}
			f := moments[0].Frame
			if got, want := withExtractors.Classify(f), without.Classify(f); got != want {
				t.Fatalf("%s@%dms: with extractors = %q, without = %q", filepath.Base(path), at, got, want)
			}
			if len(withExtractors.Observe(f).Extras) > 0 {
				read++
			}
		}
	}
	// Not vacuous: the sweep has to have found frames where the extractors
	// actually read something, or it proves nothing about reading more.
	if read == 0 {
		t.Fatal("no frame in the corpus produced an extractor yield")
	}
}
