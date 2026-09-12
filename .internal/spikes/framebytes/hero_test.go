package framebytes

import (
	"bytes"
	"testing"
)

// TestHeroCaseIsTheCaseTheArgumentNames requires the generated hero to be what
// §7 describes: a 100×40 screen fed 600 distinct full-width lines a second.
func TestHeroCaseIsTheCaseTheArgumentNames(t *testing.T) {
	h := Hero(1)
	if len(h.Chunks) != HeroLinesPerSecond {
		t.Fatalf("chunks = %d, want %d", len(h.Chunks), HeroLinesPerSecond)
	}
	if h.RawBytes() != HeroLinesPerSecond*(HeroCols+2) {
		t.Fatalf("raw = %d, want %d", h.RawBytes(), HeroLinesPerSecond*(HeroCols+2))
	}
	if h.Cols != 100 || h.Rows != 40 {
		t.Fatalf("geometry = %d×%d, want 100×40", h.Cols, h.Rows)
	}
	if h.DurationMs() != 998 {
		t.Fatalf("duration = %d ms, want 998", h.DurationMs())
	}
	for i := 1; i < len(h.Chunks); i++ {
		if bytes.Equal(h.Chunks[i].Data, h.Chunks[i-1].Data) {
			t.Fatalf("line %d repeats line %d", i, i-1)
		}
	}

	// Every row of the final screen but the cursor's is full width, which is
	// what makes the positional encoder rewrite all four thousand cells. The
	// bottom row is blank because the producer's last newline scrolled the
	// screen and left the cursor on the row below the last line.
	server := newReplay(h.Cols, h.Rows)
	for _, ch := range h.Chunks {
		server.write(ch.Data)
	}
	f := server.snapshot()
	blank := 0
	for y := range f.H {
		got := len([]rune(f.RowText(y)))
		if got == 0 {
			blank++
			continue
		}
		if got != HeroCols {
			t.Fatalf("row %d holds %d columns of text, want %d: %q", y, got, HeroCols, f.RowText(y))
		}
	}
	if blank != 1 {
		t.Fatalf("%d blank rows, want exactly the cursor row", blank)
	}
	// The same generation twice is the same bytes.
	if Hero(1).Digest() != h.Digest() {
		t.Fatal("generation is not deterministic")
	}
}

// TestScrollAwareBeatsPositionalOnTheHeroCase is the measurement's central
// claim, checked as a property rather than read off a report.
func TestScrollAwareBeatsPositionalOnTheHeroCase(t *testing.T) {
	h := Hero(2)
	m := Measure(h, Config{FPS: 60})
	if m.ScrollOps == 0 {
		t.Fatal("the scroll-aware encoder emitted no scroll operation on a scrolling screen")
	}
	if m.ScrollAware*2 >= m.Positional {
		t.Fatalf("scroll-aware %d bytes vs positional %d: expected it to be less than half",
			m.ScrollAware, m.Positional)
	}
	if m.Omitted != 0 {
		t.Fatalf("omitted = %d bytes at 60 fps on a pure scroller, want 0", m.Omitted)
	}
}
