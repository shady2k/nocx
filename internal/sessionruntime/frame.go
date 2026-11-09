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
	"strconv"
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
	State    frameState    `json:"state"`
	Rows     []frameRow    `json:"rows"`
}

type frameState struct {
	BaseRow        uint64     `json:"baseRow"`
	BufferKind     string     `json:"bufferKind"`
	BufferInstance string     `json:"bufferInstance"`
	BracketedPaste bool       `json:"bracketedPaste"`
	MouseTracking  bool       `json:"mouseTracking"`
	PointerShape   *string    `json:"pointerShape"`
	Title          *string    `json:"title"`
	Cwd            *CwdReport `json:"cwd"`
}

// pointerShape is the shape a program asked for with OSC 22 as the wire spells
// it, or nil when it asked for nothing (nocx-1ebrs.24). The emulator reports
// "nothing" as the empty string, and the wire spells it as null: an empty
// shape name would be a value the client had to know means "unset", while null
// is the same absence the title and cwd already carry.
func pointerShape(asked string) *string {
	if asked == "" {
		return nil
	}
	return &asked
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
//
// RUNS IS PRE-ENCODED JSON, and that is the allocation budget's doing
// (nocx-4y82c.1.5). The shape it used to carry — [][2]any, one `any` holding
// either the integer 0 or the five style numbers, and one holding the length
// — put two interface values per run through encoding/json. Go 1.26's encoder
// marshalled them in place; Go 1.27's marshals on encoding/json/v2, which
// makes each interface value ADDRESSABLE first (makeInterfaceArshaler →
// newAddressableValue → reflect.New), so a link of styled output paid two
// extra allocations per style run: measured 82,799 allocs/MiB against 52,868
// on the same tree under 1.26, and the pump's budget is 67,000. The runs
// array is now written by encodeRow itself (appendStyleRun, below), byte for
// byte what the encoder wrote, and carried as json.RawMessage — no interface
// value reaches the encoder at all.
type frameRow struct {
	Text         string          `json:"text"`
	Marks        [][3]int        `json:"marks,omitempty"`
	Runs         json.RawMessage `json:"runs,omitempty"`
	Wrap         bool            `json:"wrap,omitempty"`
	Continuation bool            `json:"continuation,omitempty"`
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
	geometryRows, err := screenIntToUint64(geom.Rows, "frame geometry rows")
	if err != nil {
		return nil, err
	}
	if snap.BaseRow > MaxScreenRowOffset || geometryRows > MaxScreenRowOffset-snap.BaseRow {
		return nil, fmt.Errorf("sessionruntime: absolute screen rows exceed JSON safe-integer ceiling %d", MaxScreenRowOffset)
	}
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
		State: frameState{
			BaseRow:        snap.BaseRow,
			BufferKind:     frameBufferKind(snap.Identity.AltScreen),
			BufferInstance: strconv.FormatUint(snap.Identity.BufferInstance, 10),
			BracketedPaste: snap.Modes.BracketedPaste,
			MouseTracking:  snap.Modes.MouseTracking,
			PointerShape:   pointerShape(snap.Modes.PointerShape),
			Title:          snap.Title,
			Cwd:            snap.Cwd,
		},
		Rows: make([]frameRow, 0, len(snap.Rows)),
	}
	// ONE runs buffer for the whole batch: every row's `runs` is a sub-slice of
	// it, so a snapshot costs one growable allocation rather than one per row
	// (nocx-4y82c.1.5, and the note on frameRow).
	var runsScratch []byte
	for _, row := range snap.Rows {
		var encoded frameRow
		encoded, runsScratch = encodeRow(row, runsScratch)
		f.Rows = append(f.Rows, encoded)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("sessionruntime: marshal frame: %w", err)
	}
	return raw, nil
}

func frameBufferKind(alternate bool) string {
	if alternate {
		return "alternate"
	}
	return "normal"
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
//
// THE RUNS ARRAY IS WRITTEN HERE, into the caller's scratch buffer, and the
// row holds a SUB-SLICE of it — so a batch of rows shares one growable
// allocation instead of one buffer per row (nocx-4y82c.1.5). The scratch is
// returned grown; a slice handed to an earlier row stays valid when the
// buffer is reallocated, because append copies and the old array lives on.
func encodeRow(row emulator.Row, scratch []byte) (frameRow, []byte) {
	cells := trimTrailingDefaultBlanks(row.Cells)
	var text strings.Builder
	var marks [][3]int
	start := len(scratch)
	scratch = append(scratch, '[')
	runs := 0
	firstRunIsDefault := false
	var runStyle emulator.Style
	var runLength int
	flush := func() {
		if runLength == 0 {
			return
		}
		if runs > 0 {
			scratch = append(scratch, ',')
		} else {
			firstRunIsDefault = runStyle == (emulator.Style{})
		}
		scratch = appendStyleRun(scratch, runStyle, runLength)
		runs++
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
	// A row whose styling is exactly what a receiver assumes when `runs` is
	// absent says nothing: one run of the default style over the whole row
	// (the shortcut isImplicitDefaultRuns used to take), or no cells at all.
	if runs == 0 || (runs == 1 && firstRunIsDefault) {
		return out, scratch[:start]
	}
	scratch = append(scratch, ']')
	out.Runs = json.RawMessage(scratch[start:])
	return out, scratch
}

// appendStyleRun writes one `[style, columns]` run — the exact bytes
// encoding/json wrote for [][2]any{styleValue, length}: the bare integer 0
// for the default style, or the five style numbers in one array.
// encodeColorValue packs a colour into its single integer.
func appendStyleRun(scratch []byte, style emulator.Style, length int) []byte {
	scratch = append(scratch, '[')
	if style == (emulator.Style{}) {
		scratch = append(scratch, '0')
	} else {
		scratch = append(scratch, '[')
		scratch = strconv.AppendInt(scratch, int64(encodeColorValue(style.Foreground)), 10)
		scratch = append(scratch, ',')
		scratch = strconv.AppendInt(scratch, int64(encodeColorValue(style.Background)), 10)
		scratch = append(scratch, ',')
		scratch = strconv.AppendInt(scratch, int64(encodeColorValue(style.UnderlineColor)), 10)
		scratch = append(scratch, ',')
		scratch = strconv.AppendInt(scratch, int64(style.Attributes), 10)
		scratch = append(scratch, ',')
		scratch = strconv.AppendInt(scratch, int64(style.Underline), 10)
		scratch = append(scratch, ']')
	}
	scratch = append(scratch, ',')
	scratch = strconv.AppendInt(scratch, int64(length), 10)
	return append(scratch, ']')
}

// EncodeRows renders departed rows as the frame contract's rows array —
// the same text+marks+runs encoding EncodeFrame gives a snapshot, against
// the same $defs the contract declares (nocx-2v80t.3.6). The rows that
// stream to a coordinator ride in this shape, so the block a coordinator
// stores from the stream and the live frame it paints from are ONE
// vocabulary, encoded by ONE encoder.
func EncodeRows(rows []emulator.Row) ([]byte, error) {
	out := make([]frameRow, 0, len(rows))
	// The same one-buffer rule EncodeFrame follows, and it matters more here:
	// this is the pump's own window.
	var runsScratch []byte
	for _, row := range rows {
		var encoded frameRow
		encoded, runsScratch = encodeRow(row, runsScratch)
		out = append(out, encoded)
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
