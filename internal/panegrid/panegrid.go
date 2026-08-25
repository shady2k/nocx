// Package panegrid keeps a live VT grid for an ENROLLED pane, in the backend,
// so that what a pane is doing can be read while no client is attached.
//
// # This crosses AD-6 on purpose, and the boundary is written down
//
// AD-6 forbids the backend to derive meaning from the byte stream and gives
// the renderer sole ownership of VT state. This package derives meaning from
// the byte stream. The amendment beside AD-6 in docs/architecture.md permits
// it under two constraints, and both are structural here rather than a matter
// of care:
//
//   - THE INTERVAL. A grid exists only between an explicit Enrol and the
//     matching Withdraw. Enrolment is an ACT — a control-plane call naming a
//     pane — and never an inference that a pane's title looked like an agent,
//     because an inferred set has no upper bound and no audit. Nothing in this
//     package can enrol anything by itself.
//
//   - THE TWO POWERS. What a caller may learn from a grid is a Frame: what is
//     on the screen and where the cursor is. That is all this package can
//     answer, and it is enough for exactly the two decisions the amendment
//     allows — whether nocx may write into this pane, and what its indicator
//     shows. Neither decision is made here; both are made by callers reading a
//     Frame.
//
// # What it may never do, enforced by what it cannot name
//
// A grid may not open, complete, alter or assign status to a wave state, a
// lifecycle attempt or an execution attempt. That is not a rule this package
// follows, it is a rule it cannot break: it imports no lifecycle, session,
// content or notify package, and nothing in its exported surface names one.
// boundary_test.go asserts exactly that by reading this package's own imports,
// so the day somebody adds one the test fails rather than the invariant.
//
// # Why a live grid and not a re-parse of the tail
//
// A TUI sends diffs, not full repaints, so a frame cannot be reconstructed
// from the tail of a stream — the emulator has to be fed continuously from
// byte zero of a process we launched. That is the whole reason the state lives
// here rather than being computed on demand.
//
// The emulator is github.com/charmbracelet/x/vt, chosen by measurement in
// nocx-szb40.1 and recorded in docs/decisions/0038-x-vt-as-the-backend-emulator.md:
// it is the only candidate that reproduces xterm.js's COLUMN GEOMETRY across a
// double-width character, and both powers above are positional.
package panegrid

import (
	"errors"
	"fmt"
	"io"
	"sync"

	xvt "github.com/charmbracelet/x/vt"

	"github.com/shady2k/nocx/internal/log"
)

// MaxEnrolled bounds how many panes may hold a grid at once. The amendment
// says enrolment is bounded by being an explicit act; this is the second
// bound, against a caller that enrols in a loop. A grid is one VT emulator
// and its scrollback, so the cost is real.
const MaxEnrolled = 64

var (
	// ErrNotEnrolled is returned for a pane that has no grid. It is a normal
	// answer, not a failure: most panes never have one.
	ErrNotEnrolled = errors.New("panegrid: pane is not enrolled")
	// ErrAlreadyEnrolled means Enrol was called twice for one pane. Enrolling
	// again would silently discard the grid built so far, losing the byte-zero
	// guarantee that makes the frame trustworthy, so it is refused instead.
	ErrAlreadyEnrolled = errors.New("panegrid: pane is already enrolled")
	// ErrTooManyEnrolled means MaxEnrolled grids already exist.
	ErrTooManyEnrolled = errors.New("panegrid: too many panes enrolled")
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

// Frame is everything a caller may learn from a grid: what is on the screen
// and where the cursor is. There is deliberately nothing else here — no
// classification, no verdict, no status — because a verdict computed in this
// package would be a third power the amendment does not grant.
type Frame struct {
	Cols, Rows int
	CursorX    int
	CursorY    int
	// AltScreen reports whether the pane is in the alternate screen. It is an
	// observation like any other and decides nothing on its own; ADR-0024
	// decision 1 forbids it to open or complete an execution attempt, and
	// nothing here could.
	AltScreen bool
	// Lines is Rows long; each is Cols long.
	Lines [][]Cell
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

// Observer is the seam (AD-8). One implementation lives here; a test double
// lives in the packages that consume it.
type Observer interface {
	// Enrol opens the interval for a pane. It must be called before the first
	// byte of the process, or the byte-zero guarantee does not hold and the
	// frame describes a screen whose earlier state was never seen.
	Enrol(paneID string, cols, rows int) error
	// Withdraw closes the interval and discards the grid. Idempotent: closing
	// something already closed is not an error, because the caller racing a
	// session teardown should not have to care who won.
	Withdraw(paneID string)
	// Feed hands bytes to an enrolled pane's grid. Bytes for a pane that is
	// not enrolled are DROPPED, not buffered — this is the hot path of every
	// session in the product and an unenrolled pane must cost nothing.
	Feed(paneID string, b []byte)
	// Resize follows the pane's geometry for the life of the interval. It is
	// not housekeeping: both permitted powers are POSITIONAL, so a grid left
	// at the size it was enrolled at does not go stale, it goes wrong — the
	// program repaints at the new width while the emulator keeps wrapping at
	// the old one, and every column a caller reads is off by the difference.
	Resize(paneID string, cols, rows int) error
	// Frame returns what is on the screen now.
	Frame(paneID string) (Frame, error)
	// Enrolled reports whether a pane holds a grid.
	Enrolled(paneID string) bool
}

type grid struct {
	mu   sync.Mutex
	term *xvt.Emulator
	// drained closes when the reply-drain goroutine has returned, so Withdraw
	// can be sure nothing is still touching the emulator.
	drained chan struct{}
}

// Store is the Observer implementation.
type Store struct {
	log  log.Logger
	mu   sync.RWMutex
	grid map[string]*grid
}

// New returns an empty Store. Nothing is enrolled until somebody says so.
func New(logger log.Logger) *Store {
	return &Store{log: logger, grid: make(map[string]*grid)}
}

func (s *Store) Enrol(paneID string, cols, rows int) error {
	if paneID == "" {
		return fmt.Errorf("panegrid: empty pane id")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("panegrid: enrol %q: size must be positive, got %dx%d", paneID, cols, rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.grid[paneID]; ok {
		return ErrAlreadyEnrolled
	}
	if len(s.grid) >= MaxEnrolled {
		return ErrTooManyEnrolled
	}
	e := xvt.NewEmulator(cols, rows)
	g := &grid{term: e, drained: make(chan struct{})}
	// The emulator REPLIES upstream — device attributes, in-band resize and
	// friends — through an unbuffered io.Pipe, so Write BLOCKS the moment
	// nothing is reading them. A real terminal always has a reader on the
	// other side; here nobody wants the replies, so they are read and dropped.
	// Without this the session pump would stall on the first agent that asks
	// what terminal it is talking to, which every agent TUI does — measured in
	// nocx-szb40.1, where the first real capture deadlocked.
	go func() {
		defer close(g.drained)
		buf := make([]byte, 4096)
		for {
			if _, err := e.Read(buf); err != nil {
				if !errors.Is(err, io.EOF) {
					s.log.Debug("panegrid reply drain ended", "pane_id", paneID, "error", err)
				}
				return
			}
		}
	}()
	s.grid[paneID] = g
	s.log.Debug("panegrid enrolled", "pane_id", paneID, "cols", cols, "rows", rows)
	return nil
}

func (s *Store) Withdraw(paneID string) {
	s.mu.Lock()
	g, ok := s.grid[paneID]
	delete(s.grid, paneID)
	s.mu.Unlock()
	if !ok {
		return
	}
	// Stop the drain by closing the pipe's WRITE end rather than by calling
	// Emulator.Close. Both unblock the reader, and only one of them is safe:
	// Close sets an `closed` field that Read tests without synchronisation, so
	// the pair races — go test -race says so, on this package's own suite.
	// InputPipe returns that same *io.PipeWriter, and closing it makes the
	// blocked Read return EOF without any emulator field being written.
	//
	// Then wait for the goroutine to actually be gone. Withdraw returning
	// while something still holds the emulator is the interval failing to
	// close, which is the shape AGENTS.md names: an invariant with a start and
	// no end.
	if c, ok := g.term.InputPipe().(io.Closer); ok {
		_ = c.Close()
	}
	<-g.drained
	g.mu.Lock()
	_ = g.term.Close()
	g.mu.Unlock()
	s.log.Debug("panegrid withdrawn", "pane_id", paneID)
}

func (s *Store) Feed(paneID string, b []byte) {
	if len(b) == 0 {
		return
	}
	s.mu.RLock()
	g, ok := s.grid[paneID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := g.term.Write(b); err != nil {
		s.log.Debug("panegrid write failed", "pane_id", paneID, "error", err)
	}
}

func (s *Store) Resize(paneID string, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("panegrid: resize %q: size must be positive, got %dx%d", paneID, cols, rows)
	}
	s.mu.RLock()
	g, ok := s.grid[paneID]
	s.mu.RUnlock()
	if !ok {
		// The ordinary answer, not a failure: most panes never hold a grid
		// and every one of them is resized. The caller sits on the session's
		// resize path and must not have to ask first.
		return ErrNotEnrolled
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.term.Resize(cols, rows)
	s.log.Debug("panegrid resized", "pane_id", paneID, "cols", cols, "rows", rows)
	return nil
}

func (s *Store) Frame(paneID string) (Frame, error) {
	s.mu.RLock()
	g, ok := s.grid[paneID]
	s.mu.RUnlock()
	if !ok {
		return Frame{}, ErrNotEnrolled
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	cols, rows := g.term.Width(), g.term.Height()
	pos := g.term.CursorPosition()
	f := Frame{
		Cols: cols, Rows: rows,
		CursorX: pos.X, CursorY: pos.Y,
		AltScreen: g.term.IsAltScreen(),
		Lines:     make([][]Cell, rows),
	}
	for y := 0; y < rows; y++ {
		line := make([]Cell, cols)
		for x := 0; x < cols; x++ {
			c := g.term.CellAt(x, y)
			if c == nil {
				line[x] = Cell{Text: " ", Width: 1}
				continue
			}
			line[x] = Cell{Text: c.Content, Width: c.Width}
		}
		f.Lines[y] = line
	}
	return f, nil
}

func (s *Store) Enrolled(paneID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.grid[paneID]
	return ok
}

// Count reports how many panes hold a grid. For the bound and for tests.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.grid)
}
