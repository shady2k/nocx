package sessionruntime

import (
	"fmt"
	"testing"
	"time"
)

// The resize debt's one invariant (nocx-2v80t.3.9): a resize reflows the
// screen against history, so the rows a taller screen refills itself with are
// not departures when they leave a second time — but NOTHING ELSE may be
// swallowed either. `DepartedRowCount` advances by exactly the rows that left
// the screen, per feed, and a row that left is never reported twice.
//
// This is the producer's half of the boundary defect the bead's e2e shows: an
// interval whose departure count is frozen while its screen has moved on hands
// a whole command's rows to the next block. The debt in the ghostty adapter is
// where a frozen count can come from — it is paid BEFORE any growth is
// reported, so a debt larger than what the reflow actually put back on the
// screen eats real departures — and the e2e's panes do move (consecutive
// commands' closing screens measure 27 and 28 rows across one run).

// resizeDeparturesProgram prints one screenful, waits for the test's byte, and
// then prints a second: the resize happens with the first batch on the screen
// and the second batch's departures are what the debt would swallow.
var resizeDeparturesProgram = rawPreamble + `
printf '` + obsLines(0, 40) + `'
readhex 1 >/dev/null
printf '` + obsLines(40, 40) + `'
printf 'RESIZE-DONE\n'
`

// resizeBlankRowProgram is the same command with one blank line in its first
// batch: a blank row is a real departure, and a count that skipped it while
// the runtime counted it is exactly the off-by-one this pair of tests exists
// for (nocx-atpw5.1.47).
var resizeBlankRowProgram = rawPreamble + `
printf '\r\n'
printf '` + obsLines(0, 40) + `'
readhex 1 >/dev/null
printf '` + obsLines(40, 40) + `'
printf 'RESIZE-DONE\n'
`

// TestABlankRowThatLeavesIsStillADeparture keeps the counting rule honest:
// the runtime counts every row it streams, a blank one included, so a reading
// that skips blank texts reports one row fewer than the runtime did and turns
// that into a swallowed departure.
func TestABlankRowThatLeavesIsStillADeparture(t *testing.T) {
	rs := &recordingRowStream{}
	p := startProgramRows(t, resizeBlankRowProgram, harnessGeometry(80, 12), rs)
	p.wait("L000039")
	for _, rows := range []int{20, 10, 22, 18, 24} {
		if _, err := p.s.CommitGeometry(harnessGeometry(80, rows)); err != nil {
			t.Fatalf("resize the pane to %d rows: %v", rows, err)
		}
	}
	p.typed("go")
	p.wait("RESIZE-DONE")
	select {
	case <-p.done:
	case <-time.After(hangLimit):
		t.Fatal("the program's output never ended, so the stream has no last row to read")
	}

	streamed, blank := 0, 0
	for _, e := range rs.snapshot() {
		if e.kind != "rows" {
			continue
		}
		for _, r := range e.rows {
			streamed++
			if streamRowText(r) == "" {
				blank++
			}
		}
	}
	if blank == 0 {
		t.Fatalf("no blank row reached the stream (%d rows), so this test asserts nothing", streamed)
	}
	// #nosec G115 -- a row count, small and positive
	if got, want := p.s.DepartedRowCount(), uint64(streamed); got != want {
		t.Fatalf("the runtime reports %d departed rows and streamed %d, %d of them blank",
			got, streamed, blank)
	}
}

func TestAResizeNeverSwallowsRealDepartures(t *testing.T) {
	rs := &recordingRowStream{}
	p := startProgramRows(t, resizeDeparturesProgram, harnessGeometry(80, 12), rs)
	p.wait("L000039")

	// The pane moves, the way it moves in the e2e: taller, shorter, and then
	// the grow-shrink-grow shape that leaves history reflowed more than once.
	for _, rows := range []int{20, 10, 22, 18, 24} {
		if _, err := p.s.CommitGeometry(harnessGeometry(80, rows)); err != nil {
			t.Fatalf("resize the pane to %d rows: %v", rows, err)
		}
	}

	p.typed("go")
	p.wait("RESIZE-DONE")
	// THE PROGRAM'S OUTPUT HAS ENDED, so the runtime's totals are final. The
	// screen is published under the session lock, but the row stream is read
	// under its own and the counter is read after it: a batch still on its way
	// between the two reads — the runtime increments the count and then hands
	// the rows over in the same locked call — would be counted here and
	// missing there, and the failure would name a swallowed row. So the read
	// waits for the end of the stream itself, which this program reaches by
	// exiting, and not for a moment after the last screen it produced
	// (nocx-atpw5.1.47).
	select {
	case <-p.done:
	case <-time.After(hangLimit):
		t.Fatal("the program's output never ended, so the stream has no last row to read")
	}

	// What the runtime handed the stream, in order: every row, a blank one
	// included. A blank line that left the screen is a departure like any
	// other, and skipping it made this test undercount its own stream by one
	// per blank row — the arithmetic below then blamed a debt for a row the
	// runtime had streamed (nocx-atpw5.1.47).
	counted := map[string]int{}
	streamed, holes := 0, uint64(0)
	for _, e := range rs.snapshot() {
		if e.kind != "rows" {
			continue
		}
		holes += e.lost
		for _, r := range e.rows {
			streamed++
			if text := streamRowText(r); text != "" {
				counted[text]++
			}
		}
	}
	for text, times := range counted {
		if times > 1 {
			t.Fatalf("row %q was streamed %d times: a row that left is reported once", text, times)
		}
	}

	// The rows the first batch pushed off, before any resize touched the pane:
	// forty lines on a twelve-row screen leave the first twenty-eight, and
	// every one of them must be reported exactly once.
	for i := 0; i < 28; i++ {
		marker := fmt.Sprintf("L%06d", i)
		if counted[marker] != 1 {
			t.Fatalf("row %s scrolled off before any resize and the stream reported it %d times, want once", marker, counted[marker])
		}
	}

	// And the second batch's own scrolls, after the dance: forty more lines on
	// a twenty-four row screen leave the first sixteen of them, which no debt
	// may have swallowed (the frozen count that hands a command's rows to the
	// next block). Rows the dance REFLOWED into history are not departures at
	// all — a resize reflows the screen rather than scrolling it — so those
	// are deliberately not asserted.
	for i := 40; i < 56; i++ {
		marker := fmt.Sprintf("L%06d", i)
		if counted[marker] != 1 {
			t.Fatalf("row %s scrolled off after the resize and the stream reported it %d times, want once "+
				"(departed=%d, streamed=%d): a debt that outlives what the reflow put back swallows real departures",
				marker, counted[marker], p.s.DepartedRowCount(), len(counted))
		}
	}

	// A struck feed spends one index with no row on it (rowstream.go): the
	// emulator could not read rows that left, and no count may manufacture
	// them. In this scenario that is a lost scrollback row, so it is named as
	// one instead of being left to look like a debt that swallowed a
	// departure — which is a shape the count and the stream cannot differ by
	// at all, since both are written in one locked call.
	if holes > 0 {
		t.Fatalf("the emulator struck %d feed(s), so rows that left the screen could not be read: departed=%d, streamed=%d",
			holes, p.s.DepartedRowCount(), streamed)
	}

	// The count the runtime reports is the rows it actually streamed: the debt
	// may cancel reflowed rows, never a command's own.
	// #nosec G115 -- a row count, small and positive
	want := uint64(streamed)
	if got := p.s.DepartedRowCount(); got != want {
		t.Fatalf("the runtime reports %d departed rows and streamed %d, with no struck feed to account for the difference", got, streamed)
	}
}
