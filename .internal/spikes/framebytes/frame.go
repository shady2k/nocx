package framebytes

import (
	"fmt"
	"hash/fnv"
	"image/color"
	"io"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	vt "github.com/charmbracelet/x/vt"
)

// covered marks the second half of a wide cell: it carries no content of its
// own, is never emitted, and is only ever compared so that a baseline holding
// a wide cell where the target holds a blank is not mistaken for equality.
const covered = -1

// Cell is one grid position after normalization: a grapheme, the columns it
// occupies, and its style.
type Cell struct {
	Content string
	Width   int
	Style   uv.Style
}

func blankCell() Cell { return Cell{Content: " ", Width: 1} }

func sameCell(a, b Cell) bool {
	return a.Content == b.Content && a.Width == b.Width && a.Style.Equal(&b.Style)
}

// Frame is the whole visible screen at one instant: what a client renders, and
// what an encoder diffs against.
type Frame struct {
	W, H    int
	Alt     bool
	CursorX int
	CursorY int
	Cells   []Cell

	blank []bool // lazily computed cache of RowBlank
}

// NewFrame returns a blank w×h screen.
func NewFrame(w, h int) *Frame {
	f := &Frame{W: w, H: h, Cells: make([]Cell, w*h)}
	for i := range f.Cells {
		f.Cells[i] = blankCell()
	}
	return f
}

func (f *Frame) At(x, y int) Cell {
	if x < 0 || x >= f.W || y < 0 || y >= f.H {
		return blankCell()
	}
	return f.Cells[y*f.W+x]
}

// RowHash is a content-and-style hash of one row, used only to dedupe rows
// when counting what never reached a frame.
func (f *Frame) RowHash(y int) uint64 {
	h := fnv.New64a()
	for x := range f.W {
		c := f.At(x, y)
		writeCellHash(h, c)
	}
	return h.Sum64()
}

// hashRow is the same hash over a row that is not a frame's: a scrollback line,
// padded to the frame's width so that a retained line and the row it scrolled
// off hash alike.
func hashRow(cells []Cell) uint64 {
	h := fnv.New64a()
	for _, c := range cells {
		writeCellHash(h, c)
	}
	return h.Sum64()
}

func writeCellHash(h io.Writer, c Cell) {
	io.WriteString(h, c.Content)
	_, _ = h.Write([]byte{byte(c.Width + 2)}) // +2 so covered (-1) maps to 1
	hashStyle(h, &c.Style)
}

// RowBlank reports whether every cell of the row is a plain blank, which is a
// row with nothing to recover. The answer is cached: the encoders ask it once
// per row per candidate shift.
func (f *Frame) RowBlank(y int) bool {
	if y < 0 || y >= f.H {
		return true
	}
	if f.blank == nil {
		f.blank = make([]bool, f.H)
		for r := range f.H {
			f.blank[r] = f.rowBlank(r)
		}
	}
	return f.blank[y]
}

func (f *Frame) rowBlank(y int) bool { return rowIsBlank(f.Cells[y*f.W : (y+1)*f.W]) }

// rowIsBlank reports whether a row is plain blanks throughout: nothing to
// recover, and nothing worth sending.
func rowIsBlank(cells []Cell) bool {
	for _, c := range cells {
		if c.Content != " " && c.Content != "" {
			return false
		}
		if !c.Style.IsZero() {
			return false
		}
	}
	return true
}

// RowText is the row's visible text, trailing blanks trimmed.
func (f *Frame) RowText(y int) string {
	var b strings.Builder
	for x := range f.W {
		c := f.At(x, y)
		if c.Width == covered {
			continue
		}
		b.WriteString(c.Content)
	}
	return strings.TrimRight(b.String(), " ")
}

func hashStyle(h io.Writer, s *uv.Style) {
	fmt.Fprintf(h, "%d/%d/", s.Attrs, s.Underline)
	hashColor(h, s.Fg)
	hashColor(h, s.Bg)
	hashColor(h, s.UnderlineColor)
}

func hashColor(h io.Writer, c color.Color) {
	if c == nil {
		io.WriteString(h, "-")
		return
	}
	r, g, b, _ := c.RGBA()
	fmt.Fprintf(h, "%T:%d,%d,%d;", c, r, g, b)
}

// snapshot reads the emulator's visible screen. covered positions are marked
// rather than dropped so a wide cell's tail is distinguishable from a blank.
func snapshot(e *vt.Emulator, alt bool) *Frame {
	w, h := e.Width(), e.Height()
	f := NewFrame(w, h)
	f.Alt = alt
	for y := range h {
		for x := 0; x < w; {
			c := normalize(e.CellAt(x, y))
			f.Cells[y*w+x] = c
			for i := 1; i < c.Width; i++ {
				if x+i < w {
					f.Cells[y*w+x+i] = Cell{Width: covered}
				}
			}
			x += c.Width
		}
	}
	pos := e.CursorPosition()
	f.CursorX, f.CursorY = pos.X, pos.Y
	return f
}

// normalize turns an emulator cell into the frame's cell vocabulary: a nil or
// empty cell is a plain blank of one column, and a zero-width grapheme (a
// combining mark stored beside its base) is one column of its own.
func normalize(c *uv.Cell) Cell {
	if c == nil || c.Content == "" {
		return blankCell()
	}
	w := c.Width
	if w < 1 {
		w = 1
	}
	return Cell{Content: c.Content, Width: w, Style: c.Style}
}

// normalizeRow does the same for a scrollback line, which x/vt trims of its
// trailing blank cells and which is therefore padded back to a screen row's
// shape so a retained line and the row it scrolled off can be compared.
func normalizeRow(line []uv.Cell, w int) []Cell {
	out := make([]Cell, w)
	for x := range out {
		if x < len(line) {
			out[x] = normalize(&line[x])
		} else {
			out[x] = blankCell()
		}
	}
	return out
}
