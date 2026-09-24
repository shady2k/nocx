package ghostty

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// zzUniqueStream advances a shared counter and returns n long logical lines
// built from it, each hard-newline terminated: a strictly increasing run of
// 4-digit numbers with no separators, so that ANY contiguous window of it --
// in particular each physical row a wrap produces, whatever the column width
// -- is unique across the whole test. A row reported twice is therefore a
// real duplicate, never two different lines that merely share filler text.
func zzUniqueStream(counter *int, lineLen, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		line := ""
		for len(line) < lineLen {
			line += fmt.Sprintf("%04d", *counter)
			*counter++
		}
		out += line[:lineLen] + "\r\n"
	}
	return out
}

// TESTED: a WIDTH-only resize (rows unchanged) rewraps existing scrollback --
// the same characters wrap into a different number of physical rows -- and
// the adapter's refill/push accounting is keyed ONLY on the ROW count
// (Resize's `grew := g.Rows - beforeRows`, terminal.go), so a pure width
// change fires neither its shrink-push nor its grow-refill branch at all.
// rebaselineLocked re-baselines the new depth and carries owed/pushed
// forward UNCHANGED; this measures whether that is honest once a LATER
// row-count change puts the carried-forward ledger to work
// (nocx-2v80t.3.10 asks this to be measured, not assumed).
func TestARewrapThatMovesHistoryDepthReportsEveryRowOnce(t *testing.T) {
	port, err := New(emulator.Geometry{Cols: 20, Rows: 5, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	term, ok := port.(*terminal)
	if !ok {
		t.Fatalf("the port is %T, not the ghostty terminal", port)
	}
	defer term.Close()

	counter := 0
	seen := map[string]int{}
	drain := func(label string) {
		t.Helper()
		rows, err := term.DepartedRows()
		if err != nil {
			t.Fatalf("drain %s: %v", label, err)
		}
		for _, r := range rows {
			seen[resizeRowText(r)]++
		}
		t.Logf("%s: %d rows departed, sb[0]=%+v", label, len(rows), term.sb[0])
	}

	// A: several long lines at 20 cols, wrapping into more physical rows than
	// the 5-row screen holds, so some genuinely depart.
	if _, err := term.Ingest([]byte(zzUniqueStream(&counter, 60, 6))); err != nil {
		t.Fatalf("feed A: %v", err)
	}
	drain("A")

	// Widen ONLY: rewraps the scrollback at a different column count without
	// the row-count-keyed grow/shrink logic ever firing.
	if _, err := term.Resize(emulator.Geometry{Cols: 60, Rows: 5, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
		t.Fatalf("widen: %v", err)
	}
	t.Logf("after widen sb[0]=%+v", term.sb[0])

	// B: more long lines at the new width, forcing further departures.
	if _, err := term.Ingest([]byte(zzUniqueStream(&counter, 120, 6))); err != nil {
		t.Fatalf("feed B: %v", err)
	}
	drain("B")

	// Grow the ROW count too: this is what puts whatever the rewrap left in
	// the ledger to actual use, via a refill.
	if _, err := term.Resize(emulator.Geometry{Cols: 60, Rows: 20, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
		t.Fatalf("grow rows: %v", err)
	}
	t.Logf("after growing rows sb[0]=%+v", term.sb[0])

	// C: enough to push whatever the growth refilled back off again.
	if _, err := term.Ingest([]byte(zzUniqueStream(&counter, 120, 20))); err != nil {
		t.Fatalf("feed C: %v", err)
	}
	drain("C")

	for text, n := range seen {
		if n > 1 {
			t.Errorf("row %q was reported %d times across a rewrap that moved the history depth", text, n)
		}
	}
}
