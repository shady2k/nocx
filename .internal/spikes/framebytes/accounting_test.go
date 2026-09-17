package framebytes

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// redStyle is one styled cell value: basic red on the default background.
func redStyle() uv.Style { return uv.Style{Fg: ansi.BasicColor(1)} }

// TestOmittedCountsWhatNoFrameCarried is the fourth number: content the
// terminal held between two delivered frames and no frame ever showed.
func TestOmittedCountsWhatNoFrameCarried(t *testing.T) {
	c := &Capture{Name: "burst", Cols: 40, Rows: 10}
	// The screen filled, then thirty more lines pushed all of it off before a
	// frame was ever delivered.
	fill := strings.Repeat("first screen\r\n", 10)
	c.Chunks = []Chunk{
		{AtMs: 0, Data: []byte(fill)},
		{AtMs: 5, Data: []byte(strings.Repeat("later line\r\n", 30))},
	}
	coalesced := Measure(c, Config{FPS: 15})
	if coalesced.OmittedRows == 0 || coalesced.Omitted == 0 {
		t.Fatalf("coalesced: omitted=%d rows=%d, want the first screen counted as unfetched",
			coalesced.Omitted, coalesced.OmittedRows)
	}
	// At one frame per write nothing can be missed: every state is delivered.
	every := Measure(c, Config{EveryWrite: true})
	if every.Omitted != 0 || every.OmittedRows != 0 {
		t.Fatalf("every write: omitted=%d rows=%d, want 0", every.Omitted, every.OmittedRows)
	}
	if every.Raw != coalesced.Raw {
		t.Fatalf("raw differs between runs: %d vs %d", every.Raw, coalesced.Raw)
	}
}

// TestAccountingEncodings pins the byte-accounting rules the report states, so
// the definition and the encoder cannot drift apart.
func TestAccountingEncodings(t *testing.T) {
	if got := string(appendCUP(nil, 0, 0, 100)); got != "\x1b[H" {
		t.Errorf("cursor at the origin: %q", got)
	}
	if got := string(appendCUP(nil, 5, 0, 120)); got != "\x1b[6H" {
		t.Errorf("cursor at column 1: %q", got)
	}
	if got := string(appendCUP(nil, 0, 99, 100)); got != "\x1b[1;100H" {
		t.Errorf("cursor at the last column: %q", got)
	}
	if got := string(appendScrollUp(nil, 12)); got != "\x1b[12S" {
		t.Errorf("scroll up: %q", got)
	}

	// One cell changed, default style, and the caret already where the write
	// leaves it: a cursor move and one byte of content.
	base := NewFrame(10, 2)
	next := NewFrame(10, 2)
	next.Cells[1*10+2] = Cell{Content: "x", Width: 1}
	next.CursorX, next.CursorY = 3, 1
	enc := NewEncoder(10, 2)
	enc.Frame(base, false)
	if got := enc.Frame(next, false); string(got) != "\x1b[2;3Hx" {
		t.Errorf("single cell frame: %q", got)
	}

	// A style change is emitted once per run, not once per cell.
	red := redStyle()
	styled := NewFrame(10, 2)
	for x := range 5 {
		styled.Cells[x] = Cell{Content: "r", Width: 1, Style: red}
	}
	styled.CursorX = 5 // the caret sits right after the run
	enc2 := NewEncoder(10, 2)
	enc2.Frame(base, false)
	got := enc2.Frame(styled, false)
	want := "\x1b[m" + red.String() + "rrrrr"
	if string(got) != want {
		t.Errorf("styled run: %q, want %q", got, want)
	}
	if n := strings.Count(string(got), "\x1b["); n != 2 {
		t.Errorf("styled run emitted %d sequences for one run: %q", n, got)
	}

	// Blanks cost a space each — no erase-in-line shortcut is counted — and
	// the caret is moved back to where the target holds it.
	blanked := NewFrame(10, 2)
	enc3 := NewEncoder(10, 2)
	enc3.Frame(base, false)
	enc3.Frame(styled, false)
	got3 := enc3.Frame(blanked, false)
	if want3 := "\x1b[H" + "\x1b[m" + "     " + "\x1b[H"; string(got3) != want3 {
		t.Errorf("five blanks: %q, want %q", got3, want3)
	}
}

// TestOmittedCoversInsideWriteScrolls is the case a chunk-boundary sample
// cannot see. One write pushes forty distinct lines through a ten-row screen:
// the only state the emulator was ever observed in is the last one, so a sample
// of states reports nothing omitted — yet thirty lines entered scrollback and no
// delivered frame ever carried them, so a card would have to. The lines are
// distinct so that the scrolled-off content is not also the visible content.
func TestOmittedCoversInsideWriteScrolls(t *testing.T) {
	c := &Capture{Name: "one-write", Cols: 20, Rows: 10}
	var b strings.Builder
	for i := range 40 {
		fmt.Fprintf(&b, "line %02d\r\n", i)
	}
	c.Chunks = []Chunk{{AtMs: 0, Data: []byte(b.String())}}
	m := Measure(c, Config{EveryWrite: true})
	if m.Frames != 1 {
		t.Fatalf("frames = %d, want 1", m.Frames)
	}
	// Thirty-one, not thirty: the screen holds nine lines and the blank row the
	// cursor sits on, so of forty lines one more than a full screen scrolled off.
	if m.OmittedRows != 31 {
		t.Fatalf("omitted rows = %d, want the 31 rows that scrolled off inside the write", m.OmittedRows)
	}
	if m.Omitted != m.OmittedRetained || m.OmittedRows != m.OmittedRetainedRows {
		t.Fatalf("omitted %d/%d vs retained %d/%d: with one write every omitted row is retained",
			m.Omitted, m.OmittedRows, m.OmittedRetained, m.OmittedRetainedRows)
	}
}

// TestNothingIsRetainedOnTheAlternateScreen: an alternate-screen program's rows
// are overwritten in place, so a card carries none of them even though no frame
// carried them either.
func TestNothingIsRetainedOnTheAlternateScreen(t *testing.T) {
	c := &Capture{Name: "alt", Cols: 20, Rows: 4}
	var b strings.Builder
	b.WriteString("\x1b[?1049h")
	for i := range 6 {
		b.WriteString("\x1b[1;1H") // in place, never scrolling
		fmt.Fprintf(&b, "count %d", i)
	}
	c.Chunks = []Chunk{{AtMs: 0, Data: []byte(b.String())}}
	m := Measure(c, Config{FPS: 60})
	if m.OmittedRetained != 0 || m.OmittedRetainedRows != 0 {
		t.Fatalf("retained = %d bytes / %d rows on the alternate screen, want 0",
			m.OmittedRetained, m.OmittedRetainedRows)
	}
}

// TestRetainedIsSubsetOfOmitted holds the two counts together: retained is a
// part of omitted, in both bytes and rows, on every capture.
func TestRetainedIsSubsetOfOmitted(t *testing.T) {
	caps := append(corpusCaps(t), Hero(1))
	for _, cfg := range []Config{{FPS: 30}, {FPS: 60}, {EveryWrite: true}} {
		for _, c := range caps {
			m := Measure(c, cfg)
			if m.OmittedRetained > m.Omitted || m.OmittedRetainedRows > m.OmittedRows {
				t.Errorf("%s at %s: retained %d/%d exceeds omitted %d/%d",
					c.Name, cfg.Label(), m.OmittedRetained, m.OmittedRetainedRows, m.Omitted, m.OmittedRows)
			}
		}
	}
}

// TestScrollbackNeverReachesItsMaximum guards the retained measurement's one
// assumption: newly pushed lines are identified by index, which fails if the
// buffer evicts.
func TestScrollbackNeverReachesItsMaximum(t *testing.T) {
	caps := append(corpusCaps(t), Hero(1))
	for _, c := range caps {
		r := newReplay(c.Cols, c.Rows)
		max := 0
		for _, ch := range c.Chunks {
			r.write(ch.Data)
			max = maxInt(max, r.emu.ScrollbackLen())
		}
		if max >= 10000 {
			t.Errorf("%s: scrollback reached %d lines, at the default maximum", c.Name, max)
		}
	}
}

// TestRetainedDeduplicatesRepeatedLines: a card carries a line once, so content
// that scrolled off sixty times is priced twice, not sixty times. The write ends
// with three lines that are the only ones any state boundary ever holds.
func TestRetainedDeduplicatesRepeatedLines(t *testing.T) {
	c := &Capture{Name: "repeat", Cols: 20, Rows: 3}
	var b strings.Builder
	for range 30 {
		b.WriteString("alpha\r\nbeta\r\n")
	}
	// The screen is cleared afterwards, so the repeated lines are in no state
	// boundary a frame can see and every one of the sixty pushes is retained.
	b.WriteString("\x1b[2J\x1b[1;1Htail")
	c.Chunks = []Chunk{{AtMs: 0, Data: []byte(b.String())}}
	m := Measure(c, Config{EveryWrite: true})
	if m.OmittedRows != 2 || m.OmittedRetainedRows != 2 {
		t.Fatalf("omitted %d rows / retained %d rows, want 2 and 2: two distinct lines scrolled off sixty times",
			m.OmittedRows, m.OmittedRetainedRows)
	}
	want := cardRowBytes(rowOf("alpha")) + cardRowBytes(rowOf("beta"))
	if m.OmittedRetained != want {
		t.Fatalf("retained = %d bytes, want %d for two card lines", m.OmittedRetained, want)
	}
	if m.Omitted != m.OmittedRetained {
		t.Fatalf("omitted = %d, want it equal to retained %d here", m.Omitted, m.OmittedRetained)
	}
}

// rowOf builds one frame row of plain text for pricing comparisons.
func rowOf(s string) []Cell {
	cells := make([]Cell, 20)
	for x := range cells {
		cells[x] = blankCell()
	}
	for i, r := range s {
		cells[i] = Cell{Content: string(r), Width: 1}
	}
	return cells
}

// TestStyledTrailingBlanksArePriced: a blank cell that carries a style is part
// of a retained line — x/vt keeps it, unlike the plain blanks it trims — so the
// card pays for it. Trimming on content alone would underprice every styled
// region that ends in blanks.
func TestStyledTrailingBlanksArePriced(t *testing.T) {
	plain := rowOf("ab")
	styled := rowOf("ab")
	styled[2] = Cell{Content: " ", Width: 1, Style: uv.Style{Bg: ansi.BasicColor(4)}}
	if got, want := cardRowBytes(styled), cardRowBytes(plain); got <= want {
		t.Fatalf("styled row priced %d, plain row %d: the styled trailing blank was trimmed", got, want)
	}
	// The difference is the style that keeps the blank plus the blank itself: a
	// reset and the SGR for the background, then one space.
	// The row is "ab", then the styled blank, then padding: so the delta over
	// the plain row is exactly the style that keeps the blank — a reset plus the
	// SGR for the background — and the blank itself.
	want := cardRowBytes(plain) + len(ansi.ResetStyle) + len(styled[2].Style.String()) + 1
	if got := cardRowBytes(styled); got != want {
		t.Fatalf("styled row priced %d, want %d", got, want)
	}
	t.Logf("plain=%d styled=%d reset=%d sgr=%d (%q)", cardRowBytes(plain), cardRowBytes(styled),
		len(ansi.ResetStyle), len(styled[2].Style.String()), styled[2].Style.String())
}
