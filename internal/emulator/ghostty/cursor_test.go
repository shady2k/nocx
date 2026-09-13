package ghostty

// The cursor read (nocx-ygxjv.3, the port's Cursor method).
//
// These drive the PORT, like every other test in this package: what is asserted
// is what a consumer of emulator.Terminal can see, never the adapter's own
// fields.
//
// WHY THE POSITION IS WORTH A TEST AT ALL. A caret is the one part of a frame
// that has no cell: a grid can be reconstructed from the cells a program drew,
// and where the program will draw NEXT cannot. The tests below pin the two
// facts a renderer needs and the two mistakes that produce a plausible wrong
// answer — an off-by-one origin (CUP is 1-based and this port is not), and a
// caret painted where a program had hidden it (DECTCEM).

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// cursorTerm is this file's own terminal, built the same way the rest of the
// package builds one: through the port, at a size large enough that nothing
// below wraps.
func cursorTerm(t *testing.T, cols, rows int) emulator.Terminal {
	t.Helper()
	term, err := New(emulator.Geometry{Cols: cols, Rows: rows})
	if err != nil {
		t.Fatalf("new terminal %dx%d: %v", cols, rows, err)
	}
	t.Cleanup(term.Close)
	return term
}

func cursorWrite(t *testing.T, term emulator.Terminal, s string) {
	t.Helper()
	if _, err := term.Ingest([]byte(s)); err != nil {
		t.Fatalf("ingest %q: %v", s, err)
	}
}

func cursorOf(t *testing.T, term emulator.Terminal) emulator.Cursor {
	t.Helper()
	c, err := term.Cursor()
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	return c
}

// TestCursorFollowsTheProgramsOwnMoves is the ordinary case, and it is written
// with positions nothing else in the terminal occupies: CUP is 1-BASED
// (`\x1b[3;7H` is the third row, seventh column) while this port is not, so a
// port that passed the program's numbers through would report (7, 3) here and
// be wrong by one on both axes at once — the shape of a defect that looks right
// on the home position.
func TestCursorFollowsTheProgramsOwnMoves(t *testing.T) {
	term := cursorTerm(t, 20, 6)

	if got := cursorOf(t, term); got.X != 0 || got.Y != 0 {
		t.Errorf("a fresh terminal's cursor is (%d,%d), want (0,0): the home position is the port's own origin", got.X, got.Y)
	}

	cursorWrite(t, term, "\x1b[3;7H")
	if got := cursorOf(t, term); got.X != 6 || got.Y != 2 {
		t.Errorf("after CUP 3;7 the cursor is (%d,%d), want (6,2): the program's row and column are 1-based and this port is not", got.X, got.Y)
	}

	// Printing advances the column and leaves the row, which is the half a
	// saved-and-restored position would still get right by accident.
	cursorWrite(t, term, "AB")
	if got := cursorOf(t, term); got.X != 8 || got.Y != 2 {
		t.Errorf("after printing two cells at column 6 the cursor is (%d,%d), want (8,2)", got.X, got.Y)
	}

	cursorWrite(t, term, "\x1b[1;1H")
	if got := cursorOf(t, term); got.X != 0 || got.Y != 0 {
		t.Errorf("after CUP 1;1 the cursor is (%d,%d), want (0,0)", got.X, got.Y)
	}
}

// TestCursorVisibilityFollowsTheModeTheProgramSet is the other half of the
// read, and the half a position cannot carry. A program that hides the cursor
// is composing a frame and does not want a caret drawn into it; a port that
// reported only coordinates would leave a renderer painting one.
func TestCursorVisibilityFollowsTheModeTheProgramSet(t *testing.T) {
	term := cursorTerm(t, 20, 6)

	if got := cursorOf(t, term); !got.Visible {
		t.Error("a fresh terminal's cursor is hidden: a program that said nothing about DECTCEM has a visible caret")
	}

	cursorWrite(t, term, "\x1b[?25l")
	if got := cursorOf(t, term); got.Visible {
		t.Error("after the program hid the cursor (DECTCEM off) the port still reports it visible")
	}

	cursorWrite(t, term, "\x1b[?25h")
	if got := cursorOf(t, term); !got.Visible {
		t.Error("after the program showed the cursor again (DECTCEM on) the port reports it hidden")
	}
}

// TestCursorReadsTheBufferTheScreenNames is why Cursor is a read of its own and
// not a field of Row: the two buffers have two carets, and a caller that asked
// which screen is active must get THAT screen's caret. The position on the
// alternate screen is chosen far from the primary one's, so an implementation
// that read the wrong buffer could not answer it by luck.
func TestCursorReadsTheBufferTheScreenNames(t *testing.T) {
	term := cursorTerm(t, 20, 6)

	cursorWrite(t, term, "\x1b[2;3H")
	if got := cursorOf(t, term); got.X != 2 || got.Y != 1 {
		t.Fatalf("primary cursor is (%d,%d), want (2,1)", got.X, got.Y)
	}

	cursorWrite(t, term, "\x1b[?1049h\x1b[4;9H")
	if screen, err := term.Screen(); err != nil || screen != emulator.ScreenAlternate {
		t.Fatalf("screen after 1049h = %v (%v), want the alternate one", screen, err)
	}
	if got := cursorOf(t, term); got.X != 8 || got.Y != 3 {
		t.Errorf("alternate cursor is (%d,%d), want (8,3): the caret follows the screen Screen names, not the one the program left", got.X, got.Y)
	}

	cursorWrite(t, term, "\x1b[?1049l")
	if screen, err := term.Screen(); err != nil || screen != emulator.ScreenPrimary {
		t.Fatalf("screen after 1049l = %v (%v), want the primary one", screen, err)
	}
	if got := cursorOf(t, term); got.X != 2 || got.Y != 1 {
		t.Errorf("back on the primary screen the cursor is (%d,%d), want (2,1): 1049 restores the position it saved", got.X, got.Y)
	}
}

// TestCursorIsRefusedAfterClose is the pair AGENTS.md's third rule asks for:
// every "returns an error when" beside a case where the same call succeeds —
// which the four tests above are. The refusal is asserted by SENTINEL, not by
// "an error came back": a port that answered ErrOutOfRange here would be
// describing a live terminal with a caret outside it.
func TestCursorIsRefusedAfterClose(t *testing.T) {
	term := cursorTerm(t, 20, 6)
	cursorWrite(t, term, "\x1b[3;3H")
	term.Close()

	got, err := term.Cursor()
	if err == nil {
		t.Fatalf("a closed terminal answered a cursor read with (%d,%d)", got.X, got.Y)
	}
	if !strings.Contains(err.Error(), emulator.ErrClosed.Error()) {
		t.Errorf("cursor after Close = %v, want it to name %v", err, emulator.ErrClosed)
	}
	if got.X != 0 || got.Y != 0 || got.Visible {
		t.Errorf("a refused read answered with %+v, want the zero cursor: a value beside an error is one a caller could act on", got)
	}
}
