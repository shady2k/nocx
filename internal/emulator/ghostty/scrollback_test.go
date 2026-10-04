package ghostty

// The budget tests drive the port the way a session does: the emulator is
// the real libghostty, the budget arrives through ApplyScrollback — the one
// path a session's runtime is wired to — and what is asserted is what a
// consumer of the emulator can see: the history a read reaches, and the
// departure report's errors. Every number the tests rely on was measured
// against the linked archive (scrollback.go's constants carry them).

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// budgetApply configures a terminal's retention through the production path,
// the way every session's is once the setting exists.
func budgetApply(t *testing.T, term emulator.Terminal, lines uint64) {
	t.Helper()
	gt, ok := term.(*terminal)
	if !ok {
		t.Fatalf("the port is %T, not the ghostty terminal", term)
	}
	if err := gt.ApplyScrollback(lines); err != nil {
		t.Fatalf("apply the scrollback budget: %v", err)
	}
}

// budgetTotal reads how many history rows the terminal retains right now.
func budgetTotal(t *testing.T, term emulator.Terminal) int {
	t.Helper()
	page, err := term.HistoryRows(0, 0)
	if err != nil {
		t.Fatalf("read the retained total: %v", err)
	}
	return page.Total
}

// styledLine is one heavily styled physical row: every cell of an 80-column
// row carries its own truecolour pair, so the row's cell storage costs what
// the bounded measurement called 5-7 KB — several times a plain row.
func styledLine(i int) string {
	var sb strings.Builder
	for c := 0; c < 80; c++ {
		fmt.Fprintf(&sb, "\x1b[1;38;2;%d;%d;255;48;2;%d;%d;0mX", i%256, c%256, (i*7)%256, (c*7)%256)
	}
	sb.WriteString("\x1b[0m\r\n")
	return sb.String()
}

// Criterion 1: a session at the default retains at least the 10,000 physical
// lines it promises, against the real emulator. The library prunes to a page
// boundary, so the port applies the ask one page wide (scrollback.go); this
// is the test that holds the port to the promise the screen makes.
func TestApplyScrollbackDefaultRetainsThePromisedLines(t *testing.T) {
	term := departedTerm(t, 80, 24)
	budgetApply(t, term, 10_000)

	for i := 0; i < 20_000; i++ {
		departedFeed(t, term, fmt.Sprintf("L%06d\r\n", i))
	}

	if got := budgetTotal(t, term); got < 10_000 {
		t.Fatalf("a session at the default retains %d plain lines, want at least 10,000", got)
	}
}

// Criterion 2: zero leaves no history. What was retained is gone at once,
// and nothing accumulates afterwards — 0 means only the current screen.
func TestApplyScrollbackZeroLeavesNoHistory(t *testing.T) {
	term := departedTerm(t, 80, 24)
	budgetApply(t, term, 10_000)
	departedFeed(t, term, numbered(500))
	if got := budgetTotal(t, term); got == 0 {
		t.Fatalf("precondition: nothing was retained before the zero")
	}
	// The first feed's departures are queued until they are read; read them
	// here, so the report the zero leaves behind is this test's own and not
	// the queue of rows erased before it.
	if _, err := term.DepartedRows(); err != nil {
		t.Fatalf("precondition: the first feed reported a loss: %v", err)
	}

	budgetApply(t, term, 0)
	if got := budgetTotal(t, term); got != 0 {
		t.Fatalf("after 0 the terminal retains %d rows, want none — zero erases what was kept", got)
	}

	// And it stays gone: the budget is in force, not a one-off erase. The
	// rows the next output pushes off the screen are destroyed as they
	// leave — there is no history for a report to read them out of, so the
	// report is silence. That is the data loss the value 0 asks for, and
	// the setting's screen says so before it is saved.
	departedFeed(t, term, numbered(200))
	if got := budgetTotal(t, term); got != 0 {
		t.Fatalf("after more output at 0 the terminal retains %d rows, want none", got)
	}
	if rows, err := term.DepartedRows(); err != nil || len(rows) != 0 {
		t.Fatalf("a feed at zero reported %d rows, err %v; want silence — nothing is retained to read from", len(rows), err)
	}
}

// Criterion 3: lowering prunes at once, and the read cannot reach past the
// new limit by more than one page. The one-page bound at this geometry is
// the compensation's own (scrollbackPageRowsAt(80) = 512 rows).
func TestApplyScrollbackLoweringPrunesAtOnce(t *testing.T) {
	term := departedTerm(t, 80, 24)
	budgetApply(t, term, 10_000)
	departedFeed(t, term, numbered(5000))
	before := budgetTotal(t, term)
	if before < 500 {
		t.Fatalf("precondition: depth %d after 5000 lines", before)
	}

	budgetApply(t, term, 500)
	got := budgetTotal(t, term)
	if beyond := got - 500; beyond > 512 {
		t.Fatalf("after lowering to 500 the terminal retains %d rows — %d beyond the ask, more than one page", got, beyond)
	}
	if got < 500 {
		t.Fatalf("after lowering to 500 the terminal retains %d rows, below the ask the widening exists to guarantee", got)
	}
}

// Criterion 4: heavily styled output retains fewer lines than plain at the
// same setting — the byte ceiling is real and is asserted, not assumed.
func TestApplyScrollbackStyledOutputRetainsFewerLinesThanPlain(t *testing.T) {
	plain := departedTerm(t, 80, 24)
	styled := departedTerm(t, 80, 24)
	budgetApply(t, plain, 10_000)
	budgetApply(t, styled, 10_000)

	for i := 0; i < 12_000; i++ {
		s := fmt.Sprintf("L%06d\r\n", i)
		departedFeed(t, plain, s)
		departedFeed(t, styled, styledLine(i))
	}

	plainTotal := budgetTotal(t, plain)
	styledTotal := budgetTotal(t, styled)
	if plainTotal < 10_000 {
		t.Fatalf("precondition: plain retained %d, the default's own promise failed", plainTotal)
	}
	if styledTotal >= plainTotal {
		t.Fatalf("styled output retained %d rows against plain's %d — the byte ceiling never bound",
			styledTotal, plainTotal)
	}
}

// Lowering the budget must not read as a loss. The prune the apply takes is
// rows that already departed and were already reported; the next feed
// reports its own departures and no error. This is the test the re-baseline
// exists for: without it the stale baseline reads the depth's fall as
// in-feed retention pruning and flags the interval incomplete.
//
// The assertion is the SUBJECT against a CONTROL that did everything but the
// lowering — the same second feed must produce the same report on both, so
// the test cannot pass on a miscounted expectation.
func TestApplyScrollbackLoweringDoesNotMakeTheNextFeedReportALoss(t *testing.T) {
	control := departedTerm(t, 80, 24)
	subject := departedTerm(t, 80, 24)
	budgetApply(t, control, 3_000)
	budgetApply(t, subject, 3_000)

	for _, term := range []emulator.Terminal{control, subject} {
		departedFeed(t, term, numbered(2_000))
		if _, err := term.DepartedRows(); err != nil {
			t.Fatalf("precondition: the first feed reported a loss: %v", err)
		}
	}

	// The one difference between the two: the subject's budget drops.
	budgetApply(t, subject, 1_000)

	departedFeed(t, control, numbered(50))
	departedFeed(t, subject, numbered(50))

	controlRows, controlErr := control.DepartedRows()
	subjectRows, subjectErr := subject.DepartedRows()
	if subjectErr != nil {
		t.Fatalf("the feed after lowering reported a loss: %v", subjectErr)
	}
	if controlErr != nil {
		t.Fatalf("precondition: the control's own feed reported a loss: %v", controlErr)
	}
	if len(subjectRows) != len(controlRows) {
		t.Fatalf("after lowering the feed reported %d rows, want the control's %d",
			len(subjectRows), len(controlRows))
	}
	for i := range controlRows {
		if got, want := departedText(subjectRows[i]), departedText(controlRows[i]); got != want {
			t.Fatalf("after lowering the feed's row %d reads %q, want the control's %q", i, got, want)
		}
	}
}

// Retention pruning inside a feed is copied through the history-erased effect
// instead of being guessed from the net scrollback depth.
func TestApplyScrollbackRetentionPruneKeepsDepartedRowsComplete(t *testing.T) {
	term := departedTerm(t, 80, 24)
	budgetApply(t, term, 3_000)

	departedFeed(t, term, numbered(3_400))
	if _, err := term.DepartedRows(); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if got := budgetTotal(t, term); got >= 3_512 {
		t.Fatalf("precondition: depth %d is at or past the applied limit already", got)
	}

	departedFeed(t, term, numbered(200))
	rows, err := term.DepartedRows()
	if err != nil {
		t.Fatalf("feed crossing the budget: %v", err)
	}
	if len(rows) != 200 {
		t.Fatalf("feed crossing the budget reported %d rows, want 200", len(rows))
	}
}
