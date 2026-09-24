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
// row carries no frame geometry to pad against and the reader (BlockRowsText,
// frontend/src/scrollback/block-rows.ts) treats a shorter row as ending
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
type blockRow struct {
	Text         string   `json:"text"`
	Marks        [][3]int `json:"marks,omitempty"`
	Runs         [][2]any `json:"runs,omitempty"`
	Wrap         bool     `json:"wrap,omitempty"`
	Continuation bool     `json:"continuation,omitempty"`
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
	var runs [][2]any
	var runStyle emulator.Style
	var runLength int
	flush := func() {
		if runLength == 0 {
			return
		}
		runs = append(runs, [2]any{encodeBlockStyleValue(runStyle), runLength})
		runLength = 0
	}
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
	if !isImplicitDefaultBlockRuns(runs) {
		line.Row.Runs = runs
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return nil, fmt.Errorf("content: block rows: marshal row %d: %w", from, err)
	}
	return append(raw, '\n'), nil
}

// isImplicitDefaultBlockRuns mirrors sessionruntime's isImplicitDefaultRuns:
// true when runs is exactly the one a reader assumes when `runs` is absent
// — a single run of the default style.
func isImplicitDefaultBlockRuns(runs [][2]any) bool {
	if len(runs) != 1 {
		return false
	}
	style, ok := runs[0][0].(int)
	return ok && style == 0
}

// BlockRowsText turns a stored rows artifact into the plain text a block
// reader needs. Styles are intentionally ignored; a mark's width is read
// only to tell a spacer head from ordinary text (neither carries text, so
// both already read as blank once hasText is false), and continuation rows
// belong to the logical line above.
func BlockRowsText(chunks [][]byte) (string, error) {
	var body bytes.Buffer
	for _, chunk := range chunks {
		_, _ = body.Write(chunk)
	}

	var lines []string
	decoder := json.NewDecoder(&body)
	for {
		var line struct {
			Row struct {
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

// storedCell is the fully expanded (column-indexed) form BlockRowsText and
// trimStoredTrailingCells's callers read: what decodeStoredRowCells rebuilds
// from a row's text+marks, spacers synthesised back in.
type storedCell struct {
	grapheme string
	width    int
	hasText  bool
}

// decodeStoredRowCells rebuilds the column-indexed cells a stored row's
// text+marks encode — the read half of encodeBlockRowsLine's write half,
// used by BlockRowsText. It does not need runs (styles are not part of
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

// encodeBlockStyleValue mirrors sessionruntime's encodeStyleValue: the bare
// integer 0 for the all-default style, or the 5-element tuple otherwise.
func encodeBlockStyleValue(style emulator.Style) any {
	if style == (emulator.Style{}) {
		return 0
	}
	return [5]int{
		encodeBlockColorValue(style.Foreground),
		encodeBlockColorValue(style.Background),
		encodeBlockColorValue(style.UnderlineColor),
		int(style.Attributes),
		int(style.Underline),
	}
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
