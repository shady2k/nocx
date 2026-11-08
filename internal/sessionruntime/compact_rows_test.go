package sessionruntime

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

func TestCompactRowsPreservesEncodedTerminalFacts(t *testing.T) {
	rt, _ := realScreenRuntime(t)
	// Include erased styled blanks, explicit spaces, combining/wide clusters,
	// an ordinary wrap, and a wide character at the right margin (spacer head).
	feed := "plain\r\n\x1b[48;2;12;34;56m\x1b[K\x1b[0m\r\n" +
		"\x1b[1;4:3;38;5;42m界e\u0301  \x1b[0m\r\n" +
		strings.Repeat("X", 81) + "\x1b[9;80H界"
	if err := rt.Ingest([]byte(feed)); err != nil {
		t.Fatal(err)
	}
	original := rt.Snapshot().Rows
	before, err := EncodeRows(original)
	if err != nil {
		t.Fatal(err)
	}
	compact := CompactRows(original)
	after, err := EncodeRows(compact)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("compaction changed encoded terminal facts: err=%v\nbefore=%s\nafter=%s", err, before, after)
	}
	if cap(compact[0].Cells) >= len(original[0].Cells) {
		t.Fatal("the shortened row still retains its full-width cell allocation")
	}
	for i, row := range original {
		if len(row.Cells) != 80 {
			t.Fatalf("compaction mutated source row %d: %d cells", i, len(row.Cells))
		}
	}
	// A full-width styled blank row is content, not removable padding.
	if len(compact[1].Cells) != 80 || &compact[1].Cells[0] != &original[1].Cells[0] {
		t.Fatal("the full-width styled blank row was trimmed or needlessly copied")
	}
	compact[0].Cells[0].Grapheme = "changed retained copy"
	sourceAfter, err := EncodeRows(original)
	if err != nil || !bytes.Equal(before, sourceAfter) {
		t.Fatal("editing a shortened retained row changed the runtime's source cells")
	}
}

func TestCompactRowsKeepsExplicitTrailingCellsAndUnchangedStorage(t *testing.T) {
	for _, cell := range []emulator.Cell{
		{Width: emulator.WidthNarrow, HasText: true, Grapheme: " "},
		{Width: emulator.WidthWide, HasText: true, Grapheme: "界"},
		{Width: emulator.WidthSpacerTail},
		{Width: emulator.WidthSpacerHead},
		{Width: emulator.WidthNarrow, Style: emulator.Style{Attributes: emulator.AttrBold}},
		{Width: emulator.WidthNarrow, Style: emulator.Style{Background: emulator.Color{Kind: emulator.ColorPalette, Palette: 3}}},
	} {
		source := []emulator.Row{{Cells: []emulator.Cell{cell}, Wrap: true, Continuation: true}}
		got := CompactRows(source)
		if &got[0] != &source[0] || &got[0].Cells[0] != &source[0].Cells[0] {
			t.Fatalf("an explicit trailing cell was trimmed or copied: %+v", cell)
		}
	}
	if CompactRows(nil) != nil {
		t.Fatal("nil closing rows became a present screen")
	}
	blank := []emulator.Row{{Cells: []emulator.Cell{{Width: emulator.WidthNarrow}}, Wrap: true}}
	got := CompactRows(blank)
	if len(got) != 1 || len(got[0].Cells) != 0 || cap(got[0].Cells) != 0 || !got[0].Wrap {
		t.Fatalf("an implicit blank row lost its identity or retained its allocation: %+v", got)
	}
}
