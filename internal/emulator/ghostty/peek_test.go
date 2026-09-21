package ghostty

// The peek (nocx-2v80t.2.4): a copy of the departure report that empties
// nothing. DepartedRows drains on read deliberately — a row is reported
// exactly once and by one reader — and the settle-time drain is what makes a
// settled capture cover its whole interval. The unfinished record needs the
// same rows BEFORE the settle, and taking them must not spend them.

import (
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// TestAPeekReadsTheDepartureReportWithoutSpendingIt walks the peek's whole
// contract: a peek reads the same rows a drain would, reading it twice hands
// two equal copies (each caller's own), and the drain afterwards still
// carries every row exactly once — the peek emptied nothing.
func TestAPeekReadsTheDepartureReportWithoutSpendingIt(t *testing.T) {
	term := departedTerm(t, 80, 24)
	// 30 lines on a 24-row screen: 30-24+1 = 7 rows depart.
	departedFeed(t, term, numbered(30))

	first, err := term.PeekDepartedRows()
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if len(first) != 7 {
		t.Fatalf("the first peek read %d rows, want 7", len(first))
	}

	second, err := term.PeekDepartedRows()
	if err != nil {
		t.Fatalf("second peek: %v", err)
	}
	if len(second) != len(first) {
		t.Fatalf("the second peek read %d rows, want the same %d the first read", len(second), len(first))
	}
	departedAssertEqual(t, second, []departedRowWant{
		{text: "L00"},
		{text: "L01"},
		{text: "L02"},
		{text: "L03"},
		{text: "L04"},
		{text: "L05"},
		{text: "L06"},
	})

	// THE LOAD-BEARING HALF: the drain still owns the whole report. If the
	// peek had consumed rows, the settle-time drain would silently cover
	// less than the interval, which is the guarantee task 2.3's tests pin.
	drained := departedDrain(t, term)
	if len(drained) != 7 {
		t.Fatalf("the drain after a peek carried %d rows, want all 7", len(drained))
	}
	departedAssertEqual(t, drained, []departedRowWant{
		{text: "L00"},
		{text: "L01"},
		{text: "L02"},
		{text: "L03"},
		{text: "L04"},
		{text: "L05"},
		{text: "L06"},
	})

	// And the report is still a drain: the next read starts fresh.
	if again := departedDrain(t, term); len(again) != 0 {
		t.Fatalf("a second drain carried %d rows, want 0", len(again))
	}
}

// TestAPeekOverAClosedTerminalIsClosed is the paired failure: a closed
// terminal has no report, exactly as DepartedRows answers.
func TestAPeekOverAClosedTerminalIsClosed(t *testing.T) {
	term := departedTerm(t, 80, 24)
	term.Close()
	if _, err := term.PeekDepartedRows(); !errors.Is(err, emulator.ErrClosed) {
		t.Fatalf("peek over a closed terminal: %v, want %v", err, emulator.ErrClosed)
	}
}
