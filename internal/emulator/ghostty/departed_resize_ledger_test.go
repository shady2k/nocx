package ghostty

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// A ROW LEAVES THE SCREEN ONCE, WHATEVER THE PANE'S GEOMETRY DOES
// (nocx-2v80t.3.9).
//
// The e2e re-measures the pane while a transcript runs, so geometry commits land
// between commands: a shrink reflows the screen's top rows into history, a
// growth pulls history back onto the taller screen, and the row that came back
// leaves again. The port's contract is one report per row — "a row is reported
// exactly once and by one reader" — and the ledger that decides what a refill
// owes described PUSHES only: a row that had left normally, been handed over and
// was then pulled back from plain history was charged against a push block and
// marked "fresh" (never handed), so its departure was reported a second time.
//
// Measured on the pattern below, before the ledger carried the runs between
// pushes: transcript-0003-069 reported twice. The extra arrival lands in front
// of the next boundary's window, which is how it reached the transcript as a
// foreign row in the block after it.
func TestARepeatedResizeCycleDoesNotReReportARow(t *testing.T) {
	term, err := New(emulator.Geometry{Cols: 80, Rows: 24, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("build the terminal: %v", err)
	}
	t.Cleanup(func() { term.Close() })

	reported := map[string]int{}
	feed := func(command int) {
		var out []byte
		for i := 1; i <= 100; i++ {
			out = append(out, []byte(fmt.Sprintf("transcript-%04d-%03d\r\n", command, i))...)
		}
		if _, err := term.Ingest(out); err != nil {
			t.Fatalf("feed command %d: %v", command, err)
		}
	}
	drain := func() {
		rows, err := term.DepartedRows()
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		for _, row := range rows {
			reported[departedText(row)]++
		}
	}
	resize := func(cols, rows int) {
		if _, err := term.Resize(emulator.Geometry{Cols: cols, Rows: rows, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
			t.Fatalf("resize to %dx%d: %v", cols, rows, err)
		}
	}

	// The pattern the e2e produces: the pane takes its measured width and then
	// breathes by a row as the layout re-measures, with output between the
	// commits.
	feed(1)
	drain()
	resize(148, 33)
	resize(148, 32)
	resize(148, 33)
	feed(2)
	drain()
	resize(148, 32)
	feed(3)
	drain()
	resize(148, 31)
	resize(148, 33)
	feed(4)
	drain()
	resize(148, 30)
	resize(148, 34)
	feed(5)
	drain()

	duplicates := 0
	for text, times := range reported {
		if times > 1 {
			duplicates++
			t.Errorf("row %q was reported %d times: a row leaves the screen once, whatever the pane's geometry does", text, times)
		}
	}
	if duplicates == 0 {
		return
	}
	t.Fatalf("%d rows were reported more than once across %d rows handed over", duplicates, len(reported))
}
