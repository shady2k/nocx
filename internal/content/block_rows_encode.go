package content

// The stored row vocabulary (nocx-2v80t.3.7): one JSON Lines line per row of
// a streamed block, in the SAME cell vocabulary the live screen frame
// declares (contracts/session.frame.schema.json, ADR-0072's one record for
// the cell model). The shape is forced here by the frame contract, not
// invented: cells as the positional tuple [grapheme, width, hasText], a
// row's styles as maximal [style, length] runs, a row self-describing — it
// decodes against nothing outside itself, which is what lets a stored block
// survive without the frame it arrived in.
//
// The Go shapes are declared HERE rather than borrowed from
// internal/sessionruntime's frame encoder for an ownership reason, not a
// vocabulary one: the session runtime is the helper's half and this package
// is the store's, and the ONE vocabulary is enforced where it is checkable —
// the schema, the row shape's own test against it, and the cross-encoder
// test that feeds the same emulator rows to both encoders and requires the
// same bytes. Two encoders, one contract, zero drift that a test cannot see.

import (
	"encoding/json"
	"fmt"

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

// blockRow is one physical line of the screen. Cells and runs are tuples
// rather than named structs for the measured reason the frame contract
// records: at thousands of cells the field-name bytes are the payload.
type blockRow struct {
	Cells        [][3]any `json:"cells"`
	Runs         [][2]any `json:"runs"`
	Wrap         bool     `json:"wrap"`
	Continuation bool     `json:"continuation"`
}

type blockStyle struct {
	Foreground     blockColor `json:"foreground"`
	Background     blockColor `json:"background"`
	UnderlineColor blockColor `json:"underlineColor"`
	Attributes     int        `json:"attributes"`
	Underline      int        `json:"underline"`
}

type blockColor struct {
	Kind    int      `json:"kind"`
	Palette int      `json:"palette"`
	RGB     blockRGB `json:"rgb"`
}

type blockRGB struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// encodeBlockRowsLine renders one departed row as one stored line, newline
// terminated. Every line parses on its own; a body is the concatenation of
// its lines and is read back in seq order.
func encodeBlockRowsLine(from uint64, row emulator.Row) ([]byte, error) {
	line := blockRowsLine{From: from, Row: blockRow{
		Cells:        make([][3]any, 0, len(row.Cells)),
		Runs:         make([][2]any, 0, len(row.Cells)),
		Wrap:         row.Wrap,
		Continuation: row.Continuation,
	}}
	var runStyle emulator.Style
	var runLength int
	flush := func() {
		if runLength == 0 {
			return
		}
		line.Row.Runs = append(line.Row.Runs, [2]any{encodeBlockStyle(runStyle), runLength})
		runLength = 0
	}
	for _, cell := range row.Cells {
		line.Row.Cells = append(line.Row.Cells, [3]any{cell.Grapheme, int(cell.Width), cell.HasText})
		if runLength > 0 && cell.Style == runStyle {
			runLength++
			continue
		}
		flush()
		runStyle = cell.Style
		runLength = 1
	}
	flush()
	raw, err := json.Marshal(line)
	if err != nil {
		return nil, fmt.Errorf("content: block rows: marshal row %d: %w", from, err)
	}
	return append(raw, '\n'), nil
}

func encodeBlockStyle(style emulator.Style) blockStyle {
	return blockStyle{
		Foreground:     encodeBlockColor(style.Foreground),
		Background:     encodeBlockColor(style.Background),
		UnderlineColor: encodeBlockColor(style.UnderlineColor),
		Attributes:     int(style.Attributes),
		Underline:      int(style.Underline),
	}
}

func encodeBlockColor(color emulator.Color) blockColor {
	return blockColor{
		Kind:    int(color.Kind),
		Palette: int(color.Palette),
		RGB:     blockRGB{R: int(color.RGB.R), G: int(color.RGB.G), B: int(color.RGB.B)},
	}
}
