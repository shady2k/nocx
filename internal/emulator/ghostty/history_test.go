package ghostty

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty/ghosttytest"
)

// The tests below drive the PORT — emulator.Terminal — like every other test
// in this package: what is asserted is what a consumer of the interface can
// see. Their helpers are named for this file so that a rename in another test
// file cannot break them.
//
// DepartedRows (nocx-2v80t.2.1) is a producer: it hands over the rows that
// left since the previous call, once. Scrolling up needs the other reading —
// history BY POSITION — and these tests are the range read's contract
// (nocx-zg3k3.10.2): exactly the rows asked for, in order, with their cells;
// soft-wrap flags a reader can rejoin; an overreaching range answered with
// what exists and the total it stopped at; the alternate screen answered with
// its own (empty) history; and a row of N columns costing one grid-reference
// resolution, not N — the criterion that cannot be established by reading the
// code, which is why the counter exists.

// histTerm is a terminal for the history-range tests.
func histTerm(t *testing.T, cols, rows int) emulator.Terminal {
	t.Helper()
	term, err := New(emulator.Geometry{Cols: cols, Rows: rows, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("new terminal: %v", err)
	}
	t.Cleanup(term.Close)
	return term
}

// histFeed feeds s as the program's output.
func histFeed(t *testing.T, term emulator.Terminal, s string) {
	t.Helper()
	if _, err := term.Ingest([]byte(s)); err != nil {
		t.Fatalf("ingest %d bytes: %v", len(s), err)
	}
}

// histPage reads one range, refusing an error: every test here wants to see
// what a successful read answers, and the two that do not read the error
// paths directly.
func histPage(t *testing.T, term emulator.Terminal, start, count int) emulator.HistoryPage {
	t.Helper()
	page, err := term.HistoryRows(start, count)
	if err != nil {
		t.Fatalf("history rows %d..%d: %v", start, start+count, err)
	}
	return page
}

// histText renders a row the way a serialiser would: the graphemes of the
// cells that hold text, in order.
func histText(row emulator.Row) string {
	var sb strings.Builder
	for _, cell := range row.Cells {
		if cell.HasText {
			sb.WriteString(cell.Grapheme)
		}
	}
	return sb.String()
}

// histNumbered feeds n lines "L0000".."L<n-1>", each ended by a hard newline.
// Four digits so the concurrency test can parse every line number back.
func histNumbered(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "L%04d\r\n", i)
	}
	return sb.String()
}

// Criterion 1: printing more than one screen of numbered lines and then
// reading history by range returns exactly those rows, in order, with their
// cells. Row 0 of history is the OLDEST retained row — the same origin
// DepartedRows reads the newest history from — so the numbers ascend across
// the range.
func TestHistoryRowsReturnThePrintedRowsInRange(t *testing.T) {
	term := histTerm(t, 80, 24)
	histFeed(t, term, histNumbered(100))

	// 23 lines fill the screen; every newline from the 24th line on scrolls
	// one row off the top, so history holds lines 0..76.
	page := histPage(t, term, 0, 200)
	if page.Total != 77 {
		t.Fatalf("total history rows: %d, want 77", page.Total)
	}
	if page.Start != 0 {
		t.Fatalf("page start: %d, want 0", page.Start)
	}
	if len(page.Rows) != 77 {
		t.Fatalf("rows returned: %d, want 77", len(page.Rows))
	}
	for i, row := range page.Rows {
		if row.Wrap || row.Continuation {
			t.Fatalf("history row %d: hard newline carried wrap flags (wrap=%v continuation=%v)",
				i, row.Wrap, row.Continuation)
		}
		if got, want := histText(row), fmt.Sprintf("L%04d", i); got != want {
			t.Fatalf("history row %d: text %q, want %q", i, got, want)
		}
	}
}

// Criterion 3: a range that reaches past the retained history is answered
// with what exists and an explicit statement of where it stopped — the page
// carries Total, so a short range and a short history can never be confused.
// Not an error: an overreaching read is a normal question about a finite
// buffer, and the answer is complete.
func TestHistoryRowsTruncateAtRetentionWithTheStopStated(t *testing.T) {
	term := histTerm(t, 80, 24)
	histFeed(t, term, histNumbered(100))

	page := histPage(t, term, 70, 100)
	if page.Start != 70 {
		t.Fatalf("page start: %d, want 70", page.Start)
	}
	if page.Total != 77 {
		t.Fatalf("total history rows: %d, want 77", page.Total)
	}
	if len(page.Rows) != 7 {
		t.Fatalf("rows returned: %d, want the 7 that exist (70..76)", len(page.Rows))
	}
	for i, row := range page.Rows {
		if got, want := histText(row), fmt.Sprintf("L%04d", 70+i); got != want {
			t.Fatalf("history row %d: text %q, want %q", i, got, want)
		}
	}
}

// The failure-path companion: a range that STARTS past the end is empty, and
// still states the total — an empty page with Total is "there is nothing
// there", not a failure of the terminal.
func TestHistoryRowsStartPastTheEndAnswersEmptyWithTheTotal(t *testing.T) {
	term := histTerm(t, 80, 24)
	histFeed(t, term, histNumbered(100))

	for _, start := range []int{77, 100, 10_000} {
		page := histPage(t, term, start, 5)
		if len(page.Rows) != 0 {
			t.Fatalf("history rows from %d: got %d rows, want none", start, len(page.Rows))
		}
		if page.Total != 77 {
			t.Fatalf("history rows from %d: total %d, want 77", start, page.Total)
		}
	}
}

// A range of zero rows is a well-formed question with a well-formed answer:
// the empty page, with the total alongside. It is not a refusal — a caller
// computing its window arithmetically must not special-case zero.
func TestHistoryRowsZeroCountAnswersAnEmptyPage(t *testing.T) {
	term := histTerm(t, 80, 24)
	histFeed(t, term, histNumbered(100))

	for _, start := range []int{0, 40} {
		page := histPage(t, term, start, 0)
		if len(page.Rows) != 0 {
			t.Fatalf("zero-row range from %d: got %d rows, want none", start, len(page.Rows))
		}
		if page.Total != 77 {
			t.Fatalf("zero-row range from %d: total %d, want 77", start, page.Total)
		}
	}
}

// A negative start or count is malformed input, not a position: refused with
// [emulator.ErrOutOfRange], the same sentinel every other out-of-bounds read
// on this port reports. The paired ordinary case — a well-formed range on an
// ordinary history — succeeds in every other test of this file.
func TestHistoryRowsRefuseANegativeRange(t *testing.T) {
	term := histTerm(t, 80, 24)
	histFeed(t, term, histNumbered(100))

	for _, tc := range []struct{ start, count int }{{-1, 5}, {0, -1}, {-5, -5}} {
		_, err := term.HistoryRows(tc.start, tc.count)
		if !errors.Is(err, emulator.ErrOutOfRange) {
			t.Fatalf("history rows %d, %d: err %v, want ErrOutOfRange", tc.start, tc.count, err)
		}
	}
}

// An empty history answers the same way an empty range does: an empty page
// naming Total 0. The paired ordinary case — a range over a non-empty
// history — is the first test of this file.
func TestHistoryRowsOnAnEmptyHistory(t *testing.T) {
	term := histTerm(t, 80, 24)

	page := histPage(t, term, 0, 10)
	if len(page.Rows) != 0 {
		t.Fatalf("empty history: got %d rows, want none", len(page.Rows))
	}
	if page.Total != 0 {
		t.Fatalf("empty history: total %d, want 0", page.Total)
	}
}

// Criterion 2: a soft-wrapped long line reads back with its continuation
// flags intact — Wrap true on every physical row that has a successor in the
// same logical line, Continuation true on the row that completes it, neither
// on a row a hard newline ended — so a reader can rejoin the physical lines
// of one logical line.
func TestHistoryRowsCarrySoftWrapContinuations(t *testing.T) {
	term := histTerm(t, 10, 5)
	// 25 characters at 10 columns: three physical rows, 10 + 10 + 5.
	histFeed(t, term, "0123456789012345678901234\r\n")
	histFeed(t, term, histNumbered(8))

	page := histPage(t, term, 0, 50)
	if page.Total != 7 {
		t.Fatalf("total history rows: %d, want 7 (three wrapped + four numbered)", page.Total)
	}
	want := []struct {
		text         string
		wrap         bool
		continuation bool
	}{
		{"0123456789", true, false},
		{"0123456789", true, true},
		{"01234", false, true},
		{"L0000", false, false},
		{"L0001", false, false},
		{"L0002", false, false},
		{"L0003", false, false},
	}
	for i, w := range want {
		row := page.Rows[i]
		if row.Wrap != w.wrap || row.Continuation != w.continuation {
			t.Fatalf("history row %d: wrap=%v continuation=%v, want wrap=%v continuation=%v",
				i, row.Wrap, row.Continuation, w.wrap, w.continuation)
		}
		if got := histText(row); got != w.text {
			t.Fatalf("history row %d: text %q, want %q", i, got, w.text)
		}
	}
	// And the flags do their job: the physical lines rejoin into the logical
	// line the program printed.
	var rejoined strings.Builder
	for _, row := range page.Rows[:3] {
		rejoined.WriteString(histText(row))
	}
	if got := rejoined.String(); got != "0123456789012345678901234" {
		t.Fatalf("rejoined logical line: %q, want the 25 characters the program printed", got)
	}
}

// Criterion 4: while the alternate screen is active, a history read does not
// return the primary screen's rows — the history space is the ACTIVE buffer's,
// and the alternate screen's own history is empty. Switching back restores
// the primary's rows exactly where they were.
func TestHistoryRowsDoNotReturnThePrimaryOnTheAlternateScreen(t *testing.T) {
	term := histTerm(t, 10, 5)
	histFeed(t, term, histNumbered(30))
	if page := histPage(t, term, 0, 100); page.Total != 26 {
		t.Fatalf("primary history rows: %d, want 26", page.Total)
	}

	histFeed(t, term, "\x1b[?1049h")
	if screen, err := term.Screen(); err != nil || screen != emulator.ScreenAlternate {
		t.Fatalf("after 1049h: screen %v err %v, want ScreenAlternate", screen, err)
	}
	// Churn on the alternate does not become history, and the primary's 26
	// rows are not readable around it.
	histFeed(t, term, histNumbered(3))
	page := histPage(t, term, 0, 100)
	if len(page.Rows) != 0 || page.Total != 0 {
		t.Fatalf("alternate-screen history: %d rows, total %d; want the empty answer, not the primary's rows",
			len(page.Rows), page.Total)
	}

	histFeed(t, term, "\x1b[?1049l")
	if screen, err := term.Screen(); err != nil || screen != emulator.ScreenPrimary {
		t.Fatalf("after 1049l: screen %v err %v, want ScreenPrimary", screen, err)
	}
	page = histPage(t, term, 0, 100)
	if page.Total != 26 || len(page.Rows) != 26 {
		t.Fatalf("restored primary history: %d rows, total %d; want the primary's 26 rows back",
			len(page.Rows), page.Total)
	}
	for i, row := range page.Rows {
		if got, want := histText(row), fmt.Sprintf("L%04d", i); got != want {
			t.Fatalf("restored history row %d: text %q, want %q", i, got, want)
		}
	}
}

// The failure-path companion: a closed terminal has no history, exactly as it
// has no rows. The paired ordinary case is every other test of this file.
func TestHistoryRowsRefuseAClosedTerminal(t *testing.T) {
	term := histTerm(t, 10, 5)
	histFeed(t, term, histNumbered(8))
	term.Close()
	if _, err := term.HistoryRows(0, 4); !errors.Is(err, emulator.ErrClosed) {
		t.Fatalf("history rows after close: %v, want ErrClosed", err)
	}
}

// Criterion 1, the cells half: history rows carry their cells — a styled run
// keeps its bold and its palette colour, a wide cluster keeps its width and
// its spacer, an unstyled cell reads as the default style — and the row read
// by position equals the row the active-area read saw before it scrolled.
func TestHistoryRowsCarryCellsAndStyles(t *testing.T) {
	term := histTerm(t, 40, 10)
	histFeed(t, term, "\x1b[1;31mBOLDRED\x1b[0m plain \xe6\x97\xa5\r\n")
	active, err := term.Row(0)
	if err != nil {
		t.Fatalf("active row 0: %v", err)
	}

	histFeed(t, term, histNumbered(12))
	page := histPage(t, term, 0, 50)
	if page.Total != 4 {
		t.Fatalf("total history rows: %d, want 4", page.Total)
	}
	row := page.Rows[0]
	if got, want := histText(row), "BOLDRED plain 日"; got != want {
		t.Fatalf("styled history row: text %q, want %q", got, want)
	}
	// The styled run: bold, palette colour 1.
	head := row.Cells[0]
	if !head.HasText || head.Grapheme != "B" {
		t.Fatalf("first cell: %+v, want the B of BOLDRED", head)
	}
	if head.Style.Attributes&emulator.AttrBold == 0 {
		t.Fatalf("first cell: bold lost, style %+v", head.Style)
	}
	if head.Style.Foreground.Kind != emulator.ColorPalette || head.Style.Foreground.Palette != 1 {
		t.Fatalf("first cell: foreground %+v, want palette 1", head.Style.Foreground)
	}
	// The wide cluster and its spacer.
	cjk := -1
	for i, cell := range row.Cells {
		if cell.Grapheme == "日" {
			cjk = i
			break
		}
	}
	if cjk < 0 {
		t.Fatalf("no CJK cell in history row: text %q", histText(row))
	}
	if row.Cells[cjk].Width != emulator.WidthWide {
		t.Fatalf("CJK cell width %v, want WidthWide", row.Cells[cjk].Width)
	}
	if row.Cells[cjk+1].Width != emulator.WidthSpacerTail {
		t.Fatalf("cell after CJK width %v, want WidthSpacerTail", row.Cells[cjk+1].Width)
	}
	// The unstyled tail reads as the default style — the value a full style
	// read produces for a cell that never had one.
	tail := row.Cells[30]
	if tail.Style != (emulator.Style{}) {
		t.Fatalf("unstyled cell style %+v, want the zero style", tail.Style)
	}
	// And the whole row is the row the active-area read saw.
	if len(row.Cells) != len(active.Cells) {
		t.Fatalf("history row has %d cells, active read saw %d", len(row.Cells), len(active.Cells))
	}
	for i := range row.Cells {
		if row.Cells[i] != active.Cells[i] {
			t.Fatalf("history cell %d %+v differs from the active read's %+v", i, row.Cells[i], active.Cells[i])
		}
	}
}

// Criterion 5: reading a row of N columns costs one grid-reference
// resolution, not N — measured, not asserted from the code. The bridge counts
// every grid reference it resolves; a range over R rows must spend exactly R
// of them, whatever the column count.
func TestHistoryRowsCostOneGridResolutionPerRow(t *testing.T) {
	term := histTerm(t, 80, 5)
	histFeed(t, term, histNumbered(20))

	// 16 rows of history, 80 columns each: 1280 positions from 16 resolutions.
	const rows = 16
	before := ghosttytest.GridResolutions()
	page := histPage(t, term, 0, rows)
	after := ghosttytest.GridResolutions()
	if len(page.Rows) != rows {
		t.Fatalf("rows returned: %d, want %d", len(page.Rows), rows)
	}
	if spent := after - before; spent != rows {
		t.Fatalf("reading %d rows of 80 columns spent %d grid-reference resolutions, want exactly %d (one per row)",
			rows, spent, rows)
	}
}

// The read a range promises: whole pages, never a torn one. A program feeds
// while a reader walks history by range, and every page that comes back must
// be internally consistent — its rows the numbered lines its window names,
// ascending by exactly one. A reader that straddled two states of the buffer
// would read a jump, and a torn row would not parse at all.
func TestHistoryRowsStayWholeWhileTheProgramFeeds(t *testing.T) {
	term := histTerm(t, 80, 5)

	const totalLines = 2000
	const batch = 100
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		for b := 0; b < totalLines/batch; b++ {
			var sb strings.Builder
			for i := range batch {
				fmt.Fprintf(&sb, "L%04d\r\n", b*batch+i)
			}
			if _, err := term.Ingest([]byte(sb.String())); err != nil {
				t.Errorf("ingest batch %d: %v", b, err)
				return
			}
		}
	}()

	var mu sync.Mutex
	var problems []string
	// checkPage holds mu and judges one page against the only invariant an
	// interleaved program cannot break: its rows are the numbered lines,
	// ascending by exactly one. A reader that straddled two states of the
	// buffer would read a jump; a torn row would not parse at all. Absolute
	// indexes are NOT asserted — retention prunes real pages under this
	// feed, and the history a window names shifts when it does.
	checkPage := func(page emulator.HistoryPage, label string) {
		prev := -1
		for i, row := range page.Rows {
			text := histText(row)
			if len(text) != 5 || text[0] != 'L' {
				problems = append(problems, fmt.Sprintf("%s row %d: %q is not a numbered line", label, i, text))
				return
			}
			n, err := strconv.Atoi(text[1:])
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s row %d: %q does not parse", label, i, text))
				return
			}
			if prev >= 0 && n != prev+1 {
				problems = append(problems, fmt.Sprintf("%s rows %d/%d: %d then %d, not consecutive", label, i-1, i, prev, n))
				return
			}
			prev = n
		}
	}
	next := 0
	reads := 0
	for {
		page, err := term.HistoryRows(next, 64)
		if err != nil {
			problems = append(problems, fmt.Sprintf("read at %d: %v", next, err))
			break
		}
		mu.Lock()
		reads++
		checkPage(page, "walk")
		mu.Unlock()
		next += len(page.Rows)
		if len(page.Rows) == 0 {
			// Caught up with the program: wait for it to finish, take one
			// final read, and stop. Bounded, with no clock in sight.
			<-fed
			final, err := term.HistoryRows(next, 64)
			if err != nil {
				problems = append(problems, fmt.Sprintf("final read at %d: %v", next, err))
			} else {
				mu.Lock()
				reads++
				checkPage(final, "final")
				mu.Unlock()
				next += len(final.Rows)
			}
			break
		}
		if next >= totalLines {
			break
		}
	}
	// The feeder may still be running when the walk stopped short of the
	// end; nothing is asserted about the terminal until it has finished.
	<-fed
	mu.Lock()
	defer mu.Unlock()
	if len(problems) > 0 {
		t.Fatalf("history range read tore under concurrent feeds (%d pages read): %v", reads, problems)
	}
	if reads < 2 || next == 0 {
		t.Fatalf("walk covered %d rows across %d pages: the walk did not walk", next, reads)
	}
}
