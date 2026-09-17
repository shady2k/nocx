// Package corpus loads the qualification spike's recorded terminal captures
// and renders a final screen in a form two different drivers can compare.
//
// The captures are the same files .internal/spikes/emulator/corpus holds: a
// metadata line, then one {"atMs","offset","data"} record per recorded write.
// Nothing here interprets the bytes — the emulator under test does that — so a
// difference between two drivers is a difference in the emulator, not in this
// package.
package corpus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Capture is one recorded terminal session.
type Capture struct {
	Name    string
	Cols    int
	Rows    int
	Stream  []byte
	Offsets []int // the recorder's own write boundaries, in ascending order
}

// Load reads one capture file.
func Load(path string) (*Capture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var meta struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := json.Unmarshal(lines[0], &meta); err != nil {
		return nil, fmt.Errorf("%s: metadata line: %w", path, err)
	}
	c := &Capture{
		Name: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Cols: meta.Cols,
		Rows: meta.Rows,
	}
	for _, ln := range lines[1:] {
		var ch struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(ln, &ch); err != nil {
			return nil, fmt.Errorf("%s: chunk line: %w", path, err)
		}
		c.Stream = append(c.Stream, ch.Data...)
		c.Offsets = append(c.Offsets, len(c.Stream))
	}
	return c, nil
}

// LoadAll reads every capture in a directory, sorted by name.
func LoadAll(dir string) ([]*Capture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	out := make([]*Capture, 0, len(paths))
	for _, p := range paths {
		c, err := Load(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Partitions returns the write boundaries for a feed shape: one write, the
// recorder's own writes, an even split into parts, or one byte per write.
func (c *Capture) Partitions(parts int, bytewise bool) []int {
	if bytewise {
		offs := make([]int, 0, len(c.Stream))
		for i := range c.Stream {
			offs = append(offs, i+1)
		}
		return offs
	}
	if parts <= 1 {
		return []int{len(c.Stream)}
	}
	offs := make([]int, 0, parts)
	for i := 1; i <= parts; i++ {
		offs = append(offs, len(c.Stream)*i/parts)
	}
	return offs
}

// Grid is the part of a terminal the screen renderer needs.
type Grid interface {
	Cols() int
	Rows() int
	// Cell returns the grapheme cluster, its width class and whether the cell
	// holds text — the library's own answer, unnormalised.
	Cell(x, y int) (grapheme string, width int, hasText bool)
}

// WidthOf maps a cell's width class to the column footprint a renderer
// advances by. The values are the library's (narrow, wide, spacer tail,
// spacer head), the same on both sides of a comparison.
func WidthOf(w int) int {
	switch w {
	case 1:
		return 2
	case 2, 3:
		return 0
	default:
		return 1
	}
}

// Render returns one string per row: the cell's grapheme and its width, or "."
// for a cell with no text. This is the whole final screen in a form a diff can
// point at.
func Render(g Grid) []string {
	rows := make([]string, 0, g.Rows())
	for y := range g.Rows() {
		var sb strings.Builder
		for x := range g.Cols() {
			gr, w, has := g.Cell(x, y)
			if !has || gr == "" {
				sb.WriteString(". ")
				continue
			}
			fmt.Fprintf(&sb, "%q/%d ", gr, WidthOf(w))
		}
		rows = append(rows, strings.TrimRight(sb.String(), " "))
	}
	return rows
}
