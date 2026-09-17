package framebytes

import (
	"fmt"
	"strings"
	"testing"

	vt "github.com/charmbracelet/x/vt"
)

// client is an independent terminal that applies an encoder's byte stream, so
// a round-trip compares the encoder's intent against a real emulator rather
// than against the encoder's own model of a cell.
type client struct {
	emu *vt.Emulator
	alt bool
}

func newClient(w, h int) *client {
	c := &client{emu: vt.NewEmulator(w, h)}
	c.emu.SetCallbacks(vt.Callbacks{AltScreen: func(on bool) { c.alt = on }})
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := c.emu.Read(buf); err != nil {
				return
			}
		}
	}()
	return c
}

func (c *client) apply(b []byte) {
	if _, err := c.emu.Write(b); err != nil {
		panic(err)
	}
}

func (c *client) snapshot() *Frame { return snapshot(c.emu, c.alt) }

// corpusCaps loads the recorded corpus the spike measures. The path is
// relative to this module, and the corpus is read-only to this spike.
func corpusCaps(t *testing.T) []*Capture {
	t.Helper()
	caps, err := LoadCorpus("../emulator/corpus")
	if err != nil {
		t.Fatalf("loading corpus: %v", err)
	}
	return caps
}

// TestRoundTripCorpus replays every recorded capture through both encoders and
// requires the client's screen to equal the producing terminal's screen after
// every delivered frame. A frame stream that is short because it is wrong
// would fail here.
func TestRoundTripCorpus(t *testing.T) {
	caps := corpusCaps(t)
	caps = append(caps, Hero(1))
	for _, cfg := range []Config{{FPS: 60}, {FPS: 30}, {EveryWrite: true}} {
		for _, scrollAware := range []bool{false, true} {
			name := fmt.Sprintf("%s/scroll=%v", cfg.Label(), scrollAware)
			t.Run(name, func(t *testing.T) {
				for _, c := range caps {
					t.Run(c.Name, func(t *testing.T) {
						roundTrip(t, c, cfg, scrollAware)
					})
				}
			})
		}
	}
}

func roundTrip(t *testing.T, c *Capture, cfg Config, scrollAware bool) {
	t.Helper()
	server := newReplay(c.Cols, c.Rows)
	clientEmu := newClient(c.Cols, c.Rows)
	enc := NewEncoder(c.Cols, c.Rows)
	ticks := tickTimes(cfg, c.DurationMs())

	frames := 0
	verify := func() {
		want := server.snapshot()
		got := clientEmu.snapshot()
		frames++
		if framesDiff(want, got) != "" {
			t.Fatalf("%s at %s after frame %d: %s", c.Name, cfg.Label(), frames, framesDiff(want, got))
		}
	}
	ti := 0
	for _, ch := range c.Chunks {
		if !cfg.EveryWrite {
			for ti < len(ticks) && ticks[ti] < ch.AtMs {
				clientEmu.apply(enc.Frame(server.snapshot(), scrollAware))
				verify()
				ti++
			}
		}
		server.write(ch.Data)
		if cfg.EveryWrite {
			clientEmu.apply(enc.Frame(server.snapshot(), scrollAware))
			verify()
		}
	}
	for ti < len(ticks) {
		clientEmu.apply(enc.Frame(server.snapshot(), scrollAware))
		verify()
		ti++
	}
	if frames == 0 {
		t.Fatalf("%s: no frames delivered", c.Name)
	}
	if err := verifyFinalCursor(server, clientEmu); err != nil {
		t.Fatalf("%s: %v", c.Name, err)
	}
}

// verifyFinalCursor checks the caret as well as the cells. A cursor in the
// phantom column (x == width) is the same place as the last column.
func verifyFinalCursor(server *replay, clientEmu *client) error {
	want := server.snapshot()
	got := clientEmu.snapshot()
	if want.CursorY != got.CursorY {
		return fmt.Errorf("cursor row: want %d, got %d", want.CursorY, got.CursorY)
	}
	wx := min(want.CursorX, want.W-1)
	gx := min(got.CursorX, got.W-1)
	if wx != gx {
		return fmt.Errorf("cursor column: want %d, got %d", want.CursorX, got.CursorX)
	}
	return nil
}

// framesDiff describes the first difference between two screens, or "" when
// they are identical.
func framesDiff(want, got *Frame) string {
	if want.Alt != got.Alt {
		return fmt.Sprintf("alternate screen: want %v, got %v", want.Alt, got.Alt)
	}
	for y := range want.H {
		for x := range want.W {
			wc, gc := want.At(x, y), got.At(x, y)
			if sameCell(wc, gc) {
				continue
			}
			return fmt.Sprintf("cell (%d,%d): want %q w%d %q style, got %q w%d %q style\nrow want: %q\nrow got:  %q",
				x, y, wc.Content, wc.Width, wc.Style.String(), gc.Content, gc.Width, gc.Style.String(),
				want.RowText(y), got.RowText(y))
		}
	}
	return ""
}

// TestScrollShiftRecognised requires the scroll-aware encoder to recognise a
// screen that scrolled, and to leave an unchanged screen alone.
func TestScrollShiftRecognised(t *testing.T) {
	base := NewFrame(10, 5)
	for y := range base.H {
		putRow(base, y, fmt.Sprintf("row%d", y))
	}
	scrolled := NewFrame(10, 5)
	for y := range 3 {
		copyRow(scrolled, y, base, y+2)
	}
	putRow(scrolled, 3, "new-a")
	putRow(scrolled, 4, "new-b")

	if k := scrollShift(base, scrolled); k != 2 {
		t.Fatalf("scrollShift = %d, want 2", k)
	}

	enc := NewEncoder(10, 5)
	enc.Frame(base, true)
	out := string(enc.Frame(scrolled, true))
	if enc.ScrollOps != 1 || !strings.Contains(out, "\x1b[2S") {
		t.Fatalf("scroll-aware frame %q: scroll ops=%d, want one \\x1b[2S", out, enc.ScrollOps)
	}
	// The scroll operation replaces the repaint: only the two new rows follow.
	if strings.Contains(out, "row2") || strings.Contains(out, "row0") {
		t.Fatalf("scroll-aware frame %q rewrote rows that scrolled", out)
	}

	// A screen that did not move is not scrolled, and costs nothing at all.
	enc2 := NewEncoder(10, 5)
	enc2.Frame(base, true)
	unchanged := enc2.Frame(base, true)
	if len(unchanged) != 0 {
		t.Fatalf("unchanged frame cost %d bytes: %q", len(unchanged), unchanged)
	}
	if k := scrollShift(base, base); k != 0 && len(unchanged) != 0 {
		t.Fatalf("unchanged screen reported scroll %d", k)
	}
}

// TestSameCellDistinguishesCoveredFromBlank is the one comparison a
// fixed-coordinate encoder gets wrong silently: a wide cell's tail is not the
// blank that a repaint would write there.
func TestSameCellDistinguishesCoveredFromBlank(t *testing.T) {
	wide := Cell{Content: "👍", Width: 2}
	tail := Cell{Width: covered}
	blank := blankCell()
	if sameCell(tail, blank) {
		t.Fatal("a wide cell's tail compared equal to a blank")
	}
	f := NewFrame(4, 1)
	f.Cells[0] = wide
	f.Cells[1] = tail
	if f.At(1, 0).Width != covered {
		t.Fatal("tail not marked covered")
	}
}

// TestWideGlyphRoundTrip requires a wide grapheme to survive the frame stream,
// tail and all.
func TestWideGlyphRoundTrip(t *testing.T) {
	for _, s := range []string{"👍🏽 wide", "family <👨‍👩‍👧‍👦>", "flag <🇷🇺>", "heart <❤️>"} {
		server := newReplay(20, 2)
		c := newClient(20, 2)
		enc := NewEncoder(20, 2)
		server.write([]byte(s))
		c.apply(enc.Frame(server.snapshot(), false))
		c.apply(enc.Frame(server.snapshot(), true))
		if d := framesDiff(server.snapshot(), c.snapshot()); d != "" {
			t.Fatalf("%q: %s", s, d)
		}
	}
}

// TestAlternateScreenRoundTrip requires both screens to survive a program that
// leaves and returns.
func TestAlternateScreenRoundTrip(t *testing.T) {
	server := newReplay(20, 3)
	c := newClient(20, 3)
	enc := NewEncoder(20, 3)

	step := func(s string) {
		server.write([]byte(s))
		c.apply(enc.Frame(server.snapshot(), true))
		if d := framesDiff(server.snapshot(), c.snapshot()); d != "" {
			t.Fatalf("%q: %s", s, d)
		}
	}
	step("main text")
	step("\x1b[?1049h")
	step("\x1b[2;3Halt")
	step("\x1b[?1049l")
	step("more main")
}

func putRow(f *Frame, y int, s string) {
	for x := range f.W {
		f.Cells[y*f.W+x] = blankCell()
	}
	for i, r := range s {
		if i >= f.W {
			break
		}
		f.Cells[y*f.W+i] = Cell{Content: string(r), Width: 1}
	}
}

func copyRow(dst *Frame, dy int, src *Frame, sy int) {
	copy(dst.Cells[dy*dst.W:(dy+1)*dst.W], src.Cells[sy*src.W:(sy+1)*src.W])
}
