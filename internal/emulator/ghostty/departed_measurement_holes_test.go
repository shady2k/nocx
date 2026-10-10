package ghostty

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// THE TWO WAYS A FEED'S DEPARTURES CAN BE UNREADABLE, each pinned by the seam
// that reaches it (nocx-2v80t.3.9).
//
// The port promises one report per row for as long as the session lives, and it
// has exactly two honest ways to fail that promise: a measurement that could
// not be taken, and a budget that pruned the history the measurement is taken
// against. Both must reach the consumer as a GAP. Neither may reach it as
// silence, because a consumer told nothing about rows it never received cannot
// tell a lost feed from an idle one — and that is how a whole command's output
// disappeared from the e2e's transcript while every other feed read clean.

// numberedRows is one command's output: n rows tagged with the command's own
// letter, so a report's first and last rows say which command they came from.
func numberedRows(tag string, n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "%s%04d\r\n", tag, i)
	}
	return sb.String()
}

// A measurement the terminal could not take leaves a span nobody counted. The
// feed that fails is reported — and so is the NEXT one, because the rows that
// left the screen while the depth was unreadable are still unread and cannot be
// separated from the rows the recovering feed departs. Re-baselining across
// that span in silence was the defect: the adapter reported neither rows nor a
// gap, so the consumer saw a feed that scrolled nothing.
func TestATornMeasurementIsReportedAsAGapAndNeverAsSilence(t *testing.T) {
	term := departedTerm(t, 80, 24)
	gt, ok := term.(*terminal)
	if !ok {
		t.Fatalf("the port is %T, not the ghostty terminal", term)
	}

	// A first command, measured normally: the baseline is good and the
	// consumer is told what left.
	departedFeed(t, term, numberedRows("A", 40))
	if got, err := term.DepartedRows(); err != nil || len(got) == 0 {
		t.Fatalf("the first command: %d rows, %v; want a normal report", len(got), err)
	}

	// The next measurement cannot be taken: the depth read fails, which is the
	// seam the library itself never fails on request.
	boom := errors.New("the depth could not be read")
	gt.readDepth = func() (int, error) { return 0, boom }
	departedFeed(t, term, numberedRows("B", 40))
	tornRows, tornErr := term.DepartedRows()
	if tornErr == nil {
		t.Fatalf("the feed whose depth could not be read reported %d rows and no gap: a measurement that was never taken is not a measurement", len(tornRows))
	}
	if !errors.Is(tornErr, boom) {
		t.Fatalf("the failed measurement reported %v, want the read failure %v", tornErr, boom)
	}
	gt.readDepth = gt.scrollbackLocked

	// And now the feed that recovers: its own departures are unread along with
	// the torn span's, so it owes a gap. THIS is the assertion the defect
	// fails: the old code re-baselined here and reported nothing at all.
	departedFeed(t, term, numberedRows("C", 40))
	rows, err := term.DepartedRows()
	if err == nil {
		t.Fatalf("the feed after the torn measurement reported %d rows and no gap: the rows that left the screen while the depth was unreadable were swallowed", len(rows))
	}
	if len(rows) != 0 {
		t.Fatalf("the feed after the torn measurement handed over %d rows: the torn span cannot be separated from them, so none may be claimed", len(rows))
	}

	// The count re-baselined, so the terminal is not stuck: the next feed
	// measures and reports normally again.
	departedFeed(t, term, numberedRows("D", 40))
	recovered, err := term.DepartedRows()
	if err != nil {
		t.Fatalf("the feed after the recovery was struck too: %v", err)
	}
	if len(recovered) == 0 {
		t.Fatal("no row was reported once the terminal could measure again: the torn span left the baseline unusable")
	}
	if first := departedText(recovered[0]); strings.HasPrefix(first, "A") {
		t.Fatalf("the recovered report starts at %q, a row that left the screen before the tear and was already handed over: the torn span was re-reported",
			first)
	}
	if last := departedText(recovered[len(recovered)-1]); !strings.HasPrefix(last, "D") {
		t.Fatalf("the recovered report ends at %q, not the D command's own output: the recovering feed's departures were not captured",
			last)
	}

	// What the holes cost is stated once, and the terminal kept running: the
	// three commands that left the screen during and after the torn span are
	// unread, and that is what the consumer was told.
	t.Logf("torn=%v recovered=%d rows", tornErr, len(recovered))
}

// A retention prune is explicit through history-erased callbacks: the rows
// that leave during the feed are copied before their pages die, so the port
// reports the complete stream without a synthetic hole.
func TestABudgetThatPrunesKeepsDepartedRowsComplete(t *testing.T) {
	term := departedTerm(t, 80, 24)
	departedRetention(t, term, 2_000)

	reported := 0
	for c := 1; c <= 30; c++ {
		departedFeed(t, term, numberedRows("P", 100))
		rows, err := term.DepartedRows()
		if err != nil {
			t.Fatalf("command %d: %v", c, err)
		}
		if len(rows) == 0 {
			t.Fatalf("command %d reported 0 rows: a hundred lines scrolled a 24-row screen", c)
		}
		reported += len(rows)
	}
	if reported != 2_977 {
		t.Fatalf("reported %d rows, want 2977", reported)
	}
}
