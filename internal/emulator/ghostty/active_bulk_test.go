package ghostty

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty/ghosttytest"
)

// Scalar Cell is deliberately independent of Row's bulk extraction.
func activeRowsMatchCells(t *testing.T, term emulator.Terminal, cols, rows int) []emulator.Row {
	t.Helper()
	out := make([]emulator.Row, rows)
	for y := range rows {
		row, err := term.Row(y)
		if err != nil {
			t.Fatalf("row %d: %v", y, err)
		}
		if len(row.Cells) != cols {
			t.Fatalf("row %d has %d cells, want %d", y, len(row.Cells), cols)
		}
		for x := range cols {
			cell, err := term.Cell(x, y)
			if err != nil || cell != row.Cells[x] {
				t.Fatalf("cell %d,%d: bulk=%+v scalar=%+v err=%v", x, y, row.Cells[x], cell, err)
			}
		}
		out[y] = row
	}
	return out
}

func TestActiveBulkRowFidelityAndOwnership(t *testing.T) {
	fixture := "\x1b[?2027h\x1b[48;2;10;20;30m\x1b[2K\x1b[0m" +
		"\x1b[1;2;3;4:3;5;7;8;9;53;38;2;90;64;200;48;5;42;58:2::4:5:6mB" +
		"\x1b[0me\u0301日👩\u200d💻\x1b[38;5;201;48;2;1;2;3;58;5;17mP\x1b[0m\r\n" + strings.Repeat("a", 19) + "日"
	for _, alternate := range []bool{false, true} {
		t.Run(fmt.Sprintf("alternate-%t", alternate), func(t *testing.T) {
			term := departedTerm(t, 20, 5)
			if alternate {
				departedFeed(t, term, "\x1b[?1049h")
			}
			departedFeed(t, term, fixture)
			retained := activeRowsMatchCells(t, term, 20, 5)
			want := make([]emulator.Row, len(retained))
			for y, row := range retained {
				want[y] = row
				want[y].Cells = append([]emulator.Cell(nil), row.Cells...)
				for x := range row.Cells {
					want[y].Cells[x].Grapheme = strings.Clone(row.Cells[x].Grapheme)
				}
			}
			first := retained[0].Cells[0]
			if first.Style.Attributes != emulator.AttrBold|emulator.AttrItalic|emulator.AttrFaint|emulator.AttrBlink|emulator.AttrInverse|emulator.AttrInvisible|emulator.AttrStrikethrough|emulator.AttrOverline || first.Style.Underline != emulator.UnderlineCurly {
				t.Fatalf("fixture lost decorations: %+v", first)
			}
			if retained[0].Cells[1].Grapheme != "e\u0301" || retained[0].Cells[1].Style != (emulator.Style{}) || retained[0].Cells[4].Grapheme != "👩\u200d💻" {
				t.Fatalf("fixture lost default style or clusters: %+v", retained[0].Cells[:6])
			}
			palette := retained[0].Cells[6].Style
			if palette.Foreground != (emulator.Color{Kind: emulator.ColorPalette, Palette: 201}) || palette.Background != (emulator.Color{Kind: emulator.ColorRGB, RGB: emulator.RGB{R: 1, G: 2, B: 3}}) || palette.UnderlineColor != (emulator.Color{Kind: emulator.ColorPalette, Palette: 17}) {
				t.Fatalf("fixture lost palette/RGB colors: %+v", palette)
			}
			if bg := retained[0].Cells[19].Style.Background; bg.Kind != emulator.ColorRGB || bg.RGB != (emulator.RGB{R: 10, G: 20, B: 30}) {
				t.Fatalf("fixture lost erased background: %+v", bg)
			}
			if retained[1].Cells[19].Width != emulator.WidthSpacerHead || !retained[1].Wrap || !retained[2].Continuation || retained[2].Cells[1].Width != emulator.WidthSpacerTail {
				t.Fatalf("fixture lost wide wrap: %+v / %+v", retained[1], retained[2])
			}
			if _, err := term.Resize(emulator.Geometry{Cols: 13, Rows: 5, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
				t.Fatal(err)
			}
			activeRowsMatchCells(t, term, 13, 5)
			departedFeed(t, term, "\x1b[2J\x1b[Hchanged\x1b[?1049l")
			activeRowsMatchCells(t, term, 13, 5)
			if !reflect.DeepEqual(retained, want) {
				t.Fatal("write, resize or buffer switch mutated retained cells or strings")
			}
			// Returned slices belong to the reader, not to another Row result.
			retained[0].Cells[0].Grapheme = "reader mutation"
			activeRowsMatchCells(t, term, 13, 5)
		})
	}
}

func TestActiveBulkRowCompleteExtremeGrapheme(t *testing.T) {
	// The pinned emulator (574f5b2b, src/terminal/page.zig) retains at most
	// 64 suffix codepoints. Extraction must return all of that stored cluster,
	// not promise that ingestion retained arbitrarily many input marks.
	for _, combining := range []int{40, 64, 65, 1024, 32768} {
		for _, alternate := range []bool{false, true} {
			t.Run(fmt.Sprintf("combining-%d/alternate-%t", combining, alternate), func(t *testing.T) {
				term := departedTerm(t, 1, 2)
				if alternate {
					departedFeed(t, term, "\x1b[?1049h")
				}
				input := "e" + strings.Repeat("\u0301", combining)
				cluster := "e" + strings.Repeat("\u0301", min(combining, 64))
				departedFeed(t, term, input)
				rows := activeRowsMatchCells(t, term, 1, 2)
				if rows[0].Cells[0].Grapheme != cluster {
					t.Fatalf("cluster has %d bytes, want %d", len(rows[0].Cells[0].Grapheme), len(cluster))
				}
				if !alternate {
					track, err := term.TrackRow(0)
					if err != nil {
						t.Fatal(err)
					}
					defer track.Release()
					tracked, err := track.Row()
					if err != nil || !reflect.DeepEqual(tracked, rows[0]) {
						t.Fatalf("tracked complete cluster: %+v, %v", tracked, err)
					}
					departedFeed(t, term, "\x1b[?1049h")
					if hidden, hiddenErr := track.Row(); !errors.Is(hiddenErr, emulator.ErrUnsupported) || len(hidden.Cells) != 0 {
						t.Fatalf("tracked primary row on alternate screen: %+v, %v", hidden, hiddenErr)
					}
					departedFeed(t, term, "\x1b[?1049l")
					if tracked, err = track.Row(); err != nil || !reflect.DeepEqual(tracked, rows[0]) {
						t.Fatalf("tracked row after primary restore: %+v, %v", tracked, err)
					}
				}
				departedFeed(t, term, "\rX")
				activeRowsMatchCells(t, term, 1, 2)
				if rows[0].Cells[0].Grapheme != cluster {
					t.Fatal("later write changed retained extreme cluster")
				}
			})
		}
	}
}

func TestActiveBulkRowLongGraphemeDeparture(t *testing.T) {
	// Four-byte combining marks make a reachable 257-byte cluster: larger
	// than the initial 64-byte row budget and history's first 256-byte retry.
	// No artificial emulator state is needed to exercise both retry policies.
	cluster := "e" + strings.Repeat("\U0001D185", 64)
	for _, alternate := range []bool{false, true} {
		t.Run(fmt.Sprintf("alternate-%t", alternate), func(t *testing.T) {
			term := departedTerm(t, 1, 2)
			if alternate {
				departedFeed(t, term, "\x1b[?1049h")
			}
			departedFeed(t, term, cluster)
			rows := activeRowsMatchCells(t, term, 1, 2)
			if rows[0].Cells[0].Grapheme != cluster {
				t.Fatalf("stored long cluster has %d bytes, want %d", len(rows[0].Cells[0].Grapheme), len(cluster))
			}
			if alternate {
				departedFeed(t, term, "\rX")
			} else {
				departedFeed(t, term, "\r\n\r\n")
				departed := departedDrain(t, term)
				if len(departed) != 1 || !reflect.DeepEqual(departed[0], rows[0]) {
					t.Fatalf("long-cluster departure differs from scalar-checked row: %+v", departed)
				}
			}
			if rows[0].Cells[0].Grapheme != cluster {
				t.Fatal("later mutation changed retained long cluster")
			}
		})
	}
}

func TestActiveBulkRowRetriesPreserveNeighborsAndHistory(t *testing.T) {
	// Three distinct 257-byte clusters plus four ordinary cells exceed the
	// eight-column initial budget (512 bytes). Prefix, separators and suffix
	// check every offset after retry, including cells after the first overflow.
	marks := strings.Repeat("\U0001D185", 64)
	graphemes := []string{"P", "a" + marks, "M", "b" + marks, "N", "c" + marks, "Q", ""}
	term := departedTerm(t, 8, 2)
	departedFeed(t, term, "\x1b[48;2;10;20;30m\x1b[2K\x1b[0m"+
		"P\x1b[38;2;40;50;60m"+graphemes[1]+"\x1b[0mM"+graphemes[3]+"N"+graphemes[5]+"Q")
	// Seven one-column graphemes fit without wrapping. Expected cells come
	// only from the scalar API, never from another consumer of bulk traversal.
	want := emulator.Row{Cells: make([]emulator.Cell, 8)}
	for x := range want.Cells {
		cell, err := term.Cell(x, 0)
		if err != nil {
			t.Fatal(err)
		}
		if cell.Grapheme != graphemes[x] {
			t.Fatalf("fixture cell %d has %d bytes, want %d", x, len(cell.Grapheme), len(graphemes[x]))
		}
		want.Cells[x] = cell
	}
	active, err := term.Row(0)
	if err != nil || !reflect.DeepEqual(active, want) {
		t.Fatalf("active retry differs from scalar row: %+v, %v", active, err)
	}
	departedFeed(t, term, "\r\n\r\n")
	departed := departedDrain(t, term)
	if len(departed) != 1 || !reflect.DeepEqual(departed[0], want) {
		t.Fatalf("departed retry differs from scalar row: %+v", departed)
	}
	page := histPage(t, term, 0, 1)
	if page.Total != 1 || len(page.Rows) != 1 || !reflect.DeepEqual(page.Rows[0], want) {
		t.Fatalf("history retry differs from scalar row: %+v", page)
	}
	departedFeed(t, term, "\x1b[2J\x1b[Hoverwrite")
	if _, err := term.Resize(emulator.Geometry{Cols: 6, Rows: 3, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []emulator.Row{active, departed[0], page.Rows[0]} {
		if !reflect.DeepEqual(row, want) {
			t.Fatal("later write or resize changed a retained long row")
		}
	}
}

func TestActiveBulkRowResolvesOnce(t *testing.T) {
	for _, cols := range []int{10, 80, 120} {
		t.Run(fmt.Sprint(cols), func(t *testing.T) {
			term := departedTerm(t, cols, 2)
			departedFeed(t, term, strings.Repeat("A", cols))
			before := ghosttytest.GridResolutions()
			if _, err := term.Row(0); err != nil {
				t.Fatal(err)
			}
			if spent := ghosttytest.GridResolutions() - before; spent != 1 {
				t.Fatalf("active row spent %d grid resolutions, want 1", spent)
			}
		})
	}
}

func TestActiveBulkRowUsesBoundedCgoCalls(t *testing.T) {
	term := departedTerm(t, 120, 2)
	departedFeed(t, term, strings.Repeat("A", 120))
	before := runtime.NumCgoCall()
	if _, err := term.Row(0); err != nil {
		t.Fatal(err)
	}
	calls := runtime.NumCgoCall() - before
	if calls > 2 {
		t.Fatalf("active row took %d cgo calls, want at most 2 independent of columns", calls)
	}
}

func TestActiveBulkRowScratchReuseKeepsReturnedRowsOwned(t *testing.T) {
	term := departedTerm(t, 40, 3)
	var retained, expected []emulator.Row
	for _, cols := range []int{40, 5, 80, 3} {
		if _, err := term.Resize(emulator.Geometry{Cols: cols, Rows: 3, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
			t.Fatal(err)
		}
		departedFeed(t, term, "\x1b[0m\x1b[2J\x1b[H\x1b[38;2;40;50;60mA\u0301\x1b[0mB")
		want := emulator.Row{Cells: make([]emulator.Cell, cols)}
		for x := range want.Cells {
			cell, err := term.Cell(x, 0)
			if err != nil {
				t.Fatal(err)
			}
			want.Cells[x] = cell
		}
		row, err := term.Row(0)
		if err != nil {
			t.Fatal(err)
		}
		retained = append(retained, row)
		expected = append(expected, want)
		// A blank row must clear old facts, and a different text row must
		// overwrite the reusable UTF-8 buffer without changing prior results.
		departedFeed(t, term, "\x1b[2;1HXY")
		activeRowsMatchCells(t, term, cols, 3)
		track, err := term.TrackRow(0)
		if err != nil {
			t.Fatal(err)
		}
		tracked, err := track.Row()
		track.Release()
		if err != nil || !reflect.DeepEqual(tracked, want) {
			t.Fatalf("tracked read after scratch reuse: %+v, %v", tracked, err)
		}
		retained = append(retained, tracked)
		expected = append(expected, want)
		readerCopy, err := term.Row(0)
		if err != nil {
			t.Fatal(err)
		}
		readerCopy.Cells[0].Grapheme = "reader mutation"
		readerCopy.Cells[0].Style = emulator.Style{}
		activeRowsMatchCells(t, term, cols, 3)
	}
	term.Close()
	for i, row := range retained {
		if !reflect.DeepEqual(row, expected[i]) {
			t.Fatalf("read, reader mutation, resize or close changed retained row %d", i)
		}
	}
}

func TestBulkRowPolicyIndependentOfCoordinates(t *testing.T) {
	port := departedTerm(t, 1, 2)
	cluster := "e" + strings.Repeat("\U0001D185", 64)
	departedFeed(t, port, cluster+"\r\n\r\n")
	term, ok := port.(*terminal)
	if !ok {
		t.Fatal("expected ghostty terminal")
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	// The same history coordinates accept either complete clusters or the
	// caller's small cluster ceiling, independently of the coordinate tag.
	row, err := term.rowAt(pointHistory, 0, rowReadPolicy{op: "row"})
	if err != nil || row.Cells[0].Grapheme != cluster {
		t.Fatalf("complete history-coordinate read: %+v, %v", row, err)
	}
	if _, err := term.rowAt(pointHistory, 0, rowReadPolicy{
		op: "bounded", maxClusterBytes: 32, maxRowBytes: 64,
	}); !errors.Is(err, emulator.ErrExhausted) {
		t.Fatalf("caller cluster ceiling: %v", err)
	}
}

func TestActiveBulkRowErrors(t *testing.T) {
	term := departedTerm(t, 1, 2)
	for _, y := range []int{-1, 2, int(^uint(0) >> 1)} {
		if row, err := term.Row(y); !errors.Is(err, emulator.ErrOutOfRange) || len(row.Cells) != 0 {
			t.Fatalf("row %d: %+v, %v", y, row, err)
		}
	}
	term.Close()
	if row, err := term.Row(0); !errors.Is(err, emulator.ErrClosed) || len(row.Cells) != 0 {
		t.Fatalf("closed row: %+v, %v", row, err)
	}
}

// Each operation reads one full 40-row active grid. CGO calls include only
// extraction, not setup; scalar Cell provides the unchanged comparison path.
func BenchmarkActiveBulkGrid(b *testing.B) {
	for _, cols := range []int{79, 120} {
		for _, scalar := range []bool{false, true} {
			b.Run(fmt.Sprintf("%dx40/scalar-%t", cols, scalar), func(b *testing.B) {
				term, err := New(emulator.Geometry{Cols: cols, Rows: 40, CellWidthPx: 10, CellHeightPx: 20})
				if err != nil {
					b.Fatal(err)
				}
				defer term.Close()
				if _, err := term.Ingest([]byte(strings.Repeat(strings.Repeat("A", cols)+"\r\n", 39) + strings.Repeat("A", cols))); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				calls := runtime.NumCgoCall()
				for range b.N {
					for y := range 40 {
						if scalar {
							for x := range cols {
								if _, err := term.Cell(x, y); err != nil {
									b.Fatal(err)
								}
							}
						} else if _, err := term.Row(y); err != nil {
							b.Fatal(err)
						}
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(runtime.NumCgoCall()-calls)/float64(b.N), "cgo/grid")
			})
		}
	}
}
