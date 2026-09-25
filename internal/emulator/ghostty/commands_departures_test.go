package ghostty

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// Consecutive commands, as the e2e runs them: each prints a hundred numbered
// lines (the e2e's own line length and volume, 19KB of output, past the
// adapter comment's retention budget) and its own fence, on a pane the layout
// holds at one size. Every row that leaves the screen must be reported, and
// the per-command table this prints is the evidence for where a frozen
// departure count can and cannot come from (nocx-2v80t.3.9): at a FIXED
// geometry the adapter reports every command's departures whole, so a freeze
// needs the one variable this test does not have — the pane changing size
// mid-interval.
func TestConsecutiveCommandsReportEveryRowThatLeaves(t *testing.T) {
	const (
		commands = 10
		lines    = 100
		fence    = "\x1b]1337;NOCX_FENCE;ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01\x07"
	)
	g := emulator.Geometry{Cols: 80, Rows: 26}
	term, err := New(g)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	t.Cleanup(func() { term.Close() })
	t2, ok := term.(*terminal)
	if !ok {
		t.Fatal("the port is not the ghostty terminal")
	}

	total := 0
	for cmd := 1; cmd <= commands; cmd++ {
		before := t2.sb[0]
		var sb strings.Builder
		for i := 0; i < lines; i++ {
			fmt.Fprintf(&sb, "transcript-%04d-%03d\r\n", cmd, i)
		}
		sb.WriteString(fence)
		if _, err := term.Ingest([]byte(sb.String())); err != nil {
			t.Fatalf("feed command %d: %v", cmd, err)
		}
		rows, err := term.DepartedRows()
		if err != nil {
			t.Fatalf("drain command %d: %v", cmd, err)
		}
		after := t2.sb[0]
		t.Logf("cmd=%d departed=%d depth=%d->%d base.rows=%d valid=%t owed=%d->%d err=%v",
			cmd, len(rows), before.rows, after.rows, after.rows, after.valid, before.owed, after.owed, err)
		total += len(rows)
	}

	// Six commands of a hundred lines on a twenty-six row screen: the screen
	// holds the last twenty-six, so the rest left it — and every one of those
	// rows is a departure the report owes.
	// The pane holds `g.Rows` rows of the 1000 printed, one of which is the
	// live cursor line: everything above it left the screen.
	if want := commands*lines - (g.Rows - 1); total != want {
		t.Fatalf("the report carried %d departures, want %d: %d rows left the screen and were never reported",
			total, want, want-total)
	}
}
