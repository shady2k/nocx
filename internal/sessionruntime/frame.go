package sessionruntime

// The frame the runtime publishes (contracts/session.frame.schema.json), as
// the Go DTO that marshals to it. Nothing here invents the data: every field
// is read from [Snapshot] — revision, committed geometry and caret — and from
// the emulator rows that snapshot already holds, cells and styles included.
//
// # Shape
//
// The wire shape is the one the contract was reshaped to on 2026-09-22
// (nocx-zg3k3.2.6), and it is positional because the field-name bytes were
// measured to BE the payload at thousands of cells: a cell rides as the tuple
// [grapheme, width, hasText], and a row's styles ride as runs — [style,
// length] pairs, each covering that many adjacent cells, maximal by
// construction because the encoder merges adjacent equal styles. A row stays
// self-describing: it decodes against nothing outside itself.
//
// # Width and colour vocabularies
//
// Both ride as the plain integers the enumerations spell, in the same values
// the emulator declares them (emulator.Width 0-4, emulator.ColorKind 0-2,
// emulator.Attributes as the bitset, emulator.Underline 0-5). The contract's
// enums are those constants written down; encoding passes them through and
// the conformance test in frame_test.go is what keeps the two vocabularies
// one.

import (
	"encoding/json"
	"errors"
	"fmt"

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

// frameRow is one physical line. Cells and runs are tuples rather than named
// structs for the measured reason the contract records: at thousands of cells
// the field-name bytes are the payload.
type frameRow struct {
	Cells        [][3]any `json:"cells"`
	Runs         [][2]any `json:"runs"`
	Wrap         bool     `json:"wrap"`
	Continuation bool     `json:"continuation"`
}

type frameStyle struct {
	Foreground     frameColor `json:"foreground"`
	Background     frameColor `json:"background"`
	UnderlineColor frameColor `json:"underlineColor"`
	Attributes     int        `json:"attributes"`
	Underline      int        `json:"underline"`
}

type frameColor struct {
	Kind    int      `json:"kind"`
	Palette int      `json:"palette"`
	RGB     frameRGB `json:"rgb"`
}

type frameRGB struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
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
		encoded, err := encodeRow(row)
		if err != nil {
			return nil, err
		}
		f.Rows = append(f.Rows, encoded)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("sessionruntime: marshal frame: %w", err)
	}
	return raw, nil
}

// encodeRow renders one row: cells as tuples, styles as maximal runs.
func encodeRow(row emulator.Row) (frameRow, error) {
	out := frameRow{
		Cells:        make([][3]any, 0, len(row.Cells)),
		Runs:         make([][2]any, 0, len(row.Cells)),
		Wrap:         row.Wrap,
		Continuation: row.Continuation,
	}
	var runStyle emulator.Style
	var runLength int
	flush := func() {
		if runLength == 0 {
			return
		}
		out.Runs = append(out.Runs, [2]any{encodeStyle(runStyle), runLength})
		runLength = 0
	}
	for _, cell := range row.Cells {
		out.Cells = append(out.Cells, [3]any{cell.Grapheme, int(cell.Width), cell.HasText})
		if runLength > 0 && cell.Style == runStyle {
			runLength++
			continue
		}
		flush()
		runStyle = cell.Style
		runLength = 1
	}
	flush()
	return out, nil
}

func encodeStyle(style emulator.Style) frameStyle {
	return frameStyle{
		Foreground:     encodeColor(style.Foreground),
		Background:     encodeColor(style.Background),
		UnderlineColor: encodeColor(style.UnderlineColor),
		Attributes:     int(style.Attributes),
		Underline:      int(style.Underline),
	}
}

func encodeColor(color emulator.Color) frameColor {
	return frameColor{
		Kind:    int(color.Kind),
		Palette: int(color.Palette),
		RGB:     frameRGB{R: int(color.RGB.R), G: int(color.RGB.G), B: int(color.RGB.B)},
	}
}
