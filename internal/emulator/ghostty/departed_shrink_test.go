package ghostty

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// A ROW A SHRINKING PANE PUSHES INTO HISTORY HAS LEFT THE SCREEN
// (nocx-2v80t.3.41).
//
// It is not on the screen any more and it is on no later screen either, so a
// caller that keeps what leaves — a block's rows — is told now or never: the
// transcript-budget e2e lost transcript-0151-074 to a pane going 28 -> 27 rows
// between the command's output and its fence. And a row a GROWING pane pulls
// back was reported when it went, so it is not reported again, and until it
// leaves the screen answers it among [emulator.Terminal.ReportedRowsOnScreen].

func shrinkGeometry(rows int) emulator.Geometry {
	return emulator.Geometry{Cols: 20, Rows: rows, CellWidthPx: 10, CellHeightPx: 20}
}

func shrinkLines(from, to int) []byte {
	var out []byte
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("row-%04d\r\n", i)...)
	}
	return out
}

// shrinkReport drains the report into texts.
func shrinkReport(t *testing.T, term emulator.Terminal) []string {
	t.Helper()
	rows, err := term.DepartedRows()
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, resizeRowText(r))
	}
	return out
}

func TestAShrinkReportsTheRowsItPushesIntoHistory(t *testing.T) {
	term := departedTerm(t, 20, 5)
	// Ten lines on five rows: row-0000..row-0005 leave, row-0006..row-0009
	// and the cursor's blank row are the screen.
	if _, err := term.Ingest(shrinkLines(0, 10)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if got := shrinkReport(t, term); len(got) != 6 || got[0] != "row-0000" || got[5] != "row-0005" {
		t.Fatalf("the feed reported %q, want row-0000..row-0005", got)
	}

	if _, err := term.Resize(shrinkGeometry(3)); err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if got := shrinkReport(t, term); fmt.Sprint(got) != "[row-0006 row-0007]" {
		t.Fatalf("a shrink by two rows reported %q, want the two rows it pushed off the top: [row-0006 row-0007]", got)
	}
	if n, err := term.ReportedRowsOnScreen(); err != nil || n != 0 {
		t.Fatalf("after a shrink ReportedRowsOnScreen = %d, %v; want 0, nil", n, err)
	}

	// Growing back pulls exactly those two onto the screen again: already
	// reported, so the screen says so, and they are not reported a second
	// time when they leave.
	if _, err := term.Resize(shrinkGeometry(5)); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if got := shrinkReport(t, term); len(got) != 0 {
		t.Fatalf("a growth reported %q, want nothing: a refill is not a departure", got)
	}
	if n, err := term.ReportedRowsOnScreen(); err != nil || n != 2 {
		t.Fatalf("after the growth ReportedRowsOnScreen = %d, %v; want the 2 rows it pulled back", n, err)
	}
	if _, err := term.Ingest(shrinkLines(10, 13)); err != nil {
		t.Fatalf("feed after the growth: %v", err)
	}
	if got := shrinkReport(t, term); fmt.Sprint(got) != "[row-0008]" {
		t.Fatalf("three more lines reported %q, want only [row-0008]: row-0006 and row-0007 left once already", got)
	}
	if n, err := term.ReportedRowsOnScreen(); err != nil || n != 0 {
		t.Fatalf("once the pulled-back rows left again ReportedRowsOnScreen = %d, %v; want 0", n, err)
	}
}

// Every row is reported exactly once across a pane that breathes — the
// pattern the e2e's renderer produces — and none is missing: the
// double-report guard (TestARepeatedResizeCycleDoesNotReReportARow) and the
// loss this bead measured are the two halves of one invariant.
func TestEveryRowIsReportedOnceAcrossABreathingPane(t *testing.T) {
	term := departedTerm(t, 20, 28)
	reported := map[string]int{}
	var order []string
	drain := func() {
		for _, text := range shrinkReport(t, term) {
			reported[text]++
			order = append(order, text)
		}
	}
	resize := func(rows int) {
		if _, err := term.Resize(shrinkGeometry(rows)); err != nil {
			t.Fatalf("resize to %d rows: %v", rows, err)
		}
		drain()
	}
	next := 0
	feed := func(n int) {
		if _, err := term.Ingest(shrinkLines(next, next+n)); err != nil {
			t.Fatalf("feed: %v", err)
		}
		next += n
		drain()
	}

	for _, rows := range []int{27, 33, 28, 27, 33, 27, 28} {
		feed(100)
		resize(rows)
	}
	// Push whatever is still on the screen off it, so every numbered row has
	// had its chance to leave.
	if _, err := term.Ingest([]byte(fmt.Sprintf("%040d", 0))); err != nil {
		t.Fatalf("feed: %v", err)
	}
	for range 40 {
		if _, err := term.Ingest([]byte("\r\n")); err != nil {
			t.Fatalf("feed: %v", err)
		}
	}
	drain()

	for i := range next {
		text := fmt.Sprintf("row-%04d", i)
		if n := reported[text]; n != 1 {
			t.Fatalf("%s was reported %d times, want exactly once", text, n)
		}
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] >= order[i] && order[i] != "" && order[i-1] != "" && order[i][:4] == "row-" && order[i-1][:4] == "row-" {
			t.Fatalf("the report ran out of order: %q before %q", order[i-1], order[i])
		}
	}
}

func TestReportedRowsOnScreenIsNothingOnTheAlternateScreen(t *testing.T) {
	term := departedTerm(t, 20, 5)
	if _, err := term.Ingest(shrinkLines(0, 10)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	shrinkReport(t, term)
	if _, err := term.Resize(shrinkGeometry(8)); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if n, _ := term.ReportedRowsOnScreen(); n == 0 {
		t.Fatal("test setup: the growth pulled nothing back")
	}
	if _, err := term.Ingest([]byte("\x1b[?1049h")); err != nil {
		t.Fatalf("enter the alternate screen: %v", err)
	}
	if n, err := term.ReportedRowsOnScreen(); err != nil || n != 0 {
		t.Fatalf("on the alternate screen ReportedRowsOnScreen = %d, %v; want 0: it has no history to have pulled back", n, err)
	}
	term.Close()
	if _, err := term.ReportedRowsOnScreen(); err != emulator.ErrClosed {
		t.Fatalf("after close ReportedRowsOnScreen answered %v, want %v", err, emulator.ErrClosed)
	}
}
