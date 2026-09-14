package sessionruntime

// Task 4's acceptance criteria for Digest (the-session-surface plan,
// nocx-6q1uh.4): the hash differs for a grapheme, a width, a style-only
// change, a wrap flag, an alt-screen toggle, a geometry change and a cursor
// move (when included), and is UNCHANGED for anything outside the row range
// it was asked about.

import (
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

func baseIdentity() ScreenIdentity {
	return ScreenIdentity{
		At:             Incarnation{Session: "sess-1", Generation: 1},
		AltScreen:      false,
		BufferInstance: 1,
		Cols:           10,
		Rows:           3,
	}
}

// textRow builds a row of narrow, textual cells from s, padded to width with
// blanks — enough shape for Digest's own tests, which never need a wide
// cluster or a spacer.
func textRow(s string, width int) emulator.Row {
	cells := make([]emulator.Cell, width)
	for i := range width {
		g := " "
		if i < len(s) {
			g = string(s[i])
		}
		cells[i] = emulator.Cell{Grapheme: g, Width: emulator.WidthNarrow, HasText: g != " "}
	}
	return emulator.Row{Cells: cells}
}

func baseRows() []emulator.Row {
	return []emulator.Row{
		textRow("hello", 10),
		textRow("world", 10),
		textRow("spinner", 10),
	}
}

func baseRange() RowRange { return RowRange{First: 0, Last: 1} }

func digestOf(t *testing.T, id ScreenIdentity, rows []emulator.Row, r RowRange, cur emulator.Cursor, includeCursor bool) [32]byte {
	t.Helper()
	return Digest(id, rows, r, cur, includeCursor)
}

func TestDigestDiffersWhenAGraphemeChanges(t *testing.T) {
	id, rows, r := baseIdentity(), baseRows(), baseRange()
	base := digestOf(t, id, rows, r, emulator.Cursor{}, false)

	changed := baseRows()
	changed[0].Cells[0].Grapheme = "H"
	got := digestOf(t, id, changed, r, emulator.Cursor{}, false)

	if base == got {
		t.Fatal("digest did not change when a grapheme in the row range changed")
	}
}

func TestDigestDiffersWhenAWidthChanges(t *testing.T) {
	id, rows, r := baseIdentity(), baseRows(), baseRange()
	base := digestOf(t, id, rows, r, emulator.Cursor{}, false)

	changed := baseRows()
	changed[0].Cells[0].Width = emulator.WidthWide
	got := digestOf(t, id, changed, r, emulator.Cursor{}, false)

	if base == got {
		t.Fatal("digest did not change when a cell's width changed")
	}
}

func TestDigestDiffersWhenOnlyAStyleAttributeChanges(t *testing.T) {
	id, rows, r := baseIdentity(), baseRows(), baseRange()
	base := digestOf(t, id, rows, r, emulator.Cursor{}, false)

	changed := baseRows()
	// A selection highlight: the text is identical, only the style
	// (inverse video) differs.
	changed[0].Cells[0].Style.Attributes |= emulator.AttrInverse
	got := digestOf(t, id, changed, r, emulator.Cursor{}, false)

	if base == got {
		t.Fatal("digest did not change when a style attribute changed with no text change")
	}
}

func TestDigestDiffersWhenAWrapFlagChanges(t *testing.T) {
	id, rows, r := baseIdentity(), baseRows(), baseRange()
	base := digestOf(t, id, rows, r, emulator.Cursor{}, false)

	changed := baseRows()
	changed[0].Wrap = true
	got := digestOf(t, id, changed, r, emulator.Cursor{}, false)

	if base == got {
		t.Fatal("digest did not change when a row's wrap flag changed")
	}
}

func TestDigestDiffersWhenAltScreenToggles(t *testing.T) {
	rows, r := baseRows(), baseRange()
	base := digestOf(t, baseIdentity(), rows, r, emulator.Cursor{}, false)

	toggled := baseIdentity()
	toggled.AltScreen = true
	got := digestOf(t, toggled, rows, r, emulator.Cursor{}, false)

	if base == got {
		t.Fatal("digest did not change when AltScreen toggled")
	}
}

func TestDigestDiffersWhenGeometryChanges(t *testing.T) {
	rows, r := baseRows(), baseRange()
	base := digestOf(t, baseIdentity(), rows, r, emulator.Cursor{}, false)

	resized := baseIdentity()
	resized.Cols = 20
	got := digestOf(t, resized, rows, r, emulator.Cursor{}, false)

	if base == got {
		t.Fatal("digest did not change when the geometry (Cols) changed")
	}
}

func TestDigestDiffersWhenTheCursorMovesAndIsIncluded(t *testing.T) {
	id, rows, r := baseIdentity(), baseRows(), baseRange()
	cur := emulator.Cursor{X: 1, Y: 1, Visible: true}
	base := digestOf(t, id, rows, r, cur, true)

	moved := emulator.Cursor{X: 2, Y: 1, Visible: true}
	got := digestOf(t, id, rows, r, moved, true)

	if base == got {
		t.Fatal("digest did not change when the cursor moved and includeCursor was set")
	}
}

func TestDigestIgnoresTheCursorWhenNotIncluded(t *testing.T) {
	id, rows, r := baseIdentity(), baseRows(), baseRange()
	base := digestOf(t, id, rows, r, emulator.Cursor{X: 1, Y: 1, Visible: true}, false)
	got := digestOf(t, id, rows, r, emulator.Cursor{X: 5, Y: 2, Visible: false}, false)

	if base != got {
		t.Fatal("digest changed on a cursor move even though includeCursor was false")
	}
}

// TestDigestIsUnchangedByARowOutsideTheRange is the plan's own example: a
// spinner spinning on a row the caller did not ask about must never
// invalidate a target minted against the rows it DID ask about.
func TestDigestIsUnchangedByARowOutsideTheRange(t *testing.T) {
	id, r := baseIdentity(), baseRange() // rows 0..1
	rows := baseRows()
	base := digestOf(t, id, rows, r, emulator.Cursor{}, false)

	spinning := baseRows()
	spinning[2] = textRow("spinnerX", 10) // row 2 is outside r
	got := digestOf(t, id, spinning, r, emulator.Cursor{}, false)

	if base != got {
		t.Fatal("digest changed when only a row OUTSIDE the range changed")
	}
}
