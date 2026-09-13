// Package paneview is what a pane's screen IS, read from the runtime that owns
// it — never from a second emulator beside it.
//
// # Where this package sits, and why it is not a grid
//
// ADR-0066 moved the one emulator into the session runtime beside the PTY: for
// a remote session, beside the REMOTE pty, with SSH as the carrier. The
// coordinator therefore has no terminal state of its own to keep, and no grid
// to feed: what it can do is READ a frame out of a runtime that already exists.
// That is the whole of this package — a projection ([Frame]), the one mapping
// from an [emulator.Terminal] to it ([From]), the seam a read comes through
// ([Source]) and the bookkeeping for which panes nocx is watching ([Store]).
//
// It holds no emulator, and that is the point rather than an omission. The
// package it replaces (internal/panegrid) created one per enrolled pane and
// fed it from the byte stream the coordinator received, which meant a screen
// built from whatever survived the helper's bounded window. Here the screen
// comes from the runtime that saw byte zero, and a frame read is a question
// asked of the process that owns the terminal.
//
// # The two powers are unchanged
//
// The AD-6 amendment permits a pane's screen to decide exactly two things —
// whether nocx may write into this pane, and what its activity indicator shows
// — and its 2026-09-12 amendment (ADR-0066) keeps that list verbatim while
// superseding where the parser runs. Nothing here decides a wave state, a
// lifecycle attempt, an execution attempt or a network destination, and
// boundary_test.go is what keeps it that way: the package may not import
// lifecycle, session, content or notify, so a third power cannot be built here
// by accident.
//
// # The interval, and what opens it
//
// A frame is readable for a pane nocx is WATCHING, and [Store] owns that set.
// Watching is an act (an explicit enrolment naming a pane), never an inference,
// and it is bounded. [Store.Frame] refuses a pane that is not watched, so the
// interval has one owner rather than a rule every caller remembers.
package paneview

import (
	"fmt"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// Cell is one column of a Frame.
//
// Width is the load-bearing field and the reason this type is not just a
// string. A double-width character occupies two columns: the first carries the
// grapheme with Width 2, the second is a continuation with Width 0. A consumer
// that wants text skips Width 0 cells; a consumer that wants a POSITION — which
// is what a chrome anchor is — reads the column index directly. Collapsing the
// two loses the second reading, which is the one both permitted powers need.
type Cell struct {
	Text  string
	Width int
}

// Frame is everything a caller may learn about a pane's screen: what is on it,
// where the caret is, and — for a live session — which revision it was read at
// and whether the runtime can vouch for it.
//
// There is deliberately nothing else here. No classification, no verdict, no
// status: a verdict computed in this package would be a third power the
// amendment does not grant.
type Frame struct {
	Cols, Rows int
	CursorX    int
	CursorY    int
	// CursorVisible is DECTCEM: a program composing a frame hides the caret,
	// and a renderer that painted one there would show the person something
	// the program withdrew.
	CursorVisible bool
	// AltScreen reports whether the pane is in the alternate screen. It is an
	// observation like any other and decides nothing on its own; ADR-0024
	// decision 1 forbids it to open or complete an execution attempt, and
	// nothing here could.
	AltScreen bool
	// Lines is Rows long; each is Cols long.
	Lines [][]Cell

	// Revision is the runtime revision the frame was read at — the number an
	// attach relates its cards and its live frame to (design §5), and the
	// reason a snapshot and the changes after it compose at all.
	//
	// Nothing in this package reads it, and it is here rather than added later
	// because the wire shape it crosses on is frozen when the helper ABI ships:
	// a field added afterwards is a break, not an extension
	// (contracts/helper/README.md).
	Revision sessionruntime.Revision
	// Completeness is what the runtime can honestly claim about the stream it
	// holds. It is part of a FRAME rather than a second call because a frame
	// whose provenance is unknown is not a frame a write may be decided on:
	// the write gate refuses while this is unknown (design §6.7).
	//
	// A replay leaves it [sessionruntime.CompletenessComplete]: the bytes it
	// was handed are the whole of what it was asked to show.
	Completeness sessionruntime.Completeness
}

// Text renders one row as a string, skipping continuation cells so a
// double-width character contributes its grapheme once rather than a grapheme
// and a space. Convenience for callers that want content rather than position.
func (f Frame) Text(row int) string {
	if row < 0 || row >= len(f.Lines) {
		return ""
	}
	out := make([]rune, 0, f.Cols)
	for _, c := range f.Lines[row] {
		if c.Width == 0 {
			continue
		}
		if c.Text == "" {
			out = append(out, ' ')
			continue
		}
		out = append(out, []rune(c.Text)...)
	}
	return string(out)
}

// ScreenFactory builds the terminal a frame is read from. It is the same seam
// the session runtime is created over, so the emulator choice is named in one
// place rather than at every construction site (ADR-0065 chooses which
// emulator; this is the shape the choice is reached from).
type ScreenFactory func(g emulator.Geometry) (emulator.Terminal, error)

// From reads one frame out of the emulator a runtime directs.
//
// It is the ONLY mapping from the emulator's vocabulary to this one, and it is
// here rather than in each reader because two readers would agree on the day
// they were written and disagree on the day one of them learned about a width
// class the other had not (AGENTS.md, "look for the existing answer").
//
// Revision and Completeness are left at their zero values: they are facts about
// a RUNTIME, not about a screen, and the caller that holds the runtime is the
// one that can state them.
func From(term emulator.Terminal) (Frame, error) {
	geom, err := term.Geometry()
	if err != nil {
		return Frame{}, fmt.Errorf("paneview: geometry: %w", err)
	}
	screen, err := term.Screen()
	if err != nil {
		return Frame{}, fmt.Errorf("paneview: screen: %w", err)
	}
	cursor, err := term.Cursor()
	if err != nil {
		return Frame{}, fmt.Errorf("paneview: cursor: %w", err)
	}
	if !geom.Valid() {
		// A geometry no terminal can have is not a frame with zero rows: a
		// caller that painted it would show an empty screen as though the
		// program had erased it.
		return Frame{}, fmt.Errorf("paneview: terminal reports %dx%d: %w", geom.Cols, geom.Rows, emulator.ErrOutOfRange)
	}
	f := Frame{
		Cols:          geom.Cols,
		Rows:          geom.Rows,
		CursorX:       cursor.X,
		CursorY:       cursor.Y,
		CursorVisible: cursor.Visible,
		AltScreen:     screen == emulator.ScreenAlternate,
		Lines:         make([][]Cell, geom.Rows),
	}
	for y := range geom.Rows {
		row, err := term.Row(y)
		if err != nil {
			return Frame{}, fmt.Errorf("paneview: row %d: %w", y, err)
		}
		line := make([]Cell, len(row.Cells))
		for x, c := range row.Cells {
			line[x] = Cell{Text: c.Grapheme, Width: cellWidth(c.Width)}
		}
		f.Lines[y] = line
	}
	return f, nil
}

// cellWidth maps the emulator's width class onto the column count a consumer
// reads. Both spacer classes are ZERO — they carry no text and are not
// rendered, one because the cluster to its left covered it and one because a
// cluster would have needed it at the end of a soft-wrapped line — which is
// exactly the reading [Frame.Text] skips.
//
// WidthUnknown maps to zero as well and is not a claim that the cell is empty:
// it is the class a cell read never took, and nothing is rendered either way.
func cellWidth(w emulator.Width) int {
	switch w {
	case emulator.WidthNarrow:
		return 1
	case emulator.WidthWide:
		return 2
	case emulator.WidthSpacerTail, emulator.WidthSpacerHead, emulator.WidthUnknown:
		return 0
	default:
		return 1
	}
}
