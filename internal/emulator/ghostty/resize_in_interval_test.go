package ghostty

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// A geometry change landing INSIDE an interval — the one thing fixed-geometry
// never has, and the shape the bead's e2e shows: an interval whose departure
// count stands still while its screen moves on (nocx-2v80t.3.9). Each
// scenario drives the adapter the way the runtime does (one drain per ingest),
// logs the raw per-step numbers, and then holds it to the producer's
// invariant: a resize may re-base what is reported NEXT, never withhold what
// already left.

const (
	resizeFence = "\x1b]1337;NOCX_FENCE;ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01\x07"
	resizeLines = 100
	resizeCmds  = 4
)

func resizeRowText(r emulator.Row) string {
	var sb strings.Builder
	for _, c := range r.Cells {
		if c.Grapheme != "" {
			sb.WriteString(c.Grapheme)
		}
	}
	return strings.TrimSpace(sb.String())
}

func resizeLinesOf(cmd, from, to int) string {
	var sb strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&sb, "transcript-%04d-%03d\r\n", cmd, i)
	}
	return sb.String()
}

// TestAResizeInsideAnIntervalHoldsNoDeparturesBack covers grow and shrink
// mid-command, a grow-shrink-grow straddling two commands, and a resize
// landing between a command's feed and its drain.
func TestAResizeInsideAnIntervalHoldsNoDeparturesBack(t *testing.T) {
	scenarios := []struct {
		name  string
		steps []string
	}{
		{"grow mid-command", []string{"feed:0:50", "resize:40", "feed:50:100", "drain"}},
		{"shrink mid-command", []string{"feed:0:50", "resize:12", "feed:50:100", "drain"}},
		{"grow-shrink-grow across the boundary", []string{"resize:40", "resize:14", "resize:26", "feed:0:100", "drain"}},
		{"resize between the feed and the drain", []string{"feed:0:100", "resize:40", "drain"}},
		{"no resize (control)", []string{"feed:0:100", "drain"}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			port, err := New(emulator.Geometry{Cols: 80, Rows: 26})
			if err != nil {
				t.Fatalf("build the emulator: %v", err)
			}
			t.Cleanup(func() { port.Close() })
			term, ok := port.(*terminal)
			if !ok {
				t.Fatal("the port is not the ghostty terminal")
			}

			reported := map[string]int{}
			for cmd := 1; cmd <= resizeCmds; cmd++ {
				if err := driveResizeScenario(t, term, sc.name, cmd, sc.steps, reported); err != nil {
					t.Fatal(err)
				}
			}
			for marker, times := range reported {
				if times > 1 {
					t.Fatalf("row %s was reported %d times: a row that left is reported once", marker, times)
				}
			}
			// Every row that scrolled off is owed, whatever the pane did in
			// between: only the tail still on the screen at the end may be
			// missing from the report. A debt charged for rows nobody has
			// been handed yet shows up here as a shortfall.
			if want := resizeCmds*resizeLines - 40; len(reported) < want {
				t.Fatalf("the report carried %d of the %d rows that scrolled off (%d short, scenario %q): "+
					"a debt charged for a row that was still pending cancels a departure nobody received",
					len(reported), want, want-len(reported), sc.name)
			}
		})
	}
}

// driveResizeScenario runs one command's steps and asserts that the command
// that scrolled rows off the screen reported them.
func driveResizeScenario(t *testing.T, term *terminal, scenario string, cmd int, steps []string, reported map[string]int) error {
	t.Helper()
	before := term.sb[0]
	for _, st := range steps {
		switch {
		case strings.HasPrefix(st, "resize:"):
			var rows int
			if _, err := fmt.Sscanf(st, "resize:%d", &rows); err != nil {
				return fmt.Errorf("resize step %q: %w", st, err)
			}
			if _, err := term.Resize(emulator.Geometry{Cols: 80, Rows: rows}); err != nil {
				return fmt.Errorf("resize to %d: %w", rows, err)
			}
			// The reflow's own reading, taken the instant the resize returns:
			// if the library defers the reflow to the next write, the depth
			// the next feed's measurement starts from is this one.
			b := term.sb[0]
			t.Logf("  resize:%d -> depth=%d valid=%t owed=%d pending=%d top=%q",
				rows, b.rows, b.valid, b.owed, len(term.departed), resizeTopText(term, b.rows))
		case strings.HasPrefix(st, "feed:"):
			var from, to int
			if _, err := fmt.Sscanf(st, "feed:%d:%d", &from, &to); err != nil {
				return fmt.Errorf("feed step %q: %w", st, err)
			}
			body := resizeLinesOf(cmd, from, to)
			if to == resizeLines {
				body += resizeFence
			}
			b := term.sb[0]
			beforePending := len(term.departed)
			if _, err := term.Ingest([]byte(body)); err != nil {
				return fmt.Errorf("feed %s: %w", st, err)
			}
			a := term.sb[0]
			t.Logf("  feed %s -> depth %d->%d d=%d owed %d->%d pending %d->%d topBefore=%q topAfter=%q newest3=%s",
				st, b.rows, a.rows, a.rows-b.rows, b.owed, a.owed, beforePending, len(term.departed),
				resizeTopText(term, b.rows), resizeTopText(term, a.rows), resizeNewestTexts(term, a.rows))
		case st == "drain":
			rows, err := term.DepartedRows()
			if err != nil {
				return fmt.Errorf("drain: %w", err)
			}
			base := term.sb[0]
			got := 0
			for _, r := range rows {
				marker := resizeRowText(r)
				if !strings.HasPrefix(marker, "transcript-") {
					continue
				}
				// Every marker counts, whichever command printed it: a drain
				// legitimately carries the previous command's tail (the rows
				// still on the screen when that command's own drain ran).
				reported[marker]++
				if strings.HasPrefix(marker, fmt.Sprintf("transcript-%04d-", cmd)) {
					got++
				}
			}
			first, last := "-", "-"
			if len(rows) > 0 {
				first = resizeRowText(rows[0])
				last = resizeRowText(rows[len(rows)-1])
			}
			t.Logf("scenario=%q cmd=%d h=%d d=%d base.rows=%d valid=%t owed=%d->%d pending=%d reported=%d window=[%q .. %q]",
				scenario, cmd, base.rows, base.rows-before.rows, before.rows, before.valid, before.owed, base.owed, len(rows), got, first, last)
			if got == 0 {
				return fmt.Errorf("command %d scrolled its output off the screen and the report carried none of its rows (scenario %q)", cmd, scenario)
			}
		}
	}
	return nil
}

// resizeTopText is the text of the row at the top of the active area: history
// row h is the first row below the history, which is where a scroll shows.
func resizeTopText(term *terminal, h int) string {
	row, err := term.rowAt(pointHistory, h)
	if err != nil {
		return "?"
	}
	return resizeRowText(row)
}

// resizeNewestTexts is the newest three history rows, in departure order.
func resizeNewestTexts(term *terminal, h int) string {
	out := make([]string, 0, 3)
	for y := h - 3; y < h; y++ {
		if y < 0 {
			continue
		}
		row, err := term.rowAt(pointHistory, y)
		if err != nil {
			continue
		}
		out = append(out, resizeRowText(row))
	}
	return strings.Join(out, " | ")
}
