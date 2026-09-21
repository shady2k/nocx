package captureview

// The views are functions of the stored record (nocx-2v80t.2.5), and the
// SGR dialect they emit is sgr.ts's — the golden cases below pin both, so a
// restored card draws from records exactly what it drew from renderer
// freezes, and a changed record moves both views.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func cell(text string, width int, attrs *sgrAttrsBuilt) proto.CaptureCell {
	c := proto.CaptureCell{Text: text, Width: width, HasText: text != ""}
	if attrs != nil {
		c.Fg, c.Bg, c.Attrs, c.Underline = attrs.fg, attrs.bg, attrs.attrs, attrs.underline
	}
	return c
}

// sgrAttrsBuilt is a test-side spelling of a record cell's style, in the
// proto's own vocabulary (the Attrs bitset and the colour kinds).
type sgrAttrsBuilt struct {
	fg, bg    *proto.CaptureColor
	attrs     int
	underline int
}

func palette(idx int) *proto.CaptureColor { return &proto.CaptureColor{Kind: "palette", Palette: idx} }
func rgb(hex string) *proto.CaptureColor  { return &proto.CaptureColor{Kind: "rgb", RGB: hex} }
func plainRow(cells ...proto.CaptureCell) proto.CaptureRow {
	return proto.CaptureRow{Cells: cells}
}

func TestVTBodyRendersStyledRunsInTheRendererDialect(t *testing.T) {
	rec := &proto.CaptureParams{
		Closing: proto.CaptureScreen{Lines: []proto.CaptureRow{
			plainRow(
				cell("ok", 2, &sgrAttrsBuilt{fg: palette(1)}),
				cell(" ", 1, nil),
				cell("!", 1, nil),
			),
		}},
	}
	// Red (palette 1 → 31); the default cells change only the colour, and
	// the dialect writes the bare default (39) for that — a reset is for a
	// flag going off. The row ends default, so it wears nothing forward.
	want := "\x1b[31mok\x1b[39m !"
	if got := VTBody(rec); got != want {
		t.Fatalf("VTBody = %q, want %q", got, want)
	}
}

func TestVTBodyWritesColorsTheWaySGRDoes(t *testing.T) {
	rec := &proto.CaptureParams{
		Closing: proto.CaptureScreen{Lines: []proto.CaptureRow{
			plainRow(
				cell("a", 1, &sgrAttrsBuilt{fg: palette(3)}),     // 33
				cell("b", 1, &sgrAttrsBuilt{fg: palette(11)}),    // 93 bright
				cell("c", 1, &sgrAttrsBuilt{fg: palette(200)}),   // 38;5;200
				cell("d", 1, &sgrAttrsBuilt{bg: rgb("#ff8000")}), // 48;2;255;128;0
				cell("e", 1, &sgrAttrsBuilt{fg: rgb("#102030")}), // 38;2;16;32;48
			),
		}},
	}
	// Each colour-only transition is a bare colour parameter — including a
	// colour going OFF (39/49) — and the row closes the state it opened.
	want := "\x1b[33ma\x1b[93mb\x1b[38;5;200mc\x1b[39;48;2;255;128;0md\x1b[38;2;16;32;48;49me\x1b[0m"
	if got := VTBody(rec); got != want {
		t.Fatalf("VTBody = %q, want %q", got, want)
	}
}

func TestVTBodyFlagsOrderAndResetAndReopen(t *testing.T) {
	// bold+italic on, italic off alone: reset-and-reopen, flags in the
	// dialect's order, colours restated after a reset.
	rec := &proto.CaptureParams{
		Closing: proto.CaptureScreen{Lines: []proto.CaptureRow{
			plainRow(
				cell("x", 1, &sgrAttrsBuilt{attrs: 1 | 2, fg: palette(4)}),
				cell("y", 1, &sgrAttrsBuilt{attrs: 1, fg: palette(4)}),
			),
		}},
	}
	want := "\x1b[1;3;34mx\x1b[0;1;34my\x1b[0m"
	if got := VTBody(rec); got != want {
		t.Fatalf("VTBody = %q, want %q", got, want)
	}
}

func TestVTBodyDropsTrailingBlanksAndZeroWidthCells(t *testing.T) {
	rec := &proto.CaptureParams{
		Closing: proto.CaptureScreen{Lines: []proto.CaptureRow{{
			Cells: []proto.CaptureCell{
				{Text: "h", Width: 1, HasText: true},
				{Text: "i", Width: 1, HasText: true},
				{Text: "", Width: 0},                 // wide-char continuation
				{Text: " ", Width: 1, HasText: true}, // padding
				{Text: "", Width: 0},                 // spacer
			},
		}}},
	}
	if got := VTBody(rec); got != "hi" {
		t.Fatalf("VTBody = %q, want %q", got, "hi")
	}
}

func TestVTBodyJoinsRowsWithNewlines(t *testing.T) {
	rec := &proto.CaptureParams{
		Departed: []proto.CaptureRow{plainRow(cell("one", 3, nil))},
		Closing: proto.CaptureScreen{Lines: []proto.CaptureRow{
			plainRow(cell("two", 3, nil)),
			plainRow(cell("three", 5, nil)),
		}},
	}
	want := "one\ntwo\nthree"
	if got := VTBody(rec); got != want {
		t.Fatalf("VTBody = %q, want %q", got, want)
	}
}

func TestVTBodyOfAnEmptyRecordIsEmpty(t *testing.T) {
	if got := VTBody(&proto.CaptureParams{}); got != "" {
		t.Fatalf("VTBody of an empty record = %q, want empty", got)
	}
}

func TestTextBodyJoinsWrappedRowsAndBreaksAtHardNewlines(t *testing.T) {
	rec := &proto.CaptureParams{
		Departed: []proto.CaptureRow{
			{Wrap: true, Cells: []proto.CaptureCell{cell("aa", 2, nil)}},
			{Wrap: true, Continuation: true, Cells: []proto.CaptureCell{cell("bb", 2, nil)}},
			{Continuation: true, Cells: []proto.CaptureCell{cell("cc", 2, nil)}},
			plainRow(cell("hard", 4, nil)),
		},
		Closing: proto.CaptureScreen{Lines: []proto.CaptureRow{
			plainRow(cell("last", 4, nil)),
		}},
	}
	want := "aabbcc\nhard\nlast"
	if got := TextBody(rec); got != want {
		t.Fatalf("TextBody = %q, want %q", got, want)
	}
}

func TestTextBodyCoversRowsThatScrolledAway(t *testing.T) {
	const needle = "EACCESQUX"
	rec := &proto.CaptureParams{
		Departed: []proto.CaptureRow{plainRow(cell("word: "+needle, 15, nil))},
		Closing: proto.CaptureScreen{Lines: []proto.CaptureRow{
			plainRow(cell("nothing here", 12, nil)),
		}},
	}
	if got := TextBody(rec); !strings.Contains(got, needle) {
		t.Fatalf("TextBody = %q, want it to hold the scrolled-away %q", got, needle)
	}
	if got := VTBody(rec); !strings.Contains(got, needle) {
		t.Fatalf("VTBody = %q, want it to hold the scrolled-away %q", got, needle)
	}
}

func TestViewIDIsDeterministicAndPerKind(t *testing.T) {
	a := ViewID("0198f2b0-aaaa-7000-8000-000000000000", KindVT)
	if a != ViewID("0198f2b0-aaaa-7000-8000-000000000000", KindVT) {
		t.Fatal("ViewID is not deterministic for the same record and kind")
	}
	if a == ViewID("0198f2b0-aaaa-7000-8000-000000000000", KindText) {
		t.Fatal("the two views of one record answer at one id")
	}
	if a == ViewID("0198f2b0-bbbb-7000-8000-000000000000", KindVT) {
		t.Fatal("two records answer at one view id")
	}
	// The artifact-id vocabulary's UUID shape: version 4, RFC 9562 variant.
	if len(a) != 36 || a[14] != '4' || (a[19] != '8' && a[19] != '9' && a[19] != 'a' && a[19] != 'b') {
		t.Fatalf("ViewID = %q, want the v4 UUID shape", a)
	}
}

func TestParseRecordDecodesTheStoredBytes(t *testing.T) {
	raw := `{"state":"settled","closing":{"cols":3,"rows":1,"lines":[{"cells":[{"text":"hi","width":2,"hasText":true}]}]}}`
	rec, err := ParseRecord([]byte(raw))
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if rec.State != proto.CaptureSettled || len(rec.Closing.Lines) != 1 {
		t.Fatalf("ParseRecord decoded %+v", rec)
	}
}
