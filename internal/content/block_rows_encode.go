package content

// The stored row vocabulary (nocx-2v80t.3.7, brought to its final compact
// form by nocx-zg3k3.2.12): one JSON Lines line per row of a streamed
// block, in the SAME cell vocabulary the live screen frame declares
// (contracts/session.frame.schema.json, ADR-0072's one record for the cell
// model). A row rides as its TEXT — the clusters in column order, a wide
// cluster's spacer not repeated — a sparse list of the positions whose
// codepoint count or column width departs from the default (one codepoint,
// one column), and style runs measured in columns; a trailing run of
// ordinary default blank cells is omitted rather than sent, since a stored
// row carries no frame geometry to pad against and the readers (BlockRowsTextAfter,
// frontend/src/scrollback/block-rows.ts) treat a shorter row as ending
// there.
//
// The Go shapes are declared HERE rather than borrowed from
// internal/sessionruntime's frame encoder for an ownership reason, not a
// vocabulary one: the session runtime is the helper's half and this package
// is the store's, and the ONE vocabulary is enforced where it is checkable —
// the schema, the row shape's own test against it, and the cross-encoder
// test that feeds the same emulator rows to both encoders and requires the
// same bytes. Two encoders, one contract, zero drift that a test cannot see.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/emulator"
)

// blockRowsLine is one line of the stored body: the row and the absolute
// index it departed at. The index rides the line, not a side table, for the
// same reason the row is self-describing — a chunk the cap later evicted
// takes its index with it, and what remains still names where it sits.
type blockRowsLine struct {
	From uint64   `json:"from"`
	Row  blockRow `json:"row"`
}

// blockRow is one physical line of the screen, in the text+marks+runs shape
// contracts/ledger.blockRows.schema.json declares. Marks and runs are
// omitted (nil, dropped by omitempty) when the row needs neither.
//
// RUNS IS PRE-ENCODED JSON, exactly as sessionruntime's frameRow carries it
// (nocx-4y82c.1.5): the shape it used to hold — [][2]any, two interface
// values per run — costs two extra allocations per style run under Go 1.27's
// encoding/json/v2, which makes each interface value addressable before
// marshalling it. This encoder is the frame encoder's declared mirror, so it
// takes the same shape; TestBlockRowsEncoder_MatchesTheFrameEncoderOnTheSameRows
// is what keeps the two byte-identical.
type blockRow struct {
	Text         string          `json:"text"`
	Marks        [][3]int        `json:"marks,omitempty"`
	Runs         json.RawMessage `json:"runs,omitempty"`
	Wrap         bool            `json:"wrap,omitempty"`
	Continuation bool            `json:"continuation,omitempty"`
}

// encodeBlockRowsLine renders one departed row as one stored line, newline
// terminated. The row shape and the trimming rule are IDENTICAL to
// sessionruntime.EncodeRows' — this function is that one's mirror, checked
// against it by TestBlockRowsEncoder_MatchesTheFrameEncoderOnTheSameRows —
// so a stored block decodes with the same painter the live screen does.
func encodeBlockRowsLine(from uint64, row emulator.Row) ([]byte, error) {
	cells := trimStoredTrailingCells(row.Cells)
	var text strings.Builder
	var marks [][3]int
	// A stack buffer for the runs array: a row with a couple of runs never
	// reaches the heap, and one that does grows the same way any append does.
	var runsBuf [128]byte
	runs := runsBuf[:0]
	runCount := 0
	firstRunIsDefault := false
	var runStyle emulator.Style
	var runLength int
	flush := func() {
		if runLength == 0 {
			return
		}
		if runCount > 0 {
			runs = append(runs, ',')
		} else {
			firstRunIsDefault = runStyle == (emulator.Style{})
		}
		runs = appendBlockStyleRun(runs, runStyle, runLength)
		runCount++
		runLength = 0
	}
	runs = append(runs, '[')
	pos := 0
	for _, cell := range cells {
		if runLength > 0 && cell.Style == runStyle {
			runLength++
		} else {
			flush()
			runStyle = cell.Style
			runLength = 1
		}
		if cell.Width == emulator.WidthSpacerTail {
			continue
		}
		grapheme := cell.Grapheme
		if !cell.HasText {
			grapheme = ""
		}
		text.WriteString(grapheme)
		codepoints := utf8.RuneCountInString(grapheme)
		width := int(cell.Width)
		if codepoints != 1 || width != 1 {
			marks = append(marks, [3]int{pos, codepoints, width})
		}
		pos++
	}
	flush()
	line := blockRowsLine{From: from, Row: blockRow{
		Text:         text.String(),
		Marks:        marks,
		Wrap:         row.Wrap,
		Continuation: row.Continuation,
	}}
	if runCount > 0 && !(runCount == 1 && firstRunIsDefault) {
		runs = append(runs, ']')
		line.Row.Runs = json.RawMessage(runs)
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return nil, fmt.Errorf("content: block rows: marshal row %d: %w", from, err)
	}
	return append(raw, '\n'), nil
}

// appendBlockStyleRun writes one `[style, columns]` run in the bytes the
// encoder used to produce for [][2]any{style, length}: the bare integer 0 for
// the default style, or the five style numbers in one array. It mirrors
// sessionruntime's appendStyleRun, and the cross-encoder test is what keeps
// the two producing the same bytes.
func appendBlockStyleRun(runs []byte, style emulator.Style, length int) []byte {
	runs = append(runs, '[')
	if style == (emulator.Style{}) {
		runs = append(runs, '0')
	} else {
		runs = append(runs, '[')
		runs = strconv.AppendInt(runs, int64(encodeBlockColorValue(style.Foreground)), 10)
		runs = append(runs, ',')
		runs = strconv.AppendInt(runs, int64(encodeBlockColorValue(style.Background)), 10)
		runs = append(runs, ',')
		runs = strconv.AppendInt(runs, int64(encodeBlockColorValue(style.UnderlineColor)), 10)
		runs = append(runs, ',')
		runs = strconv.AppendInt(runs, int64(style.Attributes), 10)
		runs = append(runs, ',')
		runs = strconv.AppendInt(runs, int64(style.Underline), 10)
		runs = append(runs, ']')
	}
	runs = append(runs, ',')
	runs = strconv.AppendInt(runs, int64(length), 10)
	return append(runs, ']')
}

// BlockRowsOutputStart reads a block rows artifact's recorded output start
// (nocx-4y82c.1.11) out of its payload: the absolute row a stored line's
// `from` counts from. Zero means no mark was ever recorded, and then every
// stored row belongs to the block's body.
func BlockRowsOutputStart(payload string) uint64 {
	var p struct {
		OutputStartRow *uint64 `json:"outputStartRow"`
	}
	if strings.TrimSpace(payload) == "" {
		return 0
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil || p.OutputStartRow == nil {
		return 0
	}
	return *p.OutputStartRow
}

// BlockRowsTextAfter turns a stored rows artifact into the plain text a
// block reader needs. Styles are intentionally ignored; a mark's width is
// read only to tell a spacer head from ordinary text (neither carries text,
// so both already read as blank once hasText is false), and continuation
// rows belong to the logical line above.
//
// startRow is the artifact payload's outputStartRow (nocx-4y82c.1.11): the
// lines below it are the command's own echo and redraw, which no reader
// shows. startRow 0 is every line, the unchanged read.
func BlockRowsTextAfter(chunks [][]byte, startRow uint64) (string, error) {
	var body bytes.Buffer
	for _, chunk := range chunks {
		_, _ = body.Write(chunk)
	}

	var lines []string
	decoder := json.NewDecoder(&body)
	for {
		var line struct {
			From uint64 `json:"from"`
			Row  struct {
				Text         string     `json:"text"`
				Marks        [][3]int64 `json:"marks"`
				Continuation bool       `json:"continuation"`
			} `json:"row"`
		}
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("content: block rows: decode stored line: %w", err)
		}
		if line.From < startRow {
			continue
		}
		rowText, err := blockRowPlainText(line.Row.Text, line.Row.Marks)
		if err != nil {
			return "", err
		}
		if line.Row.Continuation && len(lines) > 0 {
			lines[len(lines)-1] += rowText
		} else {
			lines = append(lines, rowText)
		}
	}
	var out bytes.Buffer
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(line)
	}
	return out.String(), nil
}

// blockRowPlainText walks one row's text+marks and reads back each
// position's grapheme (a space for a position with no text), skipping
// nothing else — a spacer contributes no position of its own, so there is
// nothing to skip here that decodeStoredRowCells does not already leave out.
// The trailing space trim matches the prior cell-walk reader: a row's own
// trailing default blanks are already omitted at the source, but a caller
// may still have padded before calling this, and a plain-text reader has no
// use for that padding either way.
func blockRowPlainText(text string, marks [][3]int64) (string, error) {
	cells, err := decodeStoredRowCells(text, marks)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, cell := range cells {
		if cell.width == int(emulator.WidthSpacerTail) {
			continue
		}
		if !cell.hasText {
			out.WriteByte(' ')
			continue
		}
		out.WriteString(cell.grapheme)
	}
	return strings.TrimRight(out.String(), " "), nil
}

// storedCell is the fully expanded (column-indexed) form BlockRowsTextAfter
// reads: what decodeStoredRowCells rebuilds from a row's text+marks,
// spacers synthesised back in.
type storedCell struct {
	grapheme string
	width    int
	hasText  bool
}

// decodeStoredRowCells rebuilds the column-indexed cells a stored row's
// text+marks encode — the read half of encodeBlockRowsLine's write half,
// used by BlockRowsTextAfter. It does not need runs (styles are not part of
// plain text) and does not pad to any column count: a stored row carries no
// frame geometry, and nothing here needs the untouched tail a live frame
// pads back in.
func decodeStoredRowCells(text string, marks [][3]int64) ([]storedCell, error) {
	codepoints := []rune(text)
	byPos := make(map[int][2]int64, len(marks))
	explicitCodepoints := 0
	for _, m := range marks {
		if _, dup := byPos[int(m[0])]; dup {
			return nil, fmt.Errorf("content: block rows: two marks name position %d", m[0])
		}
		byPos[int(m[0])] = [2]int64{m[1], m[2]}
		explicitCodepoints += int(m[1])
	}
	positions := len(codepoints) - explicitCodepoints + len(marks)
	if positions < 0 {
		return nil, fmt.Errorf("content: block rows: marks claim more codepoints than the row's text has")
	}
	out := make([]storedCell, 0, positions)
	idx := 0
	for pos := 0; pos < positions; pos++ {
		span, width := 1, 1
		if m, ok := byPos[pos]; ok {
			span, width = int(m[0]), int(m[1])
		}
		if idx+span > len(codepoints) {
			return nil, fmt.Errorf("content: block rows: position %d needs %d codepoints past the row's text", pos, span)
		}
		grapheme := string(codepoints[idx : idx+span])
		idx += span
		out = append(out, storedCell{grapheme: grapheme, width: width, hasText: span > 0})
		if width == 2 {
			out = append(out, storedCell{width: 3})
		}
	}
	return out, nil
}

func trimStoredTrailingCells(cells []emulator.Cell) []emulator.Cell {
	end := len(cells)
	for end > 0 && isDefaultBlankCell(cells[end-1]) {
		end--
	}
	return cells[:end]
}

func isDefaultBlankCell(cell emulator.Cell) bool {
	return cell.Grapheme == "" &&
		!cell.HasText &&
		cell.Width == emulator.WidthNarrow &&
		cell.Style == (emulator.Style{})
}

// encodeBlockColorValue mirrors sessionruntime's encodeColorValue: 0
// default, 1-256 a palette index plus one, 257+ a packed RGB triple.
func encodeBlockColorValue(color emulator.Color) int {
	switch color.Kind {
	case emulator.ColorPalette:
		return int(color.Palette) + 1
	case emulator.ColorRGB:
		return 257 + (int(color.RGB.R)<<16 | int(color.RGB.G)<<8 | int(color.RGB.B))
	default:
		return 0
	}
}
