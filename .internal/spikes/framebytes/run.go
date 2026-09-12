package framebytes

import (
	"math"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
)

// Config is one point of the sweep: how often a frame is delivered.
type Config struct {
	// FPS is the delivered frame rate. Ignored when EveryWrite is set.
	FPS float64
	// EveryWrite delivers one frame per recorded write — no coalescing at
	// all, and the upper bound of what the frame protocol can cost.
	EveryWrite bool
}

// Label names the point for the report's tables.
func (c Config) Label() string {
	if c.EveryWrite {
		return "every write"
	}
	return trimFloat(c.FPS) + " fps"
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) {
		return strconv2(int(f))
	}
	return strconvFloat(f)
}

// Metrics is everything measured for one capture at one frame rate.
type Metrics struct {
	Name        string
	Label       string
	Cols, Rows  int
	DurationMs  int
	Writes      int
	Frames      int
	Raw         int // what the program wrote; what nocx ships today
	Positional  int // frame stream from a fixed-coordinate diff
	ScrollAware int // the same, recognising a scrolled screen
	Omitted     int // content no delivered frame carried, as card lines
	OmittedRows int
	// OmittedRetained is the part of Omitted that entered retained history and
	// a card fetch would therefore actually carry; the rest is content
	// overwritten in place, which no terminal retains.
	OmittedRetained     int
	OmittedRetainedRows int
	ScrollOps           int // scroll operations the scroll-aware encoder emitted
	ScrollRows          int
}

// replay drives one emulator through a capture.
type replay struct {
	emu *vt.Emulator
	alt bool
}

// newReplay starts an emulator at the capture's geometry and drains its reply
// pipe: x/vt blocks a write when nothing reads its replies (ADR-0041).
func newReplay(cols, rows int) *replay {
	e := vt.NewEmulator(cols, rows)
	r := &replay{emu: e}
	e.SetCallbacks(vt.Callbacks{AltScreen: func(on bool) { r.alt = on }})
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := e.Read(buf); err != nil {
				return
			}
		}
	}()
	return r
}

func (r *replay) write(b []byte) { _, _ = r.emu.Write(b) }

func (r *replay) snapshot() *Frame { return snapshot(r.emu, r.alt) }

// Measure replays the capture once, delivering frames at the configured rate
// to both encoders, and then replays it again to count what no delivered frame
// ever carried.
func Measure(c *Capture, cfg Config) *Metrics {
	m := &Metrics{
		Name: c.Name, Label: cfg.Label(),
		Cols: c.Cols, Rows: c.Rows,
		DurationMs: c.DurationMs(), Writes: len(c.Chunks),
	}
	ticks := tickTimes(cfg, m.DurationMs)

	r := newReplay(c.Cols, c.Rows)
	pos := NewEncoder(c.Cols, c.Rows)
	scroll := NewEncoder(c.Cols, c.Rows)
	appeared := make(map[uint64]struct{}, 4096)

	deliver := func() {
		f := r.snapshot()
		for y := range f.H {
			if !f.RowBlank(y) {
				appeared[f.RowHash(y)] = struct{}{}
			}
		}
		m.Positional += len(pos.Frame(f, false))
		m.ScrollAware += len(scroll.Frame(f, true))
		m.ScrollOps += scroll.ScrollOps
		m.ScrollRows += scroll.ScrollRows
		m.Frames++
	}

	ti := 0
	for _, ch := range c.Chunks {
		if !cfg.EveryWrite {
			for ti < len(ticks) && ticks[ti] < ch.AtMs {
				deliver()
				ti++
			}
		}
		r.write(ch.Data)
		m.Raw += len(ch.Data)
		if cfg.EveryWrite {
			deliver()
		}
	}
	for ti < len(ticks) {
		deliver()
		ti++
	}

	m.Omitted, m.OmittedRows, m.OmittedRetained, m.OmittedRetainedRows = measureOmitted(c, appeared)
	return m
}

// Streams replays the capture once and returns the three byte streams a client
// would receive: the raw PTY output, the positional frame stream and the
// scroll-aware one. It exists for the compression note, which is measured
// separately because it is not part of the accounting.
func Streams(c *Capture, cfg Config) (raw, positional, scroll []byte) {
	r := newReplay(c.Cols, c.Rows)
	pos := NewEncoder(c.Cols, c.Rows)
	aware := NewEncoder(c.Cols, c.Rows)
	ticks := tickTimes(cfg, c.DurationMs())

	deliver := func() {
		f := r.snapshot()
		positional = append(positional, pos.Frame(f, false)...)
		scroll = append(scroll, aware.Frame(f, true)...)
	}
	ti := 0
	for _, ch := range c.Chunks {
		if !cfg.EveryWrite {
			for ti < len(ticks) && ticks[ti] < ch.AtMs {
				deliver()
				ti++
			}
		}
		r.write(ch.Data)
		raw = append(raw, ch.Data...)
		if cfg.EveryWrite {
			deliver()
		}
	}
	for ti < len(ticks) {
		deliver()
		ti++
	}
	return raw, positional, scroll
}

// cardLineBytes is what one retained line costs a client fetching a card: the
// line's cells from column 0 with the same style rules the frames use, then a
// line break.
func cardLineBytes(f *Frame, y int) int {
	return cardRowBytes(f.Cells[y*f.W : (y+1)*f.W])
}

// cardRowBytes prices a row of cells as one card line: style runs between the
// cells, one space per blank, then CRLF. Trailing padding is trimmed first, by
// the same test a terminal uses to decide what a scrollback line keeps: a plain
// blank is padding, while a blank that carries a style is part of the line and
// is priced, because x/vt retains it.
func cardRowBytes(cells []Cell) int {
	last := -1
	for x := range cells {
		if !isPadding(cells[x]) {
			last = x
		}
	}
	if last < 0 {
		return 0
	}
	cells = cells[:last+1]
	n := 0
	var pen uv.Style
	known := false
	for x := 0; x < len(cells); {
		c := cells[x]
		if c.Width == covered {
			x++
			continue
		}
		if !known || !c.Style.Equal(&pen) {
			n += len(ansi.ResetStyle)
			if !c.Style.IsZero() {
				n += len(c.Style.String())
			}
			pen = c.Style
			known = true
		}
		if c.Content == "" {
			n++
		} else {
			n += len(c.Content)
		}
		x += c.Width
	}
	return n + 2 // CRLF
}

// isPadding reports whether a cell is plain trailing padding: a blank with no
// style and no width of its own.
func isPadding(c Cell) bool {
	return (c.Content == " " || c.Content == "") && c.Style.IsZero() && c.Width <= 1
}

// measureOmitted counts the fourth number: every distinct row content the
// emulator held at some instant that appears in no delivered frame, and which a
// card fetch would therefore have to bring later. It is a union of two sources,
// because neither alone sees all of that content:
//
//   - the screen at every write boundary, which is a state the emulator was
//     actually in, and which no chunk-boundary sample can see past; and
//   - the scrollback, which catches the lines that scrolled off *inside* a
//     single write and were therefore never a state any frame could carry.
//
// It reports two numbers. omitted is the union — the brief's definition — and
// retained is the part of it that entered retained history, which is what a card
// actually carries. The difference is content overwritten in place, which no
// terminal retains: a fidelity loss, not a card byte. retained is always a
// subset of omitted, and the tests assert it.
func measureOmitted(c *Capture, appeared map[uint64]struct{}) (omitted, omittedRows, retained, retainedRows int) {
	counted := make(map[uint64]struct{})

	// The states the emulator was in, one per write.
	r := newReplay(c.Cols, c.Rows)
	for _, ch := range c.Chunks {
		r.write(ch.Data)
		f := r.snapshot()
		for y := range f.H {
			if f.RowBlank(y) {
				continue
			}
			h := f.RowHash(y)
			if _, ok := appeared[h]; ok {
				continue
			}
			if _, ok := counted[h]; ok {
				continue
			}
			counted[h] = struct{}{}
			omitted += cardLineBytes(f, y)
			omittedRows++
		}
	}

	// The lines that entered retained history, including those that scrolled
	// off between two writes.
	r = newReplay(c.Cols, c.Rows)
	retainedSeen := make(map[uint64]struct{})
	prev := 0
	for _, ch := range c.Chunks {
		r.write(ch.Data)
		lines := r.emu.Scrollback().Lines()
		if len(lines) <= prev {
			// Nothing was pushed, or the buffer is at its maximum and a push
			// evicted the oldest line instead of growing: the new lines cannot
			// be told apart by index, so they are left uncounted. No capture
			// here comes near the 10,000-line default, which a test checks
			// rather than assumes.
			prev = len(lines)
			continue
		}
		for _, line := range lines[prev:] {
			cells := normalizeRow(line, c.Cols)
			if rowIsBlank(cells) {
				continue // a blank line carries nothing to fetch
			}
			h := hashRow(cells)
			if _, ok := appeared[h]; ok {
				continue
			}
			if _, ok := retainedSeen[h]; ok {
				continue // one card line per distinct row, however often it was pushed
			}
			retainedSeen[h] = struct{}{}
			retained += cardRowBytes(cells)
			retainedRows++
			if _, ok := counted[h]; ok {
				continue
			}
			counted[h] = struct{}{}
			omitted += cardRowBytes(cells)
			omittedRows++
		}
		prev = len(lines)
	}
	return omitted, omittedRows, retained, retainedRows
}

// tickTimes lists the millisecond instants a frame is delivered at: k/fps for
// k = 1, 2, …, plus one final frame at the end of the recording so the client
// is never left holding a stale screen.
func tickTimes(cfg Config, durationMs int) []int {
	if cfg.EveryWrite || cfg.FPS <= 0 || durationMs <= 0 {
		return nil
	}
	var out []int
	for k := 1; ; k++ {
		t := int(math.Round(float64(k) * 1000 / cfg.FPS))
		if t > durationMs {
			break
		}
		if len(out) > 0 && t == out[len(out)-1] {
			continue
		}
		out = append(out, t)
		if len(out) > 1_000_000 {
			break
		}
	}
	if len(out) == 0 || out[len(out)-1] != durationMs {
		out = append(out, durationMs)
	}
	return out
}
