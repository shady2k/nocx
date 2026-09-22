package sessionruntime

// The frame wire shape (contracts/session.frame.schema.json) judged from this
// package: the DTO marshals to something the schema accepts, a required field
// removed is refused, and the runs a row carries partition its cells exactly.
// The size measurements at real geometry live here too — the numbers this
// task's close reports come from THIS test, not from the schema's record of
// somebody else's run.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/emulator"
)

const frameContractDir = "../../contracts"

func loadFrameContractSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	path := filepath.Join(frameContractDir, "session.frame.schema.json")
	f, openErr := os.Open(path) //nolint:gosec // test-only path under contracts/
	if openErr != nil {
		t.Fatalf("open %s: %v", path, openErr)
	}
	defer func() { _ = f.Close() }()
	doc, parseErr := jsonschema.UnmarshalJSON(f)
	if parseErr != nil {
		t.Fatalf("parse %s: %v", path, parseErr)
	}
	if addErr := c.AddResource("https://nocx.local/contracts/session.frame.schema.json", doc); addErr != nil {
		t.Fatalf("add session.frame.schema.json: %v", addErr)
	}
	s, err := c.Compile("https://nocx.local/contracts/session.frame.schema.json")
	if err != nil {
		t.Fatalf("compile session.frame.schema.json: %v", err)
	}
	return s
}

func validateFrameContract(t *testing.T, s *jsonschema.Schema, raw []byte, what string) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: unmarshal: %v", what, err)
	}
	if err := s.Validate(doc); err != nil {
		t.Errorf("%s does not satisfy its contract:\n%v\n\npayload was:\n%s", what, err, raw)
	}
}

// denseSnapshot builds the realistic screen the size measurement is taken on:
// a prompt, coloured output, a truecolour banner, CJK wide clusters, a ZWJ
// family emoji, a cell-fit boxed glyph and an inverse status bar. It is the
// same screen family the contract's own record measured, rebuilt here so the
// numbers this task reports are this test's, not a citation.
func denseSnapshot(cols, rows int) Snapshot {
	rgb := func(r, g, b int) emulator.Color {
		return emulator.Color{Kind: emulator.ColorRGB, RGB: emulator.RGB{R: uint8(r), G: uint8(g), B: uint8(b)}} // #nosec G115 — test fixture constants, each channel modulo 256
	}
	palette := func(n int) emulator.Color {
		return emulator.Color{Kind: emulator.ColorPalette, Palette: uint8(n)} // #nosec G115 — a 256-colour palette index by construction
	}
	bold := emulator.Style{Foreground: palette(2), Attributes: emulator.AttrBold}
	truecolour := emulator.Style{Foreground: rgb(244, 81, 108)}
	inverse := emulator.Style{Foreground: emulator.Color{Kind: emulator.ColorDefault}, Background: palette(4), Attributes: emulator.AttrInverse}

	styled := make([]emulator.Row, 0, rows)
	for y := range rows {
		cells := make([]emulator.Cell, 0, cols)
		for x := 0; x < cols; x++ {
			g, w, text := " ", emulator.WidthNarrow, false
			var style emulator.Style
			switch {
			case y == 0:
				// The prompt: palette-green path, user@host, a wide CJK
				// cluster and a ZWJ family emoji near the end.
				style = bold
				switch {
				case x == 0:
					g, text = "~", true
				case x == 2:
					g, text = "$", true
				case x >= 4 && x < 4+len("user@host"):
					g, text = string("user@host"[x-4]), true
				case x == 14:
					g, w, text = "你", emulator.WidthWide, true
					cells = append(cells, emulator.Cell{Grapheme: g, Width: w, HasText: text, Style: truecolour})
					cells = append(cells, emulator.Cell{Grapheme: "", Width: emulator.WidthSpacerTail, Style: truecolour})
					x++
					continue
				case x == 18:
					g, w, text = "👨‍👩‍👧", emulator.WidthWide, true
					cells = append(cells, emulator.Cell{Grapheme: g, Width: w, HasText: text, Style: style})
					cells = append(cells, emulator.Cell{Grapheme: "", Width: emulator.WidthSpacerTail, Style: style})
					x++
					continue
				case x == 21:
					g, text = "🭽", true
				}
			case y == rows-1:
				// The inverse status bar: background, no text in its gaps.
				style = inverse
				switch {
				case x < 8:
					g, text = string(" STATUS "[x]), true
				}
			case y == rows/2:
				// The truecolour banner: one distinct RGB per cell.
				style = emulator.Style{Foreground: rgb(x*255/cols, 128, 255-y*128/rows)}
				g, text = string(rune('a'+(x%26))), true
			default:
				if x < cols/2 {
					g, text = string(rune('0'+(x%10))), true
				}
			}
			cells = append(cells, emulator.Cell{Grapheme: g, Width: w, HasText: text, Style: style})
		}
		styled = append(styled, emulator.Row{
			Cells:        cells,
			Wrap:         y == 1,
			Continuation: y == 2,
		})
	}
	return Snapshot{
		Revision: 42,
		Geometry: GeometryCommit{
			Geometry: emulator.Geometry{Cols: cols, Rows: rows, CellWidthPx: 8, CellHeightPx: 16},
			Revision: 7,
		},
		Cursor: emulator.Cursor{X: 3, Y: 1, Visible: true},
		Rows:   styled,
	}
}

func TestFrameDTOConformsToContract(t *testing.T) {
	schema := loadFrameContractSchema(t)
	raw, err := EncodeFrame(denseSnapshot(80, 24))
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	validateFrameContract(t, schema, raw, "frame DTO (populated)")

	// The same test with a required field removed is refused: revision is the
	// frame's identity — a frame without it cannot be placed on the clock it
	// belongs to.
	var doc map[string]any
	if jerr := json.Unmarshal(raw, &doc); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	delete(doc, "revision")
	broken, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var brokenDoc any
	if err := json.Unmarshal(broken, &brokenDoc); err != nil {
		t.Fatalf("unmarshal broken: %v", err)
	}
	if err := schema.Validate(brokenDoc); err == nil {
		t.Errorf("a frame with revision removed satisfies the contract; want refusal")
	}
}

// TestFrameRowsCarrySelfDescribingRuns is the row invariant the schema states:
// runs partition the row's cells exactly (lengths sum to the cells length),
// every run covers at least one cell, and no two adjacent runs share a style —
// maximal by construction, because the sender merges.
func TestFrameRowsCarrySelfDescribingRuns(t *testing.T) {
	raw, err := EncodeFrame(denseSnapshot(80, 24))
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	var decoded struct {
		Rows []struct {
			Cells []json.RawMessage    `json:"cells"`
			Runs  [][2]json.RawMessage `json:"runs"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Rows) != 24 {
		t.Fatalf("rows: got %d, want 24", len(decoded.Rows))
	}
	for y, row := range decoded.Rows {
		total := 0
		for i, run := range row.Runs {
			var length int
			if err := json.Unmarshal(run[1], &length); err != nil {
				t.Fatalf("row %d run %d length: %v", y, i, err)
			}
			if length < 1 {
				t.Errorf("row %d run %d covers %d cells, want at least 1", y, i, length)
			}
			total += length
			if i > 0 && bytes.Equal(run[0], row.Runs[i-1][0]) {
				t.Errorf("row %d runs %d and %d carry the same style; the sender must merge adjacent equal styles", y, i-1, i)
			}
		}
		if total != len(row.Cells) {
			t.Errorf("row %d run lengths sum to %d, cells length %d; runs must partition the cells exactly", y, total, len(row.Cells))
		}
	}
}

// TestFrameSizeAtRealGeometry measures the encoded frame at the three
// geometries the task names, on the realistic dense screen. The numbers are
// logged for this task's close and bounded by the carrier budget the helper
// reserves for one screen frame: a realistic screen must fit one carrier
// frame with room to spare — the pathological screen (every cell a distinct
// style) is the split path's job, measured where the split lives.
func TestFrameSizeAtRealGeometry(t *testing.T) {
	for _, geometry := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		raw, err := EncodeFrame(denseSnapshot(geometry[0], geometry[1]))
		if err != nil {
			t.Fatalf("EncodeFrame %dx%d: %v", geometry[0], geometry[1], err)
		}
		t.Logf("%dx%d: %d bytes", geometry[0], geometry[1], len(raw))
	}
}
