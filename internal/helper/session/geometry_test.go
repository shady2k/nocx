package session

import "testing"

// cellGeometry is the ONE boundary between the wire's units and the runtime's:
// the client reports cells plus TIOCSWINSZ's whole-text-area pixels, and the
// commit carries a PER-CELL metric. These tests pin the decode itself — the
// wiring around it is runtime_test.go's.
func TestCellGeometryDecodesWholeAreaPixelsPerCell(t *testing.T) {
	g := cellGeometry(80, 24, 720, 384)
	if g.Cols != 80 || g.Rows != 24 {
		t.Fatalf("cellGeometry moved the grid to %dx%d, want 80x24", g.Cols, g.Rows)
	}
	if g.CellWidthPx != 9 || g.CellHeightPx != 16 {
		t.Fatalf("cellGeometry decoded 720x384 over 80x24 as %dx%d px per cell, want 9x16",
			g.CellWidthPx, g.CellHeightPx)
	}
}

func TestCellGeometryRoundsToTheNearestWholePixel(t *testing.T) {
	// A report the wire could only carry rounded: 29 px over 3 cells is
	// 9.67 per cell, and the committed metric is the nearest whole pixel.
	g := cellGeometry(3, 2, 29, 33)
	if g.CellWidthPx != 10 || g.CellHeightPx != 17 {
		t.Fatalf("cellGeometry decoded 29x33 over 3x2 as %dx%d px per cell, want 10x17",
			g.CellWidthPx, g.CellHeightPx)
	}
}

func TestCellGeometryKeepsAnUnmeasuredSessionUnmeasured(t *testing.T) {
	// Zeros are a report of "not measured yet", and decoding them into an
	// invented metric would be the runtime guessing what a client never said.
	g := cellGeometry(80, 24, 0, 0)
	if g.CellWidthPx != 0 || g.CellHeightPx != 0 {
		t.Fatalf("cellGeometry invented %dx%d px per cell for an unmeasured report, want zeros",
			g.CellWidthPx, g.CellHeightPx)
	}
}
