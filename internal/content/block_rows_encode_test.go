package content

// The stored vocabulary's guard rails (nocx-2v80t.3.7, nocx-zg3k3.2.12).
// Four tests, each covering one way two encoders drift apart, plus the
// size measurement the compaction task was done for:
//
//  1. the DTO conforms to the contract — what the store writes is what the
//     schema declares (AGENTS.md rule 5's Go half);
//  2. the row vocabulary is IDENTICAL to the live frame's — the schema
//     files carry inline copies (the dead-export ratchet's price for
//     cross-file refs), and the copies are canonical-JSON-equal;
//  3. the CROSS-ENCODER check: the same emulator rows through this store's
//     encoder and through sessionruntime's frame encoder produce the same
//     row object — one vocabulary, two halves, zero drift a test cannot
//     see;
//  4. the size a plain 18-character row stores at, before and after
//     nocx-zg3k3.2.12's shape.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

const blockRowsContractID = "https://nocx.local/contracts/ledger.blockRows.schema.json"

func loadBlockRowsSchema(t *testing.T, ref string) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "ledger.blockRows.schema.json"))
	if err != nil {
		t.Fatalf("read the contract: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse the contract: %v", err)
	}
	c := jsonschema.NewCompiler()
	if addErr := c.AddResource(blockRowsContractID, doc); addErr != nil {
		t.Fatalf("add the contract: %v", addErr)
	}
	s, err := c.Compile(blockRowsContractID + ref)
	if err != nil {
		t.Fatalf("compile the contract: %v", err)
	}
	return s
}

// aStyledRow exercises every field the vocabulary carries: narrow and wide
// clusters with their spacer, a cell whose grapheme is non-empty but
// HasText is false (the encoder must still read it as blank — the two are
// different facts, per emulator.Cell's own doc), a palette colour beside an
// RGB one, and two styles so the runs actually run.
func aStyledRow() emulator.Row {
	base := emulator.Style{}
	accent := emulator.Style{
		Foreground: emulator.Color{Kind: emulator.ColorPalette, Palette: 4},
		Background: emulator.Color{Kind: emulator.ColorRGB, RGB: emulator.RGB{R: 30, G: 30, B: 46}},
		Attributes: emulator.AttrBold | emulator.AttrItalic,
		Underline:  emulator.UnderlineSingle,
	}
	cells := []emulator.Cell{
		{Grapheme: "$", Width: emulator.WidthNarrow, HasText: true, Style: base},
		{Grapheme: " ", Width: emulator.WidthNarrow, HasText: false, Style: base},
		{Grapheme: "字", Width: emulator.WidthWide, HasText: true, Style: accent},
		{Width: emulator.WidthSpacerTail, Style: accent},
		{Grapheme: "ok", Width: emulator.WidthNarrow, HasText: true, Style: base},
	}
	return emulator.Row{Cells: cells, Wrap: true}
}

func TestBlockRowsLine_DTOConformsToContract(t *testing.T) {
	schema := loadBlockRowsSchema(t, "")

	line, err := encodeBlockRowsLine(7, aStyledRow())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var doc any
	if unmarshalErr := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &doc); unmarshalErr != nil {
		t.Fatalf("the store's own line must be JSON: %v", unmarshalErr)
	}
	if validateErr := schema.Validate(doc); validateErr != nil {
		t.Fatalf("the stored line does not conform to contracts/ledger.blockRows.schema.json: %v", validateErr)
	}

	// And the summary sidecar rides the same contract.
	summaryRaw, err := json.Marshal(blockRowsPayload{DroppedRows: 4, LostRows: 2})
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	var summaryDoc any
	if err := json.Unmarshal(summaryRaw, &summaryDoc); err != nil {
		t.Fatalf("summary json: %v", err)
	}
	summarySchema := loadBlockRowsSchema(t, "#/$defs/summary")
	if err := summarySchema.Validate(summaryDoc); err != nil {
		t.Fatalf("the summary sidecar does not conform: %v", err)
	}
}

// The inline copies of the frame vocabulary are IDENTICAL to
// session.frame.schema.json's — canonical-JSON equal, whitespace and key
// order aside. A copy that dropped a field or renamed a width would pass
// every store test and paint a stored block differently from the live
// screen it records.
func TestBlockRowsVocabulary_IsTheFrameVocabularyVerbatim(t *testing.T) {
	readDefs := func(name string) map[string]json.RawMessage {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", name)) //nolint:gosec // this package's own contracts directory, test-only path
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var doc struct {
			Defs map[string]json.RawMessage `json:"$defs"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return doc.Defs
	}
	frame := readDefs("session.frame.schema.json")
	rows := readDefs("ledger.blockRows.schema.json")

	// Canonical form: re-marshal through a decoded map, which sorts keys
	// and drops the files' differing whitespace. The definitions must be
	// EQUAL — prose included, because the prose is part of what a vocabulary
	// says.
	canonical := func(raw json.RawMessage) string {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("decode definition: %v", err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("encode definition: %v", err)
		}
		return string(out)
	}
	for _, def := range []string{"row", "mark", "run", "style", "color"} {
		f, ok := frame[def]
		if !ok {
			t.Fatalf("session.frame.schema.json carries no $defs/%s", def)
		}
		r, ok := rows[def]
		if !ok {
			t.Fatalf("ledger.blockRows.schema.json carries no $defs/%s", def)
		}
		if canonical(f) != canonical(r) {
			t.Fatalf("$defs/%s diverged between session.frame and ledger.blockRows — the stored form and the live frame are two vocabularies now, which is the defect the copy exists to prevent", def)
		}
	}
}

// THE CROSS-ENCODER CHECK. The same rows through the store's encoder and
// through the session runtime's frame encoder must produce the same row
// objects. This is the test that would fail the day either encoder grows a
// spelling the other does not have, and it is what lets two halves of one
// product carry one vocabulary without importing each other.
func TestBlockRowsEncoder_MatchesTheFrameEncoderOnTheSameRows(t *testing.T) {
	rows := []emulator.Row{aStyledRow(), aTextRowForEncoder("second physical line")}

	snap := sessionruntime.Snapshot{
		Revision: 1,
		Geometry: sessionruntime.GeometryCommit{
			Geometry: emulator.Geometry{Cols: 21, Rows: 2, CellWidthPx: 8, CellHeightPx: 16},
			Revision: 1,
		},
		Rows: rows,
	}
	frame, err := sessionruntime.EncodeFrame(snap)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	var decodedFrame struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(frame, &decodedFrame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if len(decodedFrame.Rows) != len(rows) {
		t.Fatalf("frame carried %d rows, want %d", len(decodedFrame.Rows), len(rows))
	}

	canonical := func(raw []byte) string {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return string(out)
	}
	for i, row := range rows {
		line, err := encodeBlockRowsLine(uint64(i), row) //nolint:gosec // a row index, not a byte count
		if err != nil {
			t.Fatalf("encode row %d: %v", i, err)
		}
		var decodedLine struct {
			Row json.RawMessage `json:"row"`
		}
		if err := json.Unmarshal(line, &decodedLine); err != nil {
			t.Fatalf("decode line %d: %v", i, err)
		}
		if canonical(decodedLine.Row) != canonical(decodedFrame.Rows[i]) {
			t.Fatalf("row %d encodes differently in the store and in the frame:\nstore: %s\nframe: %s",
				i, decodedLine.Row, decodedFrame.Rows[i])
		}
	}
}

// Stored rows are sparse at the right edge: a short line does not pay for the
// untouched terminal rectangle, but a styled blank remains because it paints.
func TestBlockRowsEncoder_TrimsDefaultTrailingCellsButKeepsStyledBlank(t *testing.T) {
	row := aTextRowForEncoder("hello")
	plainTail := make([]emulator.Cell, 145)
	for i := range plainTail {
		plainTail[i].Width = emulator.WidthNarrow
	}
	row.Cells = append(row.Cells, plainTail...)
	line, err := encodeBlockRowsLine(0, row)
	if err != nil {
		t.Fatalf("encode plain tail: %v", err)
	}
	var parsed blockRowsLine
	if decodeErr := json.Unmarshal(line, &parsed); decodeErr != nil {
		t.Fatalf("decode plain tail: %v", decodeErr)
	}
	if got := parsed.Row.Text; got != "hello" {
		t.Fatalf("plain row text = %q, want %q", got, "hello")
	}
	if len(parsed.Row.Runs) != 0 {
		t.Fatalf("plain row carries runs = %v, want omitted (implicit default)", parsed.Row.Runs)
	}

	styled := emulator.Cell{
		Width: emulator.WidthNarrow,
		Style: emulator.Style{
			Background: emulator.Color{
				Kind: emulator.ColorRGB,
				RGB:  emulator.RGB{R: 1, G: 2, B: 3},
			},
		},
	}
	styledTail := make([]emulator.Cell, 144)
	for i := range styledTail {
		styledTail[i].Width = emulator.WidthNarrow
	}
	row.Cells = append(aTextRowForEncoder("hello").Cells, styledTail...)
	row.Cells = append(row.Cells, styled)
	line, err = encodeBlockRowsLine(0, row)
	if err != nil {
		t.Fatalf("encode styled tail: %v", err)
	}
	parsed = blockRowsLine{}
	if decodeErr := json.Unmarshal(line, &parsed); decodeErr != nil {
		t.Fatalf("decode styled tail: %v", decodeErr)
	}
	// "hello" (5 positions with text) + 144 default-narrow blanks + 1 styled
	// blank = 150 positions; a blank contributes nothing to text (codepoints
	// 0), so every one of the 145 blanks is its own mark.
	if got := parsed.Row.Text; got != "hello" {
		t.Fatalf("styled row text = %q, want %q (blanks contribute no text)", got, "hello")
	}
	if got := len(parsed.Row.Marks); got != 145 {
		t.Fatalf("styled row marks = %d entries, want 145 (one per blank position)", got)
	}
	if len(parsed.Row.Runs) != 2 {
		t.Fatalf("styled row runs = %v, want 2 (the default stretch, then the styled cell)", parsed.Row.Runs)
	}
	if got := parsed.Row.Runs[len(parsed.Row.Runs)-1][1]; got != float64(1) {
		t.Fatalf("styled trailing run length = %v, want 1", got)
	}

	var styledWire struct {
		Row struct {
			Runs [][2]json.RawMessage `json:"runs"`
		} `json:"row"`
	}
	if decodeErr := json.Unmarshal(line, &styledWire); decodeErr != nil {
		t.Fatalf("decode styled runs: %v", decodeErr)
	}
	lastStyle := styledWire.Row.Runs[len(styledWire.Row.Runs)-1][0]
	var tuple [5]int
	if decodeErr := json.Unmarshal(lastStyle, &tuple); decodeErr != nil {
		t.Fatalf("styled trailing run style is not a 5-tuple: %v (%s)", decodeErr, lastStyle)
	}
	wantBackground := 257 + (1<<16 | 2<<8 | 3)
	if tuple[1] != wantBackground {
		t.Fatalf("styled trailing cell background packed = %v, want RGB(1,2,3) packed as %d", tuple[1], wantBackground)
	}

	blank := emulator.Row{Cells: make([]emulator.Cell, 150)}
	for i := range blank.Cells {
		blank.Cells[i].Width = emulator.WidthNarrow
	}
	line, err = encodeBlockRowsLine(0, blank)
	if err != nil {
		t.Fatalf("encode all-blank row: %v", err)
	}
	parsed = blockRowsLine{}
	if decodeErr := json.Unmarshal(line, &parsed); decodeErr != nil {
		t.Fatalf("decode all-blank row: %v", decodeErr)
	}
	if parsed.Row.Text != "" {
		t.Fatalf("all-blank row text = %q, want empty", parsed.Row.Text)
	}
	if len(parsed.Row.Runs) != 0 || len(parsed.Row.Marks) != 0 {
		t.Fatalf("all-blank row carries runs=%v marks=%v, want both omitted", parsed.Row.Runs, parsed.Row.Marks)
	}

	line, err = encodeBlockRowsLine(0, emulator.Row{})
	if err != nil {
		t.Fatalf("encode empty row: %v", err)
	}
	parsed = blockRowsLine{}
	if decodeErr := json.Unmarshal(line, &parsed); decodeErr != nil {
		t.Fatalf("decode empty row: %v", decodeErr)
	}
	if parsed.Row.Text != "" {
		t.Fatalf("empty row text = %q, want empty", parsed.Row.Text)
	}
}

func aTextRowForEncoder(text string) emulator.Row {
	row := emulator.Row{Cells: make([]emulator.Cell, 0, len(text))}
	for _, r := range text {
		row.Cells = append(row.Cells, emulator.Cell{
			Grapheme: string(r), Width: emulator.WidthNarrow, HasText: true,
		})
	}
	return row
}

// The stored lines are newline-terminated and parse independently — the
// property that makes a chunked, evicted body still readable line by line.
func TestBlockRowsLine_EveryLineParsesAlone(t *testing.T) {
	var body bytes.Buffer
	for i := 0; i < 3; i++ {
		line, err := encodeBlockRowsLine(uint64(i), aTextRowForEncoder(strings.Repeat("x", i+1))) //nolint:gosec // a row index, not a byte count
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		body.Write(line)
	}
	lines := strings.Split(strings.TrimSuffix(body.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("body holds %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		var parsed blockRowsLine
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("line %d does not parse alone: %v", i, err)
		}
		if parsed.From != uint64(i) { //nolint:gosec // a row index, not a byte count
			t.Fatalf("line %d carries from=%d", i, parsed.From)
		}
	}
}

// TestBlockRowsLine_PlainRowStoresUnderThreeTimesItsText is the measured
// acceptance bound (nocx-zg3k3.2.12): an 18-character row with no styling
// and no wide or combining cluster stores at under 3x its own text length,
// where the PRIOR shape (a [grapheme, width, hasText] tuple per column plus
// a named-field style object) measured 542 B for the same row — about 30x.
func TestBlockRowsLine_PlainRowStoresUnderThreeTimesItsText(t *testing.T) {
	const text = "18 characters here"
	if len(text) != 18 {
		t.Fatalf("test fixture text is %d bytes, want 18", len(text))
	}
	line, err := encodeBlockRowsLine(0, aTextRowForEncoder(text))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	stored := len(bytes.TrimSuffix(line, []byte{'\n'}))
	t.Logf("18-character plain row: %d bytes stored (%.2fx its text; the pre-nocx-zg3k3.2.12 shape measured 542 B, ~30x)",
		stored, float64(stored)/float64(len(text)))
	if stored >= 3*len(text) {
		t.Errorf("stored %d bytes for an 18-character plain row, want under %d (3x its text)", stored, 3*len(text))
	}
}
