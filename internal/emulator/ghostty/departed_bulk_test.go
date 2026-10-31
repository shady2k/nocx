package ghostty

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty/ghosttytest"
)

func TestDepartedBulkCaptureResolvesEachRowOnce(t *testing.T) {
	for _, cols := range []int{10, 80, 120} {
		for _, ending := range []struct {
			name string
			vt   string
		}{
			{"scroll", ""},
			{"reset", "\x1bc"},
			{"erase-history", "\x1b[3J"},
			{"prune", ""},
		} {
			t.Run(fmt.Sprintf("%s/%d", ending.name, cols), func(t *testing.T) {
				term := departedTerm(t, cols, 5)
				if ending.name == "prune" {
					departedRetention(t, term, 30)
				}
				const lines = 5000
				before := ghosttytest.GridResolutions()
				departedFeed(t, term, numbered(lines)+ending.vt)
				spent := ghosttytest.GridResolutions() - before
				rows := departedDrain(t, term)
				departedAssertEqual(t, rows, numberedWant(lines))
				if spent != uint64(len(rows)) {
					t.Fatalf("captured %d rows with %d grid resolutions, want one per row", len(rows), spent)
				}
				if ending.name == "prune" {
					page, err := term.HistoryRows(0, 1)
					if err != nil || page.Total >= len(rows) {
						t.Fatalf("retention did not prune: total=%d, departed=%d, err=%v", page.Total, len(rows), err)
					}
				}
			})
		}
	}
}

func TestDepartedBulkCapturePreservesCellsBeforeErasure(t *testing.T) {
	const line = "\x1b[?2027h\x1b[48;2;10;20;30m\x1b[2K\x1b[0m" +
		"\x1b[1;2;3;4:3;5;7;8;9;53;38;2;90;64;200;48;5;42;58:2::4:5:6mB" +
		"\x1b[0me\u0301日👩\u200d💻"
	for _, ending := range []struct {
		name string
		vt   string
	}{
		{"scroll", ""},
		{"reset", "\x1bc"},
		{"erase-history", "\x1b[3J"},
		{"prune", ""},
	} {
		t.Run(ending.name, func(t *testing.T) {
			term := departedTerm(t, 20, 5)
			if ending.name == "prune" {
				departedRetention(t, term, 30)
			}
			departedFeed(t, term, line)
			want, err := term.Row(0)
			if err != nil {
				t.Fatal(err)
			}
			if want.Cells[0].Style.Attributes != emulator.AttrBold|emulator.AttrItalic|emulator.AttrFaint|emulator.AttrBlink|emulator.AttrInverse|emulator.AttrInvisible|emulator.AttrStrikethrough|emulator.AttrOverline || want.Cells[0].Style.Underline != emulator.UnderlineCurly {
				t.Fatalf("fixture lost its decorations: %+v", want.Cells[0])
			}
			if want.Cells[1].Grapheme != "e\u0301" || want.Cells[1].Style != (emulator.Style{}) || want.Cells[2].Width != emulator.WidthWide || want.Cells[3].Width != emulator.WidthSpacerTail || want.Cells[4].Grapheme != "👩\u200d💻" {
				t.Fatalf("fixture lost its default style or Unicode cells: %+v", want.Cells[:6])
			}
			if bg := want.Cells[19].Style.Background; bg.Kind != emulator.ColorRGB || bg.RGB != (emulator.RGB{R: 10, G: 20, B: 30}) {
				t.Fatalf("fixture lost erased background: %+v", bg)
			}
			const lines = 1000
			departedFeed(t, term, "\r\n"+strings.Repeat(line+"\r\n", lines-1)+ending.vt)
			rows := departedDrain(t, term)
			if len(rows) != lines-4 {
				t.Fatalf("captured %d rows, want %d", len(rows), lines-4)
			}
			for i, row := range rows {
				if !reflect.DeepEqual(row, want) {
					t.Fatalf("row %d differs from the active read: got %+v, want %+v", i, row, want)
				}
			}
			// A subsequent write cannot change any handed-out row or string.
			departedFeed(t, term, "\x1bc"+strings.Repeat("X\r\n", 20))
			if !reflect.DeepEqual(rows[0], want) {
				t.Fatal("later writes changed a captured row")
			}
		})
	}
}

func TestDepartedBulkCaptureRetriesWholeLongGraphemeRows(t *testing.T) {
	for _, ending := range []string{"", "\x1bc", "\x1b[3J"} {
		t.Run(fmt.Sprintf("ending-%q", ending), func(t *testing.T) {
			term := departedTerm(t, 1, 2)
			cluster := "e" + strings.Repeat("\u0301", 40)
			departedFeed(t, term, cluster)
			before := ghosttytest.GridResolutions()
			departedFeed(t, term, "\r\n\r\n"+ending)
			spent := ghosttytest.GridResolutions() - before
			rows := departedDrain(t, term)
			if len(rows) != 1 || rows[0].Cells[0].Grapheme != cluster {
				t.Fatalf("long cluster was lost or truncated: %+v", rows)
			}
			// 81 UTF-8 bytes exceed the initial 64-byte one-column budget;
			// the complete traversal retries once with 256 bytes.
			if spent != 2 {
				t.Fatalf("long row spent %d resolutions, want two complete reads", spent)
			}
		})
	}
}

func TestDepartedBulkCaptureStopsAtUnreadableRow(t *testing.T) {
	for _, erased := range []bool{false, true} {
		t.Run(fmt.Sprintf("erased-%t", erased), func(t *testing.T) {
			term := departedTerm(t, 10, 5)
			departedFeed(t, term, numbered(8))
			departedDrain(t, term)
			gt, ok := term.(*terminal)
			if !ok {
				t.Fatalf("departed terminal has type %T, want *terminal", term)
			}
			gt.mu.Lock()
			frontier := gt.departedFrontier
			before := ghosttytest.GridResolutions()
			// HISTORY coordinates include the live rectangle: eight printed
			// rows and the cursor's blank row are readable, row nine is not.
			if erased {
				gt.beginHistoryCaptureLocked()
				gt.historyCapture.baseRows = 0
				gt.captureHistoryErasedLocked(0, 11)
			} else {
				gt.captureDepartedLocked(0, 11)
			}
			spent := ghosttytest.GridResolutions() - before
			advanced := gt.departedFrontier - frontier
			gt.mu.Unlock()
			rows, err := term.DepartedRows()
			if err == nil || !strings.Contains(err.Error(), "row 9") {
				t.Fatalf("unreadable row did not report its boundary: %v", err)
			}
			departedAssertEqual(t, rows, append(numberedWant(12), departedRowWant{}))
			if advanced != 9 || spent != 10 {
				t.Fatalf("capture read past the hole: frontier +%d, resolutions %d", advanced, spent)
			}
		})
	}
}

func TestDepartedBulkCaptureHasNoPerCellGraphemeAllocation(t *testing.T) {
	term := departedTerm(t, 120, 5)
	departedFeed(t, term, strings.Repeat("A", 120)+strings.Repeat("\r\n", 5))
	gt, ok := term.(*terminal)
	if !ok {
		t.Fatalf("departed terminal has type %T, want *terminal", term)
	}
	gt.mu.Lock()
	defer gt.mu.Unlock()
	allocs := testing.AllocsPerRun(10, func() {
		row, err := gt.historyRow(0)
		if err != nil || row.Cells[119].Grapheme != "A" {
			t.Fatalf("full ASCII history row: %+v, %v", row, err)
		}
	})
	// Row facts, row-wide UTF-8 storage, output cells and C out parameters
	// are row-level allocations. A per-cell buffer exceeds this by an order
	// of magnitude even when every cell contains only a single codepoint.
	if allocs > 8 {
		t.Fatalf("one ASCII row allocated %.0f objects, want at most 8 row-level objects", allocs)
	}
}
