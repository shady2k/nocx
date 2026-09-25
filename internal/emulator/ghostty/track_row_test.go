package ghostty

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// TrackRow is the row-identity primitive nocx-2v80t.3.10 went looking for:
// libghostty-vt's tracked grid references (include/ghostty/vt/grid_ref_tracked.h,
// ghostty_terminal_grid_ref_track), which the library documents as following
// their cell "across normal screen operations... scrolling, scrollback
// pruning, resize/reflow" and losing their value only when "the underlying
// grid is reset, pruned, or otherwise discarded in a way that cannot be
// mapped to a meaningful new cell" (grid_ref.h). These tests pin down what
// this port's TrackRow/RowTrack actually deliver of that, against the real
// library, rather than trusting the header's prose.

// TestTrackRowSurvivesAReflow is the property sessionruntime's window needs
// paired with its own geometry test (departed_window_geometry_test.go): a
// row's identity must not depend on the width or height the screen held when
// it was pinned, because a resize re-lays every row out. Verified at the
// e2e's own 80->148 shape.
func TestTrackRowSurvivesAReflow(t *testing.T) {
	term := departedTerm(t, 80, 24)
	if _, err := term.Ingest([]byte("hello-row\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	track, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	defer track.Release()
	if !track.Alive() {
		t.Fatal("a freshly tracked row must be alive")
	}

	for _, g := range []emulator.Geometry{
		{Cols: 40, Rows: 24, CellWidthPx: 10, CellHeightPx: 20},
		{Cols: 148, Rows: 33, CellWidthPx: 10, CellHeightPx: 20},
		{Cols: 80, Rows: 24, CellWidthPx: 10, CellHeightPx: 20},
	} {
		if _, err := term.Resize(g); err != nil {
			t.Fatalf("resize to %dx%d: %v", g.Cols, g.Rows, err)
		}
		if !track.Alive() {
			t.Fatalf("the row did not survive a reflow to %dx%d", g.Cols, g.Rows)
		}
	}
}

// TestTrackRowDiesOnAFullReset is the destroy half: a full terminal reset
// (RIS, ESC c) discards the page list the row lived in, and the library's own
// contract says a tracked reference then reports no value, never again. This
// is the case [emulator.RowTrack.Alive] exists to catch — a row nocx-2v80t.3.10
// showed being matched against by stale TEXT after it had, in the port's own
// words, ceased rather than left (emulator.Terminal.DepartedRows's doc).
func TestTrackRowDiesOnAFullReset(t *testing.T) {
	term := departedTerm(t, 80, 24)
	if _, err := term.Ingest([]byte("hello-row\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	track, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	defer track.Release()

	if _, err := term.Ingest([]byte("\x1bc")); err != nil { // RIS
		t.Fatalf("reset: %v", err)
	}
	if track.Alive() {
		t.Fatal("a row must not survive a full reset")
	}
}

// TestTrackRowStaysAliveThroughAPlainErase documents a boundary this port's
// identity does NOT cover, so a reader does not assume it does: an ordinary
// erase-in-display (CSI 2 J) plus a cursor home — the sequence an interactive
// `clear` ordinarily sends, terminfo's "clear" capability being ESC[H ESC[2J
// for the terminal types nocx runs under — blanks the row's CONTENT in place
// without discarding the page slot the row lives in, so the tracked reference
// reports it alive throughout. Content is what changed, and identity alone
// cannot see that; sessionruntime's reconcilePendingScreenLocked
// (observation.go, nocx-2v80t.3.10) is what closes that gap, by re-reading
// the row's content through the handle (RowTrack.Row, nocx-2v80t.3.13 —
// TestTrackRowReadsAPlainEraseAsBlank).
func TestTrackRowStaysAliveThroughAPlainErase(t *testing.T) {
	term := departedTerm(t, 80, 24)
	if _, err := term.Ingest([]byte("hello-row\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	track, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	defer track.Release()

	if _, err := term.Ingest([]byte("\x1b[2J\x1b[H")); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if !track.Alive() {
		t.Fatal("an ordinary erase must not itself invalidate the row's identity (see the test's own doc)")
	}
}

// TestTrackRowRefusesOutOfRange mirrors Row's own bound: a position outside
// the active area names nothing to track.
func TestTrackRowRefusesOutOfRange(t *testing.T) {
	term := departedTerm(t, 80, 24)
	if _, err := term.TrackRow(24); !errors.Is(err, emulator.ErrOutOfRange) {
		t.Fatalf("TrackRow(24) on a 24-row screen = %v, want %v", err, emulator.ErrOutOfRange)
	}
	if _, err := term.TrackRow(-1); !errors.Is(err, emulator.ErrOutOfRange) {
		t.Fatalf("TrackRow(-1) = %v, want %v", err, emulator.ErrOutOfRange)
	}
}

// TestTrackRowAfterCloseReportsNoValueAndNeverDoubleFrees is release()'s own
// sweep (terminal.go): every tracked reference a caller left outstanding is
// freed when the terminal closes, and a handle a caller still holds afterward
// must answer "no value" rather than reach into freed memory, whether the
// caller asks before or after releasing it itself.
func TestTrackRowAfterCloseReportsNoValueAndNeverDoubleFrees(t *testing.T) {
	term, err := New(emulator.Geometry{Cols: 80, Rows: 24, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("new terminal: %v", err)
	}
	track, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}

	term.Close()

	if track.Alive() {
		t.Fatal("a row on a closed terminal must not report alive")
	}
	track.Release() // must be a safe no-op, not a double free of what Close swept
	track.Release() // idempotent on its own account too
}

// TestTrackRowReleaseIsIdempotent covers the ordinary lifetime: releasing a
// still-open terminal's row twice must not double free the library's handle.
func TestTrackRowReleaseIsIdempotent(t *testing.T) {
	term := departedTerm(t, 80, 24)
	track, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	track.Release()
	track.Release()
	if track.Alive() {
		t.Fatal("a released row must not report alive")
	}
}

// TestTrackRowOnAClosedTerminalIsRefused mirrors Row's own ErrClosed.
func TestTrackRowOnAClosedTerminalIsRefused(t *testing.T) {
	term, err := New(emulator.Geometry{Cols: 80, Rows: 24, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("new terminal: %v", err)
	}
	term.Close()
	if _, err := term.TrackRow(0); !errors.Is(err, emulator.ErrClosed) {
		t.Fatalf("TrackRow on a closed terminal = %v, want %v", err, emulator.ErrClosed)
	}
}

// trackedText reads a tracked row's visible text, trailing blanks dropped.
func trackedText(t *testing.T, track emulator.RowTrack) (string, emulator.Row) {
	t.Helper()
	row, err := track.Row()
	if err != nil {
		t.Fatalf("read the tracked row: %v", err)
	}
	var sb strings.Builder
	for _, c := range row.Cells {
		sb.WriteString(c.Grapheme)
	}
	return strings.TrimRight(sb.String(), " "), row
}

// TestTrackRowReadsItsRowWhereverAReflowCarriedIt is the content half of the
// identity (nocx-2v80t.3.13): a read through the handle reaches the same
// row after a resize re-laid the screen out and after scrolling carried it
// into the history, and reads what that row carries.
func TestTrackRowReadsItsRowWhereverAReflowCarriedIt(t *testing.T) {
	term := departedTerm(t, 80, 24)
	if _, err := term.Ingest([]byte("first\r\nhello-row\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	track, err := term.TrackRow(1)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	defer track.Release()
	if got, _ := trackedText(t, track); got != "hello-row" {
		t.Fatalf("the freshly tracked row reads %q, want %q", got, "hello-row")
	}
	for _, g := range []emulator.Geometry{
		{Cols: 148, Rows: 33, CellWidthPx: 10, CellHeightPx: 20},
		{Cols: 40, Rows: 10, CellWidthPx: 10, CellHeightPx: 20},
		{Cols: 80, Rows: 24, CellWidthPx: 10, CellHeightPx: 20},
	} {
		if _, err := term.Resize(g); err != nil {
			t.Fatalf("resize to %dx%d: %v", g.Cols, g.Rows, err)
		}
		if got, _ := trackedText(t, track); got != "hello-row" {
			t.Fatalf("after a reflow to %dx%d the tracked row reads %q, want %q", g.Cols, g.Rows, got, "hello-row")
		}
	}
	// Scroll it off the top: it is in the history now, and still the row.
	var sb strings.Builder
	for range 40 {
		sb.WriteString("filler\r\n")
	}
	if _, err := term.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if got, _ := trackedText(t, track); got != "hello-row" {
		t.Fatalf("scrolled into the history the tracked row reads %q, want %q", got, "hello-row")
	}
}

// TestTrackRowReadsAPlainEraseAsBlank pairs TestTrackRowStaysAliveThroughAPlainErase:
// the handle stays alive, and its read is what shows the row was rewritten —
// before a reflow and after one.
func TestTrackRowReadsAPlainEraseAsBlank(t *testing.T) {
	term := departedTerm(t, 80, 24)
	if _, err := term.Ingest([]byte("hello-row\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	track, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	defer track.Release()
	if _, err := term.Ingest([]byte("\x1b[H\x1b[2J")); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if got, _ := trackedText(t, track); got != "" {
		t.Fatalf("an erased row reads %q through its handle, want it blank", got)
	}
	if _, err := term.Resize(emulator.Geometry{Cols: 148, Rows: 33, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if got, _ := trackedText(t, track); got != "" {
		t.Fatalf("an erased row reads %q through its handle after a reflow, want it blank", got)
	}
}

// TestTrackRowReadRefusesWhatItCannotName: a discarded row, a released
// handle, the alternate screen and a closed terminal each answer an error,
// never a row read from somewhere else.
func TestTrackRowReadRefusesWhatItCannotName(t *testing.T) {
	term := departedTerm(t, 80, 24)
	if _, err := term.Ingest([]byte("hello-row\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	released, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	released.Release()
	if _, err = released.Row(); !errors.Is(err, emulator.ErrOutOfRange) {
		t.Fatalf("a released handle's read = %v, want %v", err, emulator.ErrOutOfRange)
	}

	alt, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	defer alt.Release()
	if _, err = term.Ingest([]byte("\x1b[?1049h")); err != nil {
		t.Fatalf("enter the alternate screen: %v", err)
	}
	if _, err = alt.Row(); !errors.Is(err, emulator.ErrUnsupported) {
		t.Fatalf("a read while the alternate screen is active = %v, want %v", err, emulator.ErrUnsupported)
	}
	if _, err = term.Ingest([]byte("\x1b[?1049l")); err != nil {
		t.Fatalf("leave the alternate screen: %v", err)
	}
	if got, _ := trackedText(t, alt); got != "hello-row" {
		t.Fatalf("back on the primary screen the tracked row reads %q, want %q", got, "hello-row")
	}

	if _, err = term.Ingest([]byte("\x1bc")); err != nil { // RIS
		t.Fatalf("reset: %v", err)
	}
	if _, err = alt.Row(); !errors.Is(err, emulator.ErrOutOfRange) {
		t.Fatalf("a discarded row's read = %v, want %v", err, emulator.ErrOutOfRange)
	}

	closing, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	term.Close()
	if _, err = closing.Row(); !errors.Is(err, emulator.ErrClosed) {
		t.Fatalf("a read after close = %v, want %v", err, emulator.ErrClosed)
	}
	closing.Release()
}

// TestTrackRowReadAcrossARewrap measures what a reflow does to a tracked
// row's read when it re-cuts a soft-wrapped line: narrowing, the handle
// (column 0 of the row) reads the FIRST piece, flagged as wrapped; widening,
// a row that was a continuation is merged into the line's one row, and the
// read is that whole row. sessionruntime's reconciliation accepts exactly
// those shapes as unwritten (stillReadsAsCaptured).
func TestTrackRowReadAcrossARewrap(t *testing.T) {
	t.Run("widening merges a continuation into its line", func(t *testing.T) {
		term := departedTerm(t, 80, 24)
		line := strings.Repeat("0123456789", 10) // a hundred characters
		if _, err := term.Ingest([]byte(line + "\r\n")); err != nil {
			t.Fatalf("ingest: %v", err)
		}
		track, err := term.TrackRow(1)
		if err != nil {
			t.Fatalf("TrackRow: %v", err)
		}
		defer track.Release()
		if got, row := trackedText(t, track); got != line[80:] || !row.Continuation {
			t.Fatalf("the continuation reads %q (continuation=%v), want %q", got, row.Continuation, line[80:])
		}
		if _, err := term.Resize(emulator.Geometry{Cols: 148, Rows: 24, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
			t.Fatalf("resize: %v", err)
		}
		if got, _ := trackedText(t, track); got != line {
			t.Fatalf("after widening the tracked continuation reads %q, want the whole line %q", got, line)
		}
	})

	term := departedTerm(t, 80, 24)
	line := strings.Repeat("abcdefghij", 6) // sixty characters
	if _, err := term.Ingest([]byte(line + "\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	track, err := term.TrackRow(0)
	if err != nil {
		t.Fatalf("TrackRow: %v", err)
	}
	defer track.Release()
	if _, err := term.Resize(emulator.Geometry{Cols: 40, Rows: 24, CellWidthPx: 10, CellHeightPx: 20}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	got, row := trackedText(t, track)
	if got != line[:40] || !row.Wrap {
		t.Fatalf("after narrowing the tracked row reads %q (wrap=%v), want %q wrapped", got, row.Wrap, line[:40])
	}
}
