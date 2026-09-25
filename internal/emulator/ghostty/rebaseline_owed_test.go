package ghostty

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// rebaselineLocked's own comment says the debt and the pushed-history ledger
// SURVIVE a re-baseline: they are facts about rows that are out of the
// screen's sight, and re-seeding the depth does not un-report or un-push
// them. Measured during nocx-2v80t.3.10's review: that held only for the
// buffer the resize happened to measure. The two tests below are the review's
// own findings, red before the fix (`t.sb[active]` mutated in place rather
// than `t.sb = [2]sbBaseline{}` discarding both) and green after it.

// rebaselineOwedLines feeds n short numbered lines, each on its own hard
// newline, so a small screen (rows fits well under n) genuinely departs some
// of them.
func rebaselineOwedLines(from, to int) string {
	var sb strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&sb, "row-%04d\r\n", i)
	}
	return sb.String()
}

// rebaselineIncurOwed builds a 20x5 terminal, departs 5 rows for real, drains
// them (so the pending report is EMPTY and cannot absorb a later refill —
// see noteDepartedLocked's own comment on the pending-report trim), then
// grows the screen to 10 rows: the refill pulls the 5 already-handed rows
// back onto the screen, and — since they were already reported — that refill
// is charged as owed debt. It returns the terminal with sb[0].owed > 0.
func rebaselineIncurOwed(t *testing.T) *terminal {
	t.Helper()
	port, err := New(emulator.Geometry{Cols: 20, Rows: 5, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("new terminal: %v", err)
	}
	term, ok := port.(*terminal)
	if !ok {
		t.Fatalf("the port is %T, not the ghostty terminal", port)
	}
	t.Cleanup(term.Close)

	if _, err := term.Ingest([]byte(rebaselineOwedLines(0, 10))); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if _, err := term.DepartedRows(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, err := term.Resize(emulator.Geometry{Cols: 20, Rows: 10, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if term.sb[0].owed == 0 {
		t.Fatal("test setup failed: the primary owes nothing after the refill, so this test proves nothing")
	}
	return term
}

// TestRebaselineOnTheAlternateScreenKeepsThePrimarysOwedDebt is the first
// finding: a resize taken while the ALTERNATE screen owns the pane still
// rebaselines — ghostty carries a size per buffer, so the primary reflows
// underneath the alternate exactly as it would on its own — and the old code
// discarded BOTH buffers' owed debt and pushed ledger regardless of which one
// it had just measured, keeping only the one it read. The primary's debt is
// exactly as unpaid after a resize read on the other buffer as before it.
func TestRebaselineOnTheAlternateScreenKeepsThePrimarysOwedDebt(t *testing.T) {
	term := rebaselineIncurOwed(t)
	owedBefore, pushedBefore := term.sb[0].owed, len(term.sb[0].pushed)

	if _, err := term.Ingest([]byte("\x1b[?1049h")); err != nil { // enter the alternate screen
		t.Fatalf("enter the alternate screen: %v", err)
	}
	if _, err := term.Resize(emulator.Geometry{Cols: 24, Rows: 10, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
		t.Fatalf("resize on the alternate screen: %v", err)
	}
	if got := term.sb[0].owed; got != owedBefore {
		t.Fatalf("primary owed %d before the alternate-screen resize, %d after: "+
			"a resize taken on the OTHER buffer must not touch this one's debt", owedBefore, got)
	}
	if got := len(term.sb[0].pushed); got != pushedBefore {
		t.Fatalf("primary's pushed ledger held %d blocks before the alternate-screen resize, %d after",
			pushedBefore, got)
	}

	// Behavioural proof, not just the ledger's own numbers: back on the
	// primary, push the still-owed rows off again and confirm none of them
	// is reported a second time.
	if _, err := term.Ingest([]byte("\x1b[?1049l")); err != nil { // back to the primary
		t.Fatalf("leave the alternate screen: %v", err)
	}
	if _, err := term.Ingest([]byte(rebaselineOwedLines(100, 120))); err != nil {
		t.Fatalf("feed after: %v", err)
	}
	rows, err := term.DepartedRows()
	if err != nil {
		t.Fatalf("drain after: %v", err)
	}
	for _, r := range rows {
		text := resizeRowText(r)
		for i := 0; i < 5; i++ {
			if text == fmt.Sprintf("row-%04d", i) {
				t.Fatalf("row %q, already reported once before the alternate-screen resize, "+
					"was reported again after it dropped the debt that should have covered it", text)
			}
		}
	}
}

// TestRebaselineOnAFailedReadKeepsOwedAndThePushedLedger is the second
// finding: rebaselineLocked's own two read failures — the screen could not be
// read, or the scrollback depth could not be — used to zero BOTH buffers
// wholesale, exactly the same defect as the alternate-screen case reached
// through the OTHER door rebaselineLocked's comment already names ("The debt
// SURVIVES a re-baseline... a failed measurement destroys neither").
//
// The library never fails either read on request (departed_measurement_holes_test.go's
// own comment says so for noteDepartedLocked's identical seam), so this
// routes rebaselineLocked through the same two injectable fields
// (readScreen/readDepth) that function already uses — the fix that makes the
// failure path reachable at all, and part of what closes this finding.
func TestRebaselineOnAFailedReadKeepsOwedAndThePushedLedger(t *testing.T) {
	term := rebaselineIncurOwed(t)
	owedBefore, pushedBefore := term.sb[0].owed, len(term.sb[0].pushed)

	boom := errors.New("the depth could not be read")
	term.readDepth = func() (int, error) { return 0, boom }
	if _, err := term.Resize(emulator.Geometry{Cols: 24, Rows: 10, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	term.readDepth = term.scrollbackLocked

	if got := term.sb[0].owed; got != owedBefore {
		t.Fatalf("owed changed from %d to %d across a rebaseline whose depth read failed", owedBefore, got)
	}
	if got := len(term.sb[0].pushed); got != pushedBefore {
		t.Fatalf("the pushed ledger held %d blocks before the failed rebaseline, %d after",
			pushedBefore, got)
	}
	if term.sb[0].valid {
		t.Fatal("a rebaseline whose read failed must leave the buffer unmeasured (valid=false), " +
			"not claim the depth it never read")
	}
}
