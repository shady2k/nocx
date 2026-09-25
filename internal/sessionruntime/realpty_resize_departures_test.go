package sessionruntime

import (
	"fmt"
	"testing"
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

	// What the runtime handed the stream, in order.
	counted := map[string]int{}
	for _, text := range streamedTexts(rs) {
		if text == "" {
			continue
		}
		counted[text]++
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

	// The count the runtime reports is the rows it actually streamed: the debt
	// may cancel reflowed rows, never a command's own.
	streamed := 0
	for _, times := range counted {
		streamed += times
	}
	// #nosec G115 -- a row count, small and positive
	want := uint64(streamed)
	if got := p.s.DepartedRowCount(); got != want {
		t.Fatalf("the runtime reports %d departed rows and streamed %d: the difference is rows the debt swallowed", got, streamed)
	}
}
