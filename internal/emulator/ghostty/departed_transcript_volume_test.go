package ghostty

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// THE E2E'S OWN TRANSCRIPT, IN-PROCESS (nocx-2v80t.3.9).
//
// The reduced acceptance spec (e2e/transcript-scroll-budget.spec.ts, BLOCKS =
// 10) runs ten commands that each print a hundred numbered lines on a pane the
// layout holds at one size; the pane is spawned at 80x24 and then takes the
// frontend's measured geometry. One PTY read carries a whole command's ~2 KB
// burst (the helper's read page is 32 KiB), so each command is ONE feed and one
// drain of the departure report — exactly what these tests reproduce.
//
// WHAT WENT WRONG, and what these tests hold shut. Nothing configured the
// library's retention, so its default applied, and that default is a grid
// CAPACITY rather than a count of text: measured across widths it saturates at
// about 80k cells, so a WIDER pane retains FEWER rows (1073 at 80 columns, 873
// at 100, 673 at 120, 573 at 148). The e2e's pane is wide — its own numbers
// (saturation at 581 rows, a 252-row prune) put it at ~148 columns — and the
// SEVENTH command of ten crossed the budget. The library then pruned whole
// pages inside that single feed, the depth read after the feed was below its
// baseline (581 -> 329), and the feed's own departures were indistinguishable
// from the pruned pages: the adapter reported one struck feed with zero rows,
// the interval departed nothing, its end marker repeated the previous
// interval's endRow, and the block was sealed holding only its closing screen.
//
// The fix is in terminal.go's install: the adapter clears both budgets, so the
// history its departures are read out of lives as long as the session does.
// These two tests are the criterion — one at the spec's own shape, one well
// past the volume where the old default fired.

// transcribed is one command of the transcript the e2e runs, as the pane sees
// it: the command line echoed once, then a hundred numbered rows.
func transcribed(cmd, lines int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "printf 'transcript-%04d-%%03d\\n' {1..100}\r\n", cmd)
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&sb, "transcript-%04d-%03d\r\n", cmd, i)
	}
	return sb.String()
}

// retainedHistory reads the terminal's own depth: how many history rows it is
// holding right now. It is the library's answer to "what do you still have",
// and with no budget in force it must hold every row the adapter has handed
// over.
func retainedHistory(t *testing.T, term emulator.Terminal) int {
	t.Helper()
	page, err := term.HistoryRows(0, 0)
	if err != nil {
		t.Fatalf("history depth: %v", err)
	}
	return page.Total
}

// TestEveryCommandInAFullTranscriptKeepsItsDepartedRows is the e2e's own
// criterion at the e2e's own shape: every command's rows reach the consumer, in
// order, and nothing the transcript pushed into history is ever destroyed
// behind the reader. It fails on the first command that loses its output —
// which, before the fix, was the seventh, exactly as the spec showed.
func TestEveryCommandInAFullTranscriptKeepsItsDepartedRows(t *testing.T) {
	const (
		cols  = 148 // the e2e's measured pane width, from its own saturation
		rows  = 28  // its screen: the run's end markers carried 28 closing rows
		lines = 100 // one command's output, the spec's own shape
		cmds  = 10  // the reduced spec's BLOCKS
	)

	term := departedTerm(t, cols, rows)
	wantAtLeast := lines - rows

	total := 0
	for c := 1; c <= cmds; c++ {
		departedFeed(t, term, transcribed(c, lines))
		got, err := term.DepartedRows()
		total += len(got)
		t.Logf("command %d: reported=%d retained=%d err=%v", c, len(got), retainedHistory(t, term), err)

		if err != nil {
			t.Errorf("command %d: the departure report was struck: %v\n"+
				"its rows left the screen and were not handed over; the block that should hold this command holds nothing",
				c, err)
			continue
		}
		if len(got) < wantAtLeast {
			t.Errorf("command %d reported %d rows, want at least %d — every line the %d-row screen pushed off",
				c, len(got), wantAtLeast, rows)
		}
		// The library's own read of its retention: rows handed over are still
		// there. A prune behind the reader shows up here first — the history
		// would hold less than the adapter has reported.
		if retained := retainedHistory(t, term); retained != total {
			t.Fatalf("after command %d the history holds %d rows and the adapter has reported %d: %d rows were destroyed behind the reader",
				c, retained, total, total-retained)
		}
	}
}

// TestAFullTranscriptIsNotPrunedWellPastTheOldBoundary is the control: the same
// shape at the volume where the library's inherited default used to fire. At 80
// columns that default saturated at 1073 rows and pruned at the twelfth command
// of a hundred lines; fifty commands retain five thousand rows and must lose
// none of them. A budget that creeps back in — a default inherited again, a
// limit set without a reason — fails here.
func TestAFullTranscriptIsNotPrunedWellPastTheOldBoundary(t *testing.T) {
	const (
		cols  = 80
		rows  = 24
		lines = 100
		cmds  = 50 // five times the volume at which the inherited default pruned
	)

	term := departedTerm(t, cols, rows)

	total := 0
	for c := 1; c <= cmds; c++ {
		departedFeed(t, term, transcribed(c, lines))
		got, err := term.DepartedRows()
		total += len(got)
		if err != nil {
			t.Fatalf("command %d of %d: the departure report was struck at %d retained rows: %v",
				c, cmds, retainedHistory(t, term), err)
		}
		if len(got) < lines-rows {
			t.Fatalf("command %d reported %d rows, want at least %d", c, len(got), lines-rows)
		}
	}

	retained := retainedHistory(t, term)
	t.Logf("%d commands, %d rows handed over, %d rows retained", cmds, total, retained)
	if retained != total {
		t.Fatalf("the history holds %d rows and the adapter reported %d: %d rows were destroyed behind the reader",
			retained, total, total-retained)
	}
	if retained < 4_000 {
		t.Fatalf("only %d rows retained after %d commands: this volume no longer reaches past the boundary the test exists for", retained, cmds)
	}
}
