package sessionruntime

import "testing"

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
	for _, shrink := range []bool{true, false} {
		name := "a pane that grows"
		if shrink {
			name = "a pane that shrinks"
		}
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
			// next: the pane changes height by one row, which moves which row is
			// at the top of the screen without any of those rows departing.
			g := harnessGeometry(80, 24)
			if shrink {
				g.Rows = 23
			} else {
				g.Rows = 25
			}
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
