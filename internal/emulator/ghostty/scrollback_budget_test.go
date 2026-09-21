package ghostty

// The scrollback budget (nocx-2v80t.2.3): New pins the byte budget to the
// content policy's per-command output cap (outputcap.PerCommandBytes), so
// the knob a capture depends on is the number history is willing to keep
// rather than the library's implicit default.
//
// MEASURED at pin 1f225ebb5894: the library prunes at PAGE granularity —
// one page ≈ ~1,130 retained rows at 80 columns (≈ ~2,285 at 40, ≈ ~8,525
// at 10) — and every byte budget from the old 10,000-byte default through
// 1 MiB retains exactly that one page. What these tests pin is therefore
// the boundary the shipped budget actually has, so a re-pin or a re-decided
// number that moves it fails HERE first: three screens of numbered lines
// sit far inside the page and read back whole, and the interval is flagged
// when the page turns and rows become unreadable.

import (
	"fmt"
	"testing"
)

// Criterion 1's adapter leg: three screens of numbered lines on the 80x24
// grid push 48 rows into history and the report carries every one of them
// whole, in order, with no hole.
func TestThreeScreensOfNumberedLinesDepartWithoutAHole(t *testing.T) {
	term := departedTerm(t, 80, 24)
	departedFeed(t, term, numbered(72))
	rows := departedDrain(t, term)
	// The last line's own newline scrolls the screen: 49 rows leave, not 48.
	if len(rows) != 72-24+1 {
		t.Fatalf("72 lines on a 24-row screen departed %d rows, want 49", len(rows))
	}
	for i, row := range rows {
		if got, want := departedText(row), fmt.Sprintf("L%02d", i); got != want {
			t.Fatalf("departed row %d reads %q, want %q", i, got, want)
		}
	}
}

// Criterion 3's adapter leg: the flag is REAL — fed past the page boundary,
// the report answers with the hole instead of an empty success, and the
// boundary sits where the shipped budget's page puts it. Feeding one line
// per write, the first flag lands near line 1,155 at this pin; a budget or
// a library pin that moves the boundary by a page multiple shows up here as
// a moved flag.
func TestTheDepartureFlagFiresWhenThePageTurns(t *testing.T) {
	term := departedTerm(t, 80, 24)

	flagAt := 0
	for i := 0; i < 2000; i++ {
		departedFeed(t, term, fmt.Sprintf("%04d\r\n", i))
		if _, err := term.DepartedRows(); err != nil {
			flagAt = i
			break
		}
	}
	if flagAt == 0 {
		t.Fatalf("no interval was flagged within 2000 lines: the page turned and the report never said so")
	}
	if flagAt < 1000 {
		t.Fatalf("first flag at line %d, far below the measured page boundary (~1,155): the budget regressed below the shipped one", flagAt)
	}
}
