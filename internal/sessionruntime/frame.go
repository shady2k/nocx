package sessionruntime

// The frame the runtime publishes (contracts/session.frame.schema.json), as
// the Go DTO that marshals to it. Nothing here invents the data: every field
// is read from [Snapshot] — revision, committed geometry and caret — and from
// the emulator rows that snapshot already holds, cells and styles included.
//
// # Shape (nocx-zg3k3.2.12)
//
// The wire shape is the one the owner agreed on 2026-09-21 and confirmed
// brought to its final form on 2026-09-24: a row rides as its TEXT — the
// clusters in column order, the spacer of a wide cluster not repeated — its
// style runs measured in columns, and a sparse list of the positions whose
// text length or column footprint is not the default (one codepoint, one
// column). A trailing run of ordinary default blank cells is not sent at
// all: the row's own length names where its explicit content ends, and a
// receiver pads the rest from the frame's geometry.
//
// This replaces the 2026-09-22 shape (nocx-zg3k3.2.6) that put a
// [grapheme, width, hasText] tuple in the wire for EVERY column, blanks
// included: measured on an 18-character row, that shape stored 542 B
// (~30x its text) once the row carried nothing but the trailing trim
// already in place. The named-field style object measured there — 220 B for
// one entirely default style — was the other half of the cost: a run's
// style now rides as a 5-element tuple of small integers, or the bare
// integer 0 when the whole style is the zero value, and a row whose runs
// are entirely that one default style omits `runs` altogether (a receiver
// with no runs paints the row's whole explicit width in the default
// style). Both changes are independent and both are needed: cutting cells
// down to text+marks without also cutting the style payload leaves the
// style object the dominant cost at ~220 B before a single character of
// text is counted.
//
// # Positions, columns and marks
//
// A "position" is one surviving column-owning entry once the spacer that
// follows a wide cluster is folded away — the unit `text` and `marks` are
// indexed by. A "column" is the grid's own unit — the one `runs` is
// measured in, and the one a wide cluster's synthesised spacer adds a
// second of. The two coincide except across a wide cluster, which is one
// position and two columns.
//
// `marks` names, for the sparse few positions where it is not (1, 1), the
// pair (codepoints, width): codepoints is the position's grapheme length in
// Unicode codepoints — 0 for a cell with no text, 2+ for a cluster built of
// several codepoints (a combining mark, a family emoji's ZWJ sequence) —
// and width is the column footprint (emulator.Width: 1 narrow is the
// default and never marked, 2 wide, 4 spacerHead — 3 spacerTail never
// appears here, because a spacer position does not exist in this
// vocabulary at all, only the extra column its preceding wide cluster's
// mark declares). Codepoints rather than UTF-16 units or UTF-8 bytes,
// because it is the one unit Go's `[]rune` and JavaScript's `Array.from`
// agree on without either language re-deriving cluster boundaries the
// runtime already decided — the property ADR-0065 chose libghostty-vt for
// in the first place.
//
// # Width and colour vocabularies
//
// Colour rides as one integer per the DECODE side's own comment
// (decodeColorValue in internal/helper/client/rows.go): 0 default, 1-256 a
// palette index plus one, 257+ a packed RGB triple offset by 257. Encoding
// passes emulator.Color through that packing; the conformance test in
// frame_test.go and the cross-package test in
// internal/content/block_rows_encode_test.go are what keep every encoder
// and both decoders (the frontend's and internal/helper/client's) reading
// the same three integers.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/emulator"
)

// ErrNoScreen names an encode asked for a frame of a screen that could not be
// read. A committed frame carries the whole rectangle; a screen that answers
// no rows has no frame to send, and the caller (the publish path) says so by
// skipping this revision rather than by emitting a frame nobody could paint.
var ErrNoScreen = errors.New("sessionruntime: the screen could not be read, no frame to encode")

// Frame is one full screen at one revision, as the wire declares it.
type Frame struct {
	Revision uint64        `json:"revision"`
	Geometry frameGeometry `json:"geometry"`
	Cursor   frameCursor   `json:"cursor"`
	Rows     []frameRow    `json:"rows"`
}

type frameGeometry struct {
	Cols         int    `json:"cols"`
	Rows         int    `json:"rows"`
	CellWidthPx  int    `json:"cellWidthPx"`
	CellHeightPx int    `json:"cellHeightPx"`
	Revision     uint64 `json:"revision"`
}

type frameCursor struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Visible bool `json:"visible"`
}

// frameRow is one physical line, in the compact text+marks+runs shape this
// task brought the wire to. Runs and marks are omitted (nil, not an empty
// array — encoding/json drops a nil slice under omitempty) when the row
// needs neither: no styling beyond the default, no cluster whose codepoint
// count or column width departs from the default (narrow, one codepoint).
type frameRow struct {
	Text         string   `json:"text"`
	Marks        [][3]int `json:"marks,omitempty"`
	Runs         [][2]any `json:"runs,omitempty"`
	Wrap         bool     `json:"wrap,omitempty"`
	Continuation bool     `json:"continuation,omitempty"`
}

// EncodeFrame renders one snapshot as the wire frame. It takes the rows the
// snapshot already holds — read at the revision the snapshot names, under the
// session lock the caller holds — so the bytes describe exactly the revision
// they carry, never a later read of a mutable screen.
func EncodeFrame(snap Snapshot) ([]byte, error) {
	if snap.Rows == nil {
		return nil, ErrNoScreen
	}
	geom := snap.Geometry.Geometry
	f := Frame{
		Revision: uint64(snap.Revision),
		Geometry: frameGeometry{
			Cols:         geom.Cols,
			Rows:         geom.Rows,
			CellWidthPx:  geom.CellWidthPx,
			CellHeightPx: geom.CellHeightPx,
			Revision:     uint64(snap.Geometry.Revision),
		},
		Cursor: frameCursor{X: snap.Cursor.X, Y: snap.Cursor.Y, Visible: snap.Cursor.Visible},
		Rows:   make([]frameRow, 0, len(snap.Rows)),
	}
	for _, row := range snap.Rows {
		f.Rows = append(f.Rows, encodeRow(row))
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("sessionruntime: marshal frame: %w", err)
	}
	return raw, nil
}

// encodeRow renders one row in the text+marks+runs shape. Style runs are
// measured in columns and walk EVERY cell, spacers included, because a
// spacer's own style read can differ from the wide cluster it follows
// (independent grid references) and a run the receiver cannot reconstruct
// column-by-column is not self-describing. Marks and text walk only the
// surviving positions — a spacer contributes no position of its own.
//
// Trailing cells that are entirely the zero value (no text, narrow, default
// style) are dropped: the row's own length is what tells a receiver where
// its explicit content ends, and the frame's geometry supplies the rest.
func encodeRow(row emulator.Row) frameRow {
	cells := trimTrailingDefaultBlanks(row.Cells)
	var text strings.Builder
	var marks [][3]int
	var runs [][2]any
	var runStyle emulator.Style
	var runLength int
	flush := func() {
		if runLength == 0 {
			return
		}
		runs = append(runs, [2]any{encodeStyleValue(runStyle), runLength})
		runLength = 0
	}
	pos := 0
	for _, cell := range cells {
		// Runs are measured in columns and see every cell, spacers included.
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
	out := frameRow{
		Text:         text.String(),
		Marks:        marks,
		Wrap:         row.Wrap,
		Continuation: row.Continuation,
	}
	if !isImplicitDefaultRuns(runs) {
		out.Runs = runs
	}
	return out
}

// isImplicitDefaultRuns reports whether runs is exactly the one a receiver
// assumes when `runs` is absent from the wire: a single run of the default
// style. Omitting it is the shortcut a fully unstyled row (or the unstyled
// remainder of an otherwise-trimmed row) takes.
func isImplicitDefaultRuns(runs [][2]any) bool {
	if len(runs) != 1 {
		return false
	}
	style, ok := runs[0][0].(int)
	return ok && style == 0
}

// EncodeRows renders departed rows as the frame contract's rows array —
// the same text+marks+runs encoding EncodeFrame gives a snapshot, against
// the same $defs the contract declares (nocx-2v80t.3.6). The rows that
// stream to a coordinator ride in this shape, so the block a coordinator
// stores from the stream and the live frame it paints from are ONE
// vocabulary, encoded by ONE encoder.
func EncodeRows(rows []emulator.Row) ([]byte, error) {
	out := make([]frameRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, encodeRow(row))
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("sessionruntime: marshal rows: %w", err)
	}
	return raw, nil
}

// trimTrailingDefaultBlanks drops a trailing run of cells that are entirely
// the zero value: no text, narrow, the zero style. Anything else — text, a
// non-narrow width (a styled trailing blank is still a blank, but a spacer
// head is not the zero value either), or a non-default style — stops the
// trim, because it is content a receiver could not otherwise reconstruct.
func trimTrailingDefaultBlanks(cells []emulator.Cell) []emulator.Cell {
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

// encodeStyleValue packs one style as the wire's own vocabulary: the bare
// integer 0 when every field is the zero value (the overwhelmingly common
// case — plain, unstyled output), or the 5-element tuple [foreground,
// background, underlineColor, attributes, underline] otherwise.
func encodeStyleValue(style emulator.Style) any {
	if style == (emulator.Style{}) {
		return 0
	}
	return [5]int{
		encodeColorValue(style.Foreground),
		encodeColorValue(style.Background),
		encodeColorValue(style.UnderlineColor),
		int(style.Attributes),
		int(style.Underline),
	}
}

// encodeColorValue packs one colour as a single integer: 0 the terminal's
// own default (emulator.ColorDefault, the zero value), 1-256 a palette
// index plus one (0 is taken by default, so every index shifts up), 257+ a
// packed 24-bit RGB triple offset by 257 so it never collides with a
// palette index. The packing is undone by decodeColorValue in
// internal/helper/client/rows.go and by the frontend's cell-model.ts; all
// three read the same three ranges.
func encodeColorValue(color emulator.Color) int {
	switch color.Kind {
	case emulator.ColorPalette:
		return int(color.Palette) + 1
	case emulator.ColorRGB:
		return 257 + (int(color.RGB.R)<<16 | int(color.RGB.G)<<8 | int(color.RGB.B))
	default:
		return 0
	}
}
