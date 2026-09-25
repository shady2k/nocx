package sessionruntime

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// A ROW ERASED IN PLACE AND THEN REFLOWED IS NOT TAKEN FOR THE STORED SCREEN
// (nocx-2v80t.3.13).
//
// The boundary window is closed against a `clear` by two layers
// (nocx-2v80t.3.10): the tracked pin on each entry, which dies on a reset but
// stays alive through an ordinary erase (ESC[H ESC[2J blanks the row in place),
// and the reconciliation at a settle with no screen, which reads the fresh
// screen and drops what no longer reads what the window recorded. That second
// layer compared BY POSITION, so it stood down whenever the geometry had moved
// since capture — and an erase followed by a resize before the next boundary
// was then closed by neither: the next command's first lines, repeating the
// erased rows' old text, were swallowed as though the destroyed rows were
// leaving again.
//
// The same schedule without the resize is the pair: it passed before the fix
// and must keep passing.
func TestARowErasedInPlaceThenReflowedIsNotTakenForTheStoredScreen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		resize *Geometry
	}{
		{name: "an erase with no resize after it"},
		{name: "an erase and then a pane that becomes 148x33", resize: ptrGeometry(harnessGeometry(148, 33))},
		{name: "an erase and then a pane that becomes 100x24", resize: ptrGeometry(harnessGeometry(100, 24))},
		{name: "an erase and then a pane that becomes 80x23", resize: ptrGeometry(harnessGeometry(80, 23))},
		{name: "an erase and then a pane that becomes 60x24", resize: ptrGeometry(harnessGeometry(60, 24))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rs := streamSession(t, harnessGeometry(80, 24))

			// Command A: short enough that nothing departs, so its whole
			// closing screen is the window.
			obsFeed(t, s, 0, 10)
			obsSeal(t, s, obsNonce(1))
			if boundary, _ := boundaryScreen(t, rs); len(boundary) == 0 {
				t.Fatal("the boundary carried no closing screen")
			}
			if len(s.pendingScreen) == 0 {
				t.Fatal("the seal installed no window")
			}

			// `clear`: the rows are erased in place. Nothing departs, and the
			// pins stay alive (TestTrackRowStaysAliveThroughAPlainErase).
			if err := s.Ingest([]byte("\x1b[H\x1b[2J")); err != nil {
				t.Fatalf("clear: %v", err)
			}
			// The pane reflows BEFORE the next boundary settles.
			if tc.resize != nil {
				if _, err := s.CommitGeometry(*tc.resize); err != nil {
					t.Fatalf("commit the geometry: %v", err)
				}
			}
			// `clear`'s completion arrives and its sighting never does: the next
			// command's fence settles it with no screen (ADR-0074 case 3).
			s.Completed(s.Incarnation(), obsNonce(2), 0)
			if err := s.SightFence(obsNonce(3), []byte("fence-source")); err != nil {
				t.Fatalf("sight command B's fence: %v", err)
			}

			// Command B prints exactly the erased rows' old text first, and
			// enough after it to push those lines off the screen for real.
			obsFeed(t, s, 0, 60)
			obsSeal(t, s, obsNonce(4))

			var streamed []string
			for _, e := range rs.snapshot() {
				if e.kind != "rows" {
					continue
				}
				for _, row := range e.rows {
					streamed = append(streamed, streamRowText(row))
				}
			}
			if len(streamed) < 10 {
				t.Fatalf("command B departed %d rows, want at least its first ten: the schedule stopped proving anything", len(streamed))
			}
			for i := range 10 {
				want := fmt.Sprintf("L%06d", i)
				if streamed[i] != want {
					t.Fatalf("command B's departing row %d was %q, want %q: a row erased before the "+
						"reflow was taken for the stored screen leaving again (streamed head %q)",
						i, streamed[i], want, streamed[:10])
				}
			}
		})
	}
}

func ptrGeometry(g Geometry) *Geometry { return &g }

// The other direction of the same reconciliation: a window nothing wrote over
// must SURVIVE a reflow and a settle with no screen. Reading each entry through
// its pin is what makes that possible where the positional comparison had to
// stand down; a reconciliation that simply dropped the window whenever the
// geometry moved would pass the test above and store the closing screen twice.
func TestAReflowedWindowNobodyWroteOverSurvivesASettleWithNoScreen(t *testing.T) {
	for _, g := range []Geometry{
		harnessGeometry(148, 33),
		harnessGeometry(100, 24),
		harnessGeometry(80, 23),
		harnessGeometry(60, 24),
	} {
		t.Run(fmt.Sprintf("a pane that becomes %dx%d", g.Cols, g.Rows), func(t *testing.T) {
			s, rs := streamSession(t, harnessGeometry(80, 24))

			obsFeed(t, s, 0, 40)
			obsSeal(t, s, obsNonce(1))
			boundary, endRow := boundaryScreen(t, rs)
			if len(boundary) == 0 {
				t.Fatal("the boundary carried no closing screen")
			}

			if _, err := s.CommitGeometry(g); err != nil {
				t.Fatalf("commit the geometry: %v", err)
			}
			s.Completed(s.Incarnation(), obsNonce(2), 0)
			if err := s.SightFence(obsNonce(3), []byte("fence-source")); err != nil {
				t.Fatalf("sight the next fence: %v", err)
			}
			if len(s.pendingScreen) == 0 {
				t.Fatal("the settle dropped a window nothing wrote over")
			}

			obsFeed(t, s, 100, 60)
			for _, e := range rs.snapshot() {
				if e.kind != "rows" || e.from < endRow {
					continue
				}
				for i, row := range e.rows {
					if text := streamRowText(row); boundary[text] {
						index := e.from + uint64(i) // #nosec G115 -- a slice index, never negative
						t.Fatalf("row %q left the screen at index %d and streamed again after a reflow and a settle with no screen", text, index)
					}
				}
			}
		})
	}
}

// A row that is part of a soft-wrapped line is re-cut by a reflow without
// anything writing to it; the reconciliation must read that as unwritten, and
// still read an erase as written.
func TestStillReadsAsCapturedAcrossARewrap(t *testing.T) {
	row := func(text string, wrap, cont bool) emulator.Row {
		cells := make([]emulator.Cell, 0, len(text))
		for _, r := range text {
			cells = append(cells, emulator.Cell{Grapheme: string(r)})
		}
		return emulator.Row{Cells: cells, Wrap: wrap, Continuation: cont}
	}
	for _, tc := range []struct {
		name          string
		captured, now emulator.Row
		want          bool
	}{
		{"the same line", row("hello", false, false), row("hello   ", false, false), true},
		{"a narrowing re-cut reads the first piece", row("abcdefghij", false, false), row("abcde", true, false), true},
		{"a widening merges a continuation into its line", row("klmno", false, true), row("abcdefghijklmno", false, false), true},
		{"an erased row", row("hello", false, false), row("", false, false), false},
		{"an erased wrapped row", row("abcdefghij", true, false), row("", false, false), false},
		{"a different line", row("hello", false, false), row("world", false, false), false},
		{"a different line on a wrapped row", row("abcdefghij", true, false), row("zzz", true, false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stillReadsAsCaptured(tc.captured, tc.now); got != tc.want {
				t.Fatalf("stillReadsAsCaptured = %v, want %v", got, tc.want)
			}
		})
	}
}
