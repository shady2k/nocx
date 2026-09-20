package ghostty

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The tests below drive the PORT — emulator.Terminal — like every other test
// in this package: what is asserted is what a consumer of the interface can
// see. Their helpers are named for this file so that a rename in another
// test file cannot break them.
//
// WHY A REPORT OF DEPARTED ROWS IS WORTH FOUR TESTS. A capture record needs
// "the rows that left the screen during an interval, in order, readable as
// logical lines" (nocx-2v80t.2). The failure modes are quiet: rows reported
// twice, rows skipped when the feed is chunked differently, soft-wrap lost to
// a width re-measurement, alternate-screen churn reported as history. Each
// test below is one of those failures, named.

// departedTerm is a terminal for the departed-row tests.
func departedTerm(t *testing.T, cols, rows int) emulator.Terminal {
	t.Helper()
	term, err := New(emulator.Geometry{Cols: cols, Rows: rows, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("new terminal: %v", err)
	}
	t.Cleanup(term.Close)
	return term
}

// departedFeed feeds s as the program's output.
func departedFeed(t *testing.T, term emulator.Terminal, s string) {
	t.Helper()
	if _, err := term.Ingest([]byte(s)); err != nil {
		t.Fatalf("ingest %d bytes: %v", len(s), err)
	}
}

// departedDrain drains the departed-row report, refusing an error: a caller
// that cannot read the report has nothing, and every test here wants to see
// what the report says rather than survive its absence.
func departedDrain(t *testing.T, term emulator.Terminal) []emulator.Row {
	t.Helper()
	rows, err := term.DepartedRows()
	if err != nil {
		t.Fatalf("departed rows: %v", err)
	}
	return rows
}

// departedText renders a row the way a serialiser would: the graphemes of the
// cells that hold text, in order.
func departedText(row emulator.Row) string {
	var sb strings.Builder
	for _, cell := range row.Cells {
		if cell.HasText {
			sb.WriteString(cell.Grapheme)
		}
	}
	return sb.String()
}

// departedRowWant is one expected row of a report: what a consumer reads off
// it — its text and the soft-wrap pair — and nothing about the code.
type departedRowWant struct {
	text         string
	wrap         bool
	continuation bool
}

func departedAssertEqual(t *testing.T, got []emulator.Row, want []departedRowWant) {
	t.Helper()
	if len(got) != len(want) {
		var texts []string
		for _, row := range got {
			texts = append(texts, departedText(row))
		}
		t.Fatalf("departed rows: got %d, want %d: %q", len(got), len(want), texts)
	}
	for i := range want {
		if got[i].Wrap != want[i].wrap || got[i].Continuation != want[i].continuation {
			t.Fatalf("departed row %d: wrap=%v continuation=%v, want wrap=%v continuation=%v (text %q)",
				i, got[i].Wrap, got[i].Continuation, want[i].wrap, want[i].continuation, departedText(got[i]))
		}
		if text := departedText(got[i]); text != want[i].text {
			t.Fatalf("departed row %d: text %q, want %q", i, text, want[i].text)
		}
		if len(got[i].Cells) == 0 {
			t.Fatalf("departed row %d: carries no cells", i)
		}
	}
}

// numbered feeds n lines "L00".."L<n-1>", each ended by a hard newline.
func numbered(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "L%02d\r\n", i)
	}
	return sb.String()
}

// On a 5-row terminal, n numbered lines push the first n-4 into scrollback:
// the first four fill the screen and every newline from the fifth line on
// scrolls one row off the top.
func numberedWant(n int) []departedRowWant {
	var want []departedRowWant
	for i := range n - 4 {
		want = append(want, departedRowWant{text: fmt.Sprintf("L%02d", i)})
	}
	return want
}

// Criterion 1: every row that left the screen is reported exactly once, in
// order, with its cells — and the report is drained by reading it.
func TestDepartedRowsReportEveryRowOnceInOrder(t *testing.T) {
	term := departedTerm(t, 10, 5)
	departedFeed(t, term, numbered(12))

	departedAssertEqual(t, departedDrain(t, term), numberedWant(12))

	// A report is read once: the second drain of the same interval is empty,
	// so a consumer that polls cannot mistake what it already read for a
	// fresh departure.
	if rows := departedDrain(t, term); len(rows) != 0 {
		t.Fatalf("second drain: got %d rows, want none", len(rows))
	}
}

// Criterion 2: a soft-wrapped line reports its continuation rows AS
// continuations — the flags the terminal carried across the wrap, not a width
// measurement — and a hard newline reports neither flag.
func TestDepartedRowsCarrySoftWrapAndHardNewline(t *testing.T) {
	term := departedTerm(t, 10, 3)
	// One 12-column line on a 10-column grid: "AAAAABBBBB" fills a row, "CC"
	// continues on the next. Then a hard-newline line, then filler to push
	// all four rows off the top.
	departedFeed(t, term, "AAAAABBBBBCC\r\nXYZ\r\nF\r\nF\r\nF\r\n")

	got := departedDrain(t, term)
	departedAssertEqual(t, got, []departedRowWant{
		{text: "AAAAABBBBB", wrap: true},
		{text: "CC", continuation: true},
		{text: "XYZ"},
		{text: "F"},
	})

	// The pair joins back into the line the program printed: that is what a
	// wrap flag is FOR.
	if joined := departedText(got[0]) + departedText(got[1]); joined != "AAAAABBBBBCC" {
		t.Fatalf("wrapped pair joined to %q, want %q", joined, "AAAAABBBBBCC")
	}
}

// Criterion 3: the alternate screen has no history, so rows leaving it report
// nothing — and switching back does not report the primary's restored history
// either.
func TestDepartedRowsReportNothingOnTheAlternateScreen(t *testing.T) {
	term := departedTerm(t, 10, 5)
	departedFeed(t, term, numbered(8))
	departedAssertEqual(t, departedDrain(t, term), numberedWant(8))

	departedFeed(t, term, "\x1b[?1049h")
	departedFeed(t, term, numbered(20))
	if rows := departedDrain(t, term); len(rows) != 0 {
		t.Fatalf("alternate-screen churn reported %d rows, want none", len(rows))
	}

	// Coming back restores the primary exactly as it was: its rows are where
	// they were, not a fresh departure.
	departedFeed(t, term, "\x1b[?1049l")
	if rows := departedDrain(t, term); len(rows) != 0 {
		t.Fatalf("restoring the primary reported %d rows, want none", len(rows))
	}

	// And the primary continues from where it left off: the next departures
	// are the rows after the ones already reported, in order.
	departedFeed(t, term, "L08\r\nL09\r\n")
	departedAssertEqual(t, departedDrain(t, term), []departedRowWant{
		{text: "L04"},
		{text: "L05"},
	})
}

// The one boundary the library imposes: it reports scrollback per ACTIVE
// buffer, so a write that scrolls the primary and then enters the alternate
// screen cannot name those rows yet. They are reported at the next
// measurement of the primary — the switch back — still in order, and still
// exactly once.
func TestDepartedRowsLeftBeforeAnAlternateSwitchReportAtRestore(t *testing.T) {
	term := departedTerm(t, 10, 5)
	departedFeed(t, term, numbered(6)+"\x1b[?1049h")
	if rows := departedDrain(t, term); len(rows) != 0 {
		t.Fatalf("write that ended on the alternate screen reported %d rows", len(rows))
	}

	departedFeed(t, term, numbered(10))
	if rows := departedDrain(t, term); len(rows) != 0 {
		t.Fatalf("alternate-screen churn reported %d rows, want none", len(rows))
	}

	departedFeed(t, term, "\x1b[?1049l")
	departedAssertEqual(t, departedDrain(t, term), numberedWant(6))
}

// Criterion 4: the report is a function of the BYTES, not of how they were
// chunked. The same stream fed at every split position reports the same rows
// as one feed — the failure that would otherwise be quiet, because every
// happy-path chunking looks correct.
func TestDepartedRowsAreTheSameRowsAtEverySplit(t *testing.T) {
	// Numbered lines, a soft-wrapped line, an alternate-screen excursion, and
	// more numbered lines: every detection boundary the port has, in one
	// stream.
	stream := numbered(8) +
		"AAAAABBBBBCC\r\n" +
		"\x1b[?1049h" +
		numbered(7) +
		"\x1b[?1049l" +
		"L08\r\nL09\r\nL10\r\n"

	// The one-feed answer, derived by hand: the eight numbered lines, the two
	// rows the wrapped line pushed off (its filled first row among them), and
	// — after the excursion — the row the wrap left, the two lines printed
	// before it, and the wrapped line's own first row, which leaves carrying
	// its wrap flag.
	golden := append(numberedWant(8),
		departedRowWant{text: "L04"},
		departedRowWant{text: "L05"},
		departedRowWant{text: "L06"},
		departedRowWant{text: "L07"},
		departedRowWant{text: "AAAAABBBBB", wrap: true},
	)

	control := departedTerm(t, 10, 5)
	departedFeed(t, control, stream)
	departedAssertEqual(t, departedDrain(t, control), golden)

	for split := range len(stream) {
		term := departedTerm(t, 10, 5)
		departedFeed(t, term, stream[:split])
		before := departedDrain(t, term)
		departedFeed(t, term, stream[split:])
		after := departedDrain(t, term)
		departedAssertEqual(t, append(before, after...), golden)
	}
}

// A closed terminal has no report, exactly as it has no rows: the port's one
// answer after Close is ErrClosed, and a report nobody can read is not an
// answer.
func TestDepartedRowsRefuseAClosedTerminal(t *testing.T) {
	term, err := New(emulator.Geometry{Cols: 10, Rows: 5, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatal(err)
	}
	departedFeed(t, term, numbered(8))
	term.Close()
	if _, err := term.DepartedRows(); !errors.Is(err, emulator.ErrClosed) {
		t.Fatalf("departed rows after close: %v, want ErrClosed", err)
	}
}
