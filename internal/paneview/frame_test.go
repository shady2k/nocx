package paneview

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// The frame is the reading every consumer downstream acts on, so these tests
// drive the REAL emulator through the port — the same one the session runtime
// directs — rather than a hand-built Frame. A mapping tested against a fixture
// it also produced would agree with itself.

func frameOf(t *testing.T, cols, rows int, program string) Frame {
	t.Helper()
	f, err := replayNow(cols, rows, program, 1)
	if err != nil {
		t.Fatalf("replay %q: %v", program, err)
	}
	if len(f) != 1 {
		t.Fatalf("replay answered %d frames, want 1", len(f))
	}
	return f[0]
}

func replayNow(cols, rows int, program string, marks ...int) ([]Frame, error) {
	return Replay(ghostty.New, emulator.Geometry{Cols: cols, Rows: rows},
		[][]byte{[]byte(program)}, marks)
}

// TestAFrameCarriesWhatTheProgramDrew is the ordinary case: cells where the
// program put them, and the caret where it left it.
func TestAFrameCarriesWhatTheProgramDrew(t *testing.T) {
	f := frameOf(t, 20, 5, "\x1b[2;3HHI")

	if f.Cols != 20 || f.Rows != 5 {
		t.Fatalf("frame is %dx%d, want 20x5", f.Cols, f.Rows)
	}
	if got := f.Text(1); !strings.HasPrefix(got, "  HI") {
		t.Errorf("row 1 reads %q, want the two cells at column 3", got)
	}
	if f.CursorX != 4 || f.CursorY != 1 {
		t.Errorf("cursor is (%d,%d), want (4,1): the port's row and column are zero-indexed", f.CursorX, f.CursorY)
	}
	if !f.CursorVisible {
		t.Error("the caret is reported hidden: a program that said nothing about DECTCEM shows one")
	}
	if f.AltScreen {
		t.Error("a shell's own screen is reported as the alternate one")
	}
	if f.Completeness != sessionruntime.CompletenessComplete {
		t.Errorf("a replay answered completeness %v, want Complete: every byte it was asked to show reached it", f.Completeness)
	}
	if f.Revision != 0 {
		t.Errorf("a replay answered revision %d, want 0: a revision is a live runtime's, and a capture has none", f.Revision)
	}
}

// TestAWideClusterOccupiesTwoColumns is why Cell carries a width at all: a
// consumer that wants a POSITION reads the column index, and collapsing the
// continuation cell into the cluster's own cell would move every column after
// it. The text reading is asserted in the same breath, because a mapping that
// got one right and the other wrong is the failure this pins.
func TestAWideClusterOccupiesTwoColumns(t *testing.T) {
	f := frameOf(t, 10, 2, "\x1b[1;1Hこん")

	if got := f.Lines[0][0]; got.Text != "こ" || got.Width != 2 {
		t.Errorf("cell 0 = %+v, want the wide cluster at width 2", got)
	}
	if got := f.Lines[0][1]; got.Width != 0 {
		t.Errorf("cell 1 = %+v, want the continuation at width 0", got)
	}
	if got := f.Lines[0][2]; got.Text != "ん" || got.Width != 2 {
		t.Errorf("cell 2 = %+v, want the second cluster at width 2 — one column per cluster shifts everything after it", got)
	}
	// Text() renders the whole row, trailing blank cells included, so the
	// assertion is about what the clusters contributed: the two of them, once
	// each, with nothing between them.
	if got := f.Text(0); !strings.HasPrefix(got, "こん") {
		t.Errorf("row text starts %q, want %q: the continuation contributes no column of its own", got, "こん")
	}
	if got := f.Text(0); strings.HasPrefix(got, "こ ん") {
		t.Errorf("row text is %q: a space was inserted where the continuation cell is", got)
	}
}

// TestReplayAnswersTheSameScreenWhereverTheStreamIsSplit is the property the
// replay exists for: a capture's chunks are an artifact of how the reader
// happened to read, and the screens it produces must not depend on them.
func TestReplayAnswersTheSameScreenWhereverTheStreamIsSplit(t *testing.T) {
	whole := "\x1b[2J\x1b[1;1Halpha\x1b[3;4Hbeta\x1b[5;1Hこんにちは"

	oneShot, err := replayNow(24, 8, whole, 1)
	if err != nil {
		t.Fatalf("whole: %v", err)
	}
	// Split mid-escape, mid-cluster and between rows: the three boundaries a
	// chunked reader actually produces.
	parts := [][]byte{
		[]byte("\x1b[2J\x1b[1;1Hal"),
		[]byte("pha\x1b[3;4Hbe"),
		[]byte("ta\x1b[5;1Hこ"),
		[]byte("んにちは"),
	}
	split, err := Replay(ghostty.New, emulator.Geometry{Cols: 24, Rows: 8}, parts, []int{4})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if diff := frameDiff(whole, oneShot[0], split[0]); diff != "" {
		t.Fatalf("the split stream answered a different screen: %s", diff)
	}
}

// TestReplayAnswersTheScreenAtEachMarkAndNotOnlyTheLast: a capture is replayed
// once and read at every mark, and the frames must be the screens those marks
// describe rather than the final one repeated.
func TestReplayAnswersTheScreenAtEachMarkAndNotOnlyTheLast(t *testing.T) {
	chunks := [][]byte{
		[]byte("\x1b[2J\x1b[1;1Hfirst"),
		[]byte("\x1b[3;1Hsecond"),
		[]byte("\x1b[5;1Hthird"),
	}
	frames, err := Replay(ghostty.New, emulator.Geometry{Cols: 20, Rows: 6}, chunks, []int{1, 2, 3})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("replay answered %d frames, want one per mark", len(frames))
	}
	if got := frames[0].Text(0); !strings.HasPrefix(got, "first") {
		t.Errorf("the first mark reads %q, want the first chunk's screen", got)
	}
	if got := frames[1].Text(2); !strings.HasPrefix(got, "second") {
		t.Errorf("the second mark reads row 3 as %q, want the second chunk's screen", got)
	}
	if got := frames[2].Text(4); !strings.HasPrefix(got, "third") {
		t.Errorf("the third mark reads row 5 as %q, want the third chunk's screen", got)
	}
	// The earlier rows are still there at the last mark: the emulator is fed
	// forward, never restarted per mark.
	if got := frames[2].Text(0); !strings.HasPrefix(got, "first") {
		t.Errorf("at the last mark row 1 reads %q, want what the first chunk drew", got)
	}
}

// TestReplayReadsTheAlternateScreenTheStreamEndedIn: a full-screen program's
// last screen is the one a rule must classify, and the flag that says so is
// part of the frame.
func TestReplayReadsTheAlternateScreenTheStreamEndedIn(t *testing.T) {
	f := frameOf(t, 20, 4, "\x1b[1;1Hshell prompt\x1b[?1049h\x1b[2J\x1b[1;1Hfull-screen")
	if !f.AltScreen {
		t.Error("the frame does not report the alternate screen the program is on")
	}
	if got := f.Text(0); !strings.HasPrefix(got, "full-screen") {
		t.Errorf("row 1 reads %q, want the program's own screen", got)
	}
}

// The refusals, each beside the case above where the same call succeeds.
func TestReplayRefusesMarksItCannotHonour(t *testing.T) {
	chunks := [][]byte{[]byte("a"), []byte("b")}
	geom := emulator.Geometry{Cols: 8, Rows: 2}

	if _, err := Replay(ghostty.New, geom, chunks, []int{2, 1}); err == nil {
		t.Error("a mark that goes backwards was accepted: a capture replays forward only")
	}
	if _, err := Replay(ghostty.New, geom, chunks, []int{3}); err == nil {
		t.Error("a mark past the end of the stream was accepted")
	}
	if _, err := Replay(ghostty.New, emulator.Geometry{}, chunks, []int{1}); err == nil {
		t.Error("a replay at a geometry no terminal can have was accepted")
	}
	if _, err := Replay(nil, geom, chunks, []int{1}); err == nil {
		t.Error("a replay with no screen factory was accepted")
	}
}

func TestFromRefusesATerminalThatIsClosed(t *testing.T) {
	term, err := ghostty.New(emulator.Geometry{Cols: 8, Rows: 2})
	if err != nil {
		t.Fatalf("new terminal: %v", err)
	}
	if _, err := From(term); err != nil {
		t.Fatalf("from a live terminal: %v", err)
	}
	term.Close()
	if _, err := From(term); !errors.Is(err, emulator.ErrClosed) {
		t.Errorf("from a closed terminal = %v, want ErrClosed", err)
	}
}

// frameDiff renders the difference between two frames, or "" when they agree.
func frameDiff(name string, want, got Frame) string {
	if want.Cols != got.Cols || want.Rows != got.Rows {
		return name + ": geometry differs"
	}
	if want.CursorX != got.CursorX || want.CursorY != got.CursorY ||
		want.CursorVisible != got.CursorVisible || want.AltScreen != got.AltScreen {
		return name + ": caret or screen differs"
	}
	for y := range want.Rows {
		w, g := want.Text(y), got.Text(y)
		if w != g {
			return name + ": row differs: " + w + " vs " + g
		}
		for x := range want.Cols {
			if want.Lines[y][x].Width != got.Lines[y][x].Width {
				return name + ": cell width differs"
			}
		}
	}
	return ""
}
