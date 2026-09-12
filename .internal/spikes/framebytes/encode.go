package framebytes

import (
	"strconv"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Accounting is how every byte count in REPORT.md is built. A number is the
// length of a stream produced by these rules, with no compression layer and no
// transport framing:
//
//   - Cursor position: absolute CUP — "\x1b[H" for the origin, "\x1b[<row>H"
//     when the column is 1, otherwise "\x1b[<row>;<col>H". One CUP precedes
//     each run whose first cell is not already under the cursor.
//   - Cell content: the grapheme as stored, one byte for a blank cell. No
//     erase-in-line, no repeat, no insert/delete, so a run of blanks costs one
//     space each. That inflates every frame number here rather than flattering
//     it. A wide grapheme is emitted once and covers both its columns.
//   - Style: a terminal's pen. A run whose style differs from the pen emits a
//     reset ("\x1b[m") and then the full SGR for the new style; a run that
//     continues the pen's style emits nothing. Styles are never diffed
//     parameter by parameter, which again inflates the frame numbers.
//   - Scroll: one scroll-up, "\x1b[<k>S", emitted only when the target is
//     provably the baseline shifted up by k rows and the scroll costs less
//     than the plain diff.
//   - Screen: one "\x1b[?1049h" or "\x1b[?1049l" when the alternate screen is
//     entered or left; the entering frame repaints the cleared screen.
//   - Cursor: at most one final CUP per frame, to the target's cursor.
//
// The encoder is also its own client model: each target frame is diffed
// against the screen the client actually holds. Round-trip tests apply the
// emitted bytes to a fresh x/vt emulator and require its screen to equal the
// target exactly.

// Encoder turns a sequence of target frames into a byte stream a client can
// apply. It holds the client's state — both screens, the pen, the cursor — so
// each frame is diffed against what that client actually has.
type Encoder struct {
	w, h int

	main *Frame // the client's normal screen
	alt  *Frame // the client's alternate screen, cleared on entry
	cur  *Frame // whichever of the two is active

	altOn        bool
	pen          uv.Style
	penKnown     bool
	cx, cy       int // the client's cursor on the active screen
	mainX, mainY int
	altX, altY   int

	// Per-frame statistics.
	ScrollOps, ScrollRows int
}

// encState is the part of the client a diff changes: the pen and the cursor.
type encState struct {
	pen    uv.Style
	known  bool
	cx, cy int
}

// NewEncoder returns an encoder for a w×h screen whose client is blank and
// whose pen is the default style: a fresh client's attributes are defined, so
// the first default-styled run needs no reset.
func NewEncoder(w, h int) *Encoder {
	e := &Encoder{w: w, h: h, main: NewFrame(w, h), alt: NewFrame(w, h)}
	e.alt.Alt = true
	e.cur = e.main
	e.penKnown = true
	return e
}

// Frame emits the bytes that take the client from its current screen to t.
// scrollAware enables the scroll operation.
func (e *Encoder) Frame(t *Frame, scrollAware bool) []byte {
	e.ScrollOps, e.ScrollRows = 0, 0
	b := make([]byte, 0, 4096)
	b = e.switchScreen(b, t)

	base := e.cur
	st := encState{pen: e.pen, known: e.penKnown, cx: e.cx, cy: e.cy}
	shift := 0
	if scrollAware {
		shift = scrollShift(base, t)
	}
	if shift > 0 {
		trial := st
		withScroll := diff(appendScrollUp(nil, shift), base, t, shift, &trial, e.w)
		plain := diff(nil, base, t, 0, &st, e.w)
		if len(withScroll) < len(plain) {
			b = append(b, withScroll...)
			st = trial
			e.ScrollOps++
			e.ScrollRows += shift
		} else {
			b = append(b, plain...)
		}
	} else {
		b = diff(b, base, t, 0, &st, e.w)
	}

	// Leave the caret where the producing terminal has it.
	tx := t.CursorX
	if tx >= e.w {
		tx = e.w - 1 // a cursor in the phantom column is the last column
	}
	if tx < 0 {
		tx = 0
	}
	if tx != st.cx || t.CursorY != st.cy {
		b = appendCUP(b, t.CursorY, tx, e.w)
		st.cx, st.cy = tx, t.CursorY
	}

	e.pen, e.penKnown, e.cx, e.cy = st.pen, st.known, st.cx, st.cy
	if t.Alt {
		e.alt = t
	} else {
		e.main = t
	}
	e.cur = t
	return b
}

// switchScreen emits and applies the alternate-screen transition, if the
// target is on the other screen than the client.
func (e *Encoder) switchScreen(b []byte, t *Frame) []byte {
	if t.Alt == e.altOn {
		return b
	}
	if t.Alt {
		b = append(b, "\x1b[?1049h"...)
		e.altOn = true
		e.alt = NewFrame(e.w, e.h)
		e.alt.Alt = true
		e.cur = e.alt
		e.mainX, e.mainY = e.cx, e.cy // the normal screen keeps its cursor
		e.cx, e.cy = 0, 0             // 1049h clears the alternate screen and homes the cursor
	} else {
		b = append(b, "\x1b[?1049l"...)
		e.altOn = false
		e.altX, e.altY = e.cx, e.cy
		e.cur = e.main
		e.cx, e.cy = e.mainX, e.mainY
	}
	e.penKnown = false // a screen switch saves and restores state; re-assert the pen
	return b
}

// diff appends the cell updates that take the client from the baseline
// shifted up by shift rows to t, and advances st to the state those updates
// leave the client in.
func diff(b []byte, base, t *Frame, shift int, st *encState, w int) []byte {
	for y := range t.H {
		x := 0
		for x < t.W {
			tc := t.At(x, y)
			if sameCell(base.At(x, y+shift), tc) {
				x += tc.Width
				continue
			}
			if st.cx != x || st.cy != y {
				b = appendCUP(b, y, x, w)
				st.cx, st.cy = x, y
			}
			for x < t.W {
				tc = t.At(x, y)
				if sameCell(base.At(x, y+shift), tc) {
					break
				}
				b = st.emitStyle(b, tc.Style)
				b = appendCell(b, tc)
				st.cx, st.cy = x+tc.Width, y
				x += tc.Width
			}
		}
	}
	return b
}

func (s *encState) emitStyle(b []byte, want uv.Style) []byte {
	if s.known && want.Equal(&s.pen) {
		return b
	}
	b = append(b, ansi.ResetStyle...)
	if !want.IsZero() {
		b = append(b, want.String()...)
	}
	s.pen = want
	s.known = true
	return b
}

// appendCell writes one cell's content: its grapheme, or a single space for a
// blank.
func appendCell(b []byte, c Cell) []byte {
	if c.Width == covered {
		return b
	}
	if c.Content == "" {
		return append(b, ' ')
	}
	return append(b, c.Content...)
}

func appendCUP(b []byte, y, x, w int) []byte {
	col := min(max(x+1, 1), w)
	row := max(y+1, 1)
	b = append(b, "\x1b["...)
	switch {
	case row == 1 && col == 1:
		return append(b, 'H')
	case col == 1:
		return append(append(b, strconv.AppendInt(nil, int64(row), 10)...), 'H')
	default:
		b = strconv.AppendInt(b, int64(row), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(col), 10)
		return append(b, 'H')
	}
}

func appendScrollUp(b []byte, k int) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(k), 10)
	return append(b, 'S')
}

// scrollShift proposes the shift k that the target most looks like, or 0 when
// no shift explains any of it.
//
// A terminal that scrolled k rows does not hold a grid shifted by exactly k.
// The cursor sits on a blank row below the last line written, so the row that
// scrolled into a given position is one blank row short of where a whole-grid
// shift would put it: at 60 fps on a 100×40 screen fed full lines, the screen
// holds 39 lines of text and a blank cursor row, and only 29 of the 30 rows a
// shift of 10 could explain actually match. So this scores every k by how many
// rows of the target it explains and takes the best one; whether that shift is
// worth emitting is then decided on bytes, by the caller, which compares the
// scroll stream against the plain diff. Correctness never depends on the
// proposal: whatever k is chosen, the diff after the scroll is computed against
// the client's real screen.
func scrollShift(base, t *Frame) int {
	if base.W != t.W || base.H != t.H || base.H < 2 {
		return 0
	}
	bh := make([]uint64, base.H)
	th := make([]uint64, t.H)
	for y := range base.H {
		bh[y] = base.RowHash(y)
		th[y] = t.RowHash(y)
	}
	best, bestScore := 0, 0
	for k := 1; k < t.H; k++ {
		score := 0
		for y := 0; y+k < t.H; y++ {
			if t.RowBlank(y) {
				continue // a blank row of the target explains nothing
			}
			if th[y] == bh[y+k] {
				score++
			}
		}
		if score > 0 && score >= bestScore {
			best, bestScore = k, score
		}
	}
	return best
}
