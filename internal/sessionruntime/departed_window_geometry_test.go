package sessionruntime

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
)

// A GEOMETRY COMMIT MAY NOT RE-STORE THE INTERVAL BEFORE'S CLOSING SCREEN
// (nocx-2v80t.3.9, the last instance of "rows cross the command boundary").
//
// The runtime holds the interval sealed last's screen — the rows it handed to
// the block as its closing screen — as a window, and declines to stream those
// rows a second time when they leave the screen during the command that
// follows. The window was a STRICT PREFIX comparison, cleared at the first row
// that did not match, and a geometry commit lands exactly there: a pane that
// shrinks by a row pushes the screen's top row into history WITHOUT it
// departing (the port's rule), and a pane that grows pulls a row back in ABOVE
// the screen's top. Either way the first row that leaves next is not the
// window's first row, the window died on that comparison, and every row of the
// closing screen it still held was streamed again — stored a second time by
// the next block.
//
// The invariant this test holds shut is the one the bead names: a row the
// interval before stored as its closing screen is never stored again by the
// interval after it.
func TestAClosingScreenIsNotStoredAgainAfterAGeometryCommit(t *testing.T) {
	// The three shapes a commit takes between a boundary and the rows that leave
	// next: a pane one row shorter (the top row is pushed into history, silently),
	// one row taller (a row is pulled back in ABOVE the window's head), and the
	// e2e's own measured geometry — 80x24 spawned, then the wide pane the layout
	// settles on — which re-lays every row out as well as changing the height.
	for _, g := range []emulator.Geometry{
		harnessGeometry(80, 23),
		harnessGeometry(80, 25),
		harnessGeometry(148, 33),
	} {
		name := fmt.Sprintf("a pane that becomes %dx%d", g.Cols, g.Rows)
		t.Run(name, func(t *testing.T) {
			s, rs := streamSession(t, harnessGeometry(80, 24))

			// One command: forty lines on a twenty-four row screen depart
			// seventeen and leave the rest on the screen.
			obsFeed(t, s, 0, 40)
			obsSeal(t, s, obsNonce(1))

			boundary, endRow := boundaryScreen(t, rs)
			if len(boundary) == 0 {
				t.Fatal("the boundary carried no closing screen")
			}

			// The geometry commits BETWEEN the boundary and the rows that leave
			// next: which row is at the top of the screen moves without any of
			// those rows departing, and a width change re-lays every row out.
			if _, err := s.CommitGeometry(g); err != nil {
				t.Fatalf("commit the geometry: %v", err)
			}

			// The next command's output pushes the closing screen off the
			// screen. Those rows are the block before's already.
			obsFeed(t, s, 100, 40)

			for _, e := range rs.snapshot() {
				if e.kind != "rows" || e.from < endRow {
					continue
				}
				for i, row := range e.rows {
					text := streamRowText(row)
					if !boundary[text] {
						continue
					}
					index := e.from + uint64(i) // #nosec G115 -- a slice index, never negative
					t.Fatalf("row %q left the screen at index %d and streamed again: the interval before stored it as its closing screen",
						text, index)
				}
			}
		})
	}
}

// A DESTROYED history is not a departure: the port's own rule. The window must
// not survive the rows it names going away, and it must not suppress a row the
// next command writes that merely LOOKS like one of them — a cleared screen
// must not swallow output.
func TestAWindowDoesNotSurviveItsOwnRowsBeingDestroyed(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	obsFeed(t, s, 0, 40)
	obsSeal(t, s, obsNonce(1))
	boundary, endRow := boundaryScreen(t, rs)
	if len(boundary) == 0 {
		t.Fatal("the boundary carried no closing screen")
	}

	// RIS destroys the scrollback and the screen; the boundary's rows ceased
	// rather than left. The next command's output must stream, none of it
	// suppressed by a window naming rows that no longer exist.
	if err := s.Ingest([]byte("\x1bc")); err != nil {
		t.Fatalf("reset the terminal: %v", err)
	}
	obsFeed(t, s, 200, 40)

	var streamed int
	for _, e := range rs.snapshot() {
		if e.kind != "rows" || e.from < endRow {
			continue
		}
		streamed += len(e.rows)
	}
	if streamed < 17 {
		t.Fatalf("the frame after a reset streamed %d rows, want the seventeen a forty-line command pushes off a twenty-four row screen", streamed)
	}
}

// boundaryScreen is the boundary's closing screen as a set of texts, plus the
// row index the end marker stopped at: the rows the interval before stored.
func boundaryScreen(t *testing.T, rs *recordingRowStream) (map[string]bool, uint64) {
	t.Helper()
	events := rs.snapshot()
	if len(events) == 0 || events[len(events)-1].kind != "end" {
		t.Fatalf("the stream carried %d events, want an end marker last", len(events))
	}
	end := events[len(events)-1]
	out := map[string]bool{}
	for _, row := range end.closing {
		out[streamRowText(row)] = true
	}
	return out, end.endRow
}

// A ROW FROM ABOVE THE WINDOW MUST NOT COST THE WINDOW (nocx-2v80t.3.9).
//
// The e2e's own leak, at the last: a shrink and a growth around a boundary gave
// one row back to the screen that the interval before had already stored, the
// emulator reported that row's departure AGAIN (its ledger's own account of
// what a refill put back is a count, and it was wrong here), and the row
// arrived in front of the boundary's screen. The window died on it — the first
// row that matched nothing was taken for the end of the boundary's screen — and
// the whole closing screen was streamed into the block after, 27 rows at a
// time, measured on the block the store criterion failed on.
//
// The invariant: the boundary's own rows are not stored again, whatever else
// arrives. The stray row is still streamed — it is the emulator's contract that
// it should not have been reported at all, and the schedule below injects it to
// say exactly that — but it no longer takes the closing screen with it.
func TestAStrayRowAboveTheWindowDoesNotCostTheClosingScreen(t *testing.T) {
	screen, err := ghostty.New(harnessGeometry(80, 24))
	if err != nil {
		t.Fatalf("build the real emulator: %v", err)
	}
	t.Cleanup(screen.Close)
	emu := &harnessEmulator{Terminal: screen}
	s := obsSessionOver(t, harnessGeometry(80, 24), emu)
	rs := &recordingRowStream{}
	s.SetRowStream(rs)

	obsFeed(t, s, 0, 40)
	handed := rs.snapshot()[0].rows[0] // a row the interval before already streamed
	obsSeal(t, s, obsNonce(1))
	boundary, endRow := boundaryScreen(t, rs)
	if boundary[streamRowText(handed)] {
		t.Fatal("the row chosen as the stray one is part of the boundary's screen")
	}

	// The row comes back onto the screen above the boundary's screen and leaves
	// again, ahead of it.
	emu.InjectDepartures(handed)
	obsFeed(t, s, 100, 40)

	stray := false
	for _, e := range rs.snapshot() {
		if e.kind != "rows" || e.from < endRow {
			continue
		}
		for i, row := range e.rows {
			text := streamRowText(row)
			if boundary[text] {
				index := e.from + uint64(i) // #nosec G115 -- a slice index, never negative
				t.Fatalf("row %q left the screen at index %d and streamed again: a stray row above the window cost the interval before its closing screen",
					text, index)
			}
			if text == streamRowText(handed) {
				stray = true
			}
		}
	}
	if !stray {
		t.Fatal("the injected row was not streamed: the window swallowed a row it never held")
	}
}

// AN EMPTY BOUNDARY MUST NOT WIPE THE WINDOW (nocx-2v80t.3.9).
//
// A boundary that carries no screen — the read failed, the alternate buffer held
// the pane, or the interval was settled with no sighting at all — used to
// replace the window with nothing, and the rows the interval BEFORE left were
// then stored a second time by the interval after: the e2e's block 4 holding the
// twenty-nine closing rows of the block before it, the first of them the very
// row the window should have held. An empty expectation leaves the window in
// force; the rows it names may still be about to leave.
func TestAnEmptyBoundaryDoesNotWipeTheWindow(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	obsFeed(t, s, 0, 40)
	obsSeal(t, s, obsNonce(1))
	boundary, endRow := boundaryScreen(t, rs)
	if len(boundary) == 0 {
		t.Fatal("the boundary carried no closing screen")
	}
	window := len(s.pendingScreen)
	if window == 0 {
		t.Fatal("the seal installed no window")
	}

	// An interval that authenticates complete and whose fence's sighting never
	// arrives: the next interval's start settles it with NO closing screen.
	s.Completed(s.Incarnation(), obsNonce(2), 0)
	if err := s.SightFence(obsNonce(3), []byte("fence-source")); err != nil {
		t.Fatalf("sight the next fence: %v", err)
	}
	if got := len(s.pendingScreen); got == 0 {
		t.Fatal("an empty boundary wiped the window: the interval before's closing screen is now unguarded")
	}

	// And the guard still works: none of the boundary's rows streams again.
	obsFeed(t, s, 100, 40)
	for _, e := range rs.snapshot() {
		if e.kind != "rows" || e.from < endRow {
			continue
		}
		for i, row := range e.rows {
			text := streamRowText(row)
			if boundary[text] {
				index := e.from + uint64(i) // #nosec G115 -- a slice index, never negative
				t.Fatalf("row %q left the screen at index %d and streamed again after an empty boundary", text, index)
			}
		}
	}
}
