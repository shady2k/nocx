package ghostty

import (
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// A clear boundary (nocx-2v80t.3.17): the program erasing the display AND
// its saved lines — ED3, `CSI 3 J`, ESC [ 3 J. This is SCANNED for, the way
// the fence and the output mark are (fence.go, output_mark.go), rather than
// read off a side effect of the library's own state: the library still
// parses and executes ED3 exactly as before, and the scanner only locates
// where it sits in the feed. These tests assert that a real ED3 produces
// exactly one EffectClearBoundary REGARDLESS of what it did to the
// scrollback depth — the case that mattered on the e2e is a session whose
// depth was already zero before the erase, which a depth-transition signal
// cannot see at all (nocx-2v80t.3.17) — and that plain ED2 (erase display
// alone — what a full-screen program redrawing sends) never does:
// nocx-2v80t.3.17's own paired acceptance criterion.

// clearSeq is exactly what `clear(1)` emits (nocx-zg3k3.10.3's owner
// decision, verified): home the cursor, erase the display, erase the saved
// lines.
const clearSeq = "\x1b[H\x1b[2J\x1b[3J"

func wantOneClearBoundary(t *testing.T, got []emulator.Effect) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("the drain holds %s, want exactly one clear-boundary effect", describeEffects(got))
	}
	if got[0].Kind != emulator.EffectClearBoundary {
		t.Fatalf("the effect is %s, want a clear boundary", describeEffects(got))
	}
	if len(got[0].Body) != 0 {
		t.Fatalf("the clear boundary carries body %q, want none", got[0].Body)
	}
}

// TestEraseSavedLinesProducesAClearBoundary: a screen with real scrollback
// behind it, then the program discards it. Exactly the shape a shell's own
// `clear` produces.
func TestEraseSavedLinesProducesAClearBoundary(t *testing.T) {
	term := departedTerm(t, 20, 4)
	departedFeed(t, term, numbered(10))
	if got := term.Effects(); len(got) != 0 {
		t.Fatalf("filling scrollback produced %s, want no effect", describeEffects(got))
	}
	departedFeed(t, term, clearSeq)
	wantOneClearBoundary(t, term.Effects())
}

// TestEraseDisplayAloneProducesNoClearBoundary: ED2 with no ED3 — a
// full-screen program redrawing its own view, the case nocx-2v80t.3.17
// names by name ("a command that prints ESC[2J alone… hides no blocks").
func TestEraseDisplayAloneProducesNoClearBoundary(t *testing.T) {
	term := departedTerm(t, 20, 4)
	departedFeed(t, term, numbered(10))
	term.Effects() // drain whatever the fill produced, if anything
	departedFeed(t, term, "\x1b[H\x1b[2J")
	if got := term.Effects(); len(got) != 0 {
		t.Fatalf("ED2 alone produced %s, want no effect: a redrawing program must hide no block", describeEffects(got))
	}
}

// TestEraseSavedLinesOnAScreenThatNeverScrolledStillProducesAClearBoundary
// is the e2e's own defect, reproduced at the unit level: a screen tall
// enough that ordinary output never pushed a row into history has a
// scrollback depth of zero BEFORE the erase, and ED3 leaves it at zero too
// — 0 to 0 is not a transition a depth-based signal can see, and this is
// exactly the shape "run one short command, then `clear`" takes. The
// boundary must still be sighted: the feature hides blocks that are
// currently VISIBLE, not only ones that already scrolled into history.
func TestEraseSavedLinesOnAScreenThatNeverScrolledStillProducesAClearBoundary(t *testing.T) {
	term := departedTerm(t, 40, 24) // a real pane's shape: nowhere near full
	departedFeed(t, term, "hello\r\n")
	if got := term.Effects(); len(got) != 0 {
		t.Fatalf("one short line produced %s, want no effect", describeEffects(got))
	}
	departedFeed(t, term, clearSeq)
	wantOneClearBoundary(t, term.Effects())
}

// TestEraseSavedLinesWithNoScrollbackAtAllStillProducesAClearBoundary: a
// fresh terminal, `clear` as the very first bytes — the boundary is still
// sighted (a session with nothing to hide records a boundary that hides
// nothing, which content.RecordClearBoundary already handles honestly) and
// the sighting does not depend on there having been ANY prior output.
func TestEraseSavedLinesWithNoScrollbackAtAllStillProducesAClearBoundary(t *testing.T) {
	term := departedTerm(t, 20, 4)
	departedFeed(t, term, clearSeq)
	wantOneClearBoundary(t, term.Effects())
}

// TestEraseSavedLinesTwiceProducesTwoClearBoundaries: each real erase is its
// own sighting — a second `clear` after fresh output must not be silently
// swallowed because the first one already fired once for this terminal.
func TestEraseSavedLinesTwiceProducesTwoClearBoundaries(t *testing.T) {
	term := departedTerm(t, 20, 4)
	departedFeed(t, term, numbered(10))
	departedFeed(t, term, clearSeq)
	wantOneClearBoundary(t, term.Effects())
	departedFeed(t, term, numbered(10))
	departedFeed(t, term, clearSeq)
	wantOneClearBoundary(t, term.Effects())
}

// TestEraseSavedLinesStraddlingTwoIngestCalls: the scanner's state is a byte
// position, not a buffer — a chunk boundary landing mid-sequence must not
// lose the sighting, the same property fence_test.go and
// output_mark_test.go assert for their own sequences.
func TestEraseSavedLinesStraddlingTwoIngestCalls(t *testing.T) {
	term := departedTerm(t, 20, 4)
	departedFeed(t, term, clearSeq[:2])
	if got := term.Effects(); len(got) != 0 {
		t.Fatalf("a partial ED3 produced %s, want nothing yet", describeEffects(got))
	}
	departedFeed(t, term, clearSeq[2:])
	wantOneClearBoundary(t, term.Effects())
}

// TestEraseSavedLinesNeverEatsTheBytesAroundIt: the scanner splits the feed,
// it never withholds bytes — output before and after ED3 both reach the
// screen, in the same ingest call it arrived in.
func TestEraseSavedLinesNeverEatsTheBytesAroundIt(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, "before"+clearSeq+"after")
	wantOneClearBoundary(t, term.Effects())
	if text := screenText(t, term, 4); text != "after\n\n\n\n" {
		t.Fatalf("the screen reads %q, want ED3 to have cost no bytes either side", text)
	}
}
