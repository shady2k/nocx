package paneview

import (
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// MaxWatched bounds how many panes nocx may watch at once.
//
// The amended AD-6 says watching is bounded by being an explicit act; this is
// the second bound, against a caller that enrols in a loop. It used to be the
// bound on how many EMULATORS the coordinator held (panegrid.MaxEnrolled, same
// value), and the subject changed with ADR-0066's lifetime supersession: what a
// loop can now exhaust is not memory here but the helper's willingness to
// answer a frame read per pane.
const MaxWatched = 64

var (
	// ErrNotWatched is returned for a pane nobody is watching. It is a normal
	// answer, not a failure: most panes are never watched.
	ErrNotWatched = errors.New("paneview: pane is not watched")
	// ErrAlreadyWatched means Enrol was called twice for one pane. Watching
	// again would be harmless to the runtime and is refused anyway, because a
	// second enrolment is a caller that has lost track of the first and the
	// sentence it gets back is how it finds out.
	ErrAlreadyWatched = errors.New("paneview: pane is already watched")
	// ErrTooManyWatched means MaxWatched panes are watched already.
	ErrTooManyWatched = errors.New("paneview: too many panes watched")
)

// Source is the seam onto the runtime that owns a pane's terminal (AD-8).
//
// Two methods, and the second is not redundant with the first. Enrolment has to
// decide whether a pane CAN be read before it opens a watch, and it may not ask
// by reading a frame: [Store.Frame] refuses an unwatched pane, so a caller that
// probed with it would be refused by its own interval. Available is the cheap
// question, Screen is the read.
//
// Both are synchronous and neither takes a context, which is deliberate: the
// callers are a 120ms sweeper and a write gate that is already holding a
// decision, and neither has a lifetime to thread. An implementation that
// reaches another process therefore bounds its own call, the way
// internal/helper/client's signal seam does — a REQUEST bound, not a policy.
type Source interface {
	// Available reports whether this pane's screen can be read at all, or why
	// it cannot. It is asked once, at enrolment, and never on the sweep.
	Available(paneID string) error
	// Screen reads the pane's frame as the runtime holds it now.
	Screen(paneID string) (Frame, error)
}

// Store is the set of panes nocx is watching, and the reader over the runtimes
// that own them.
//
// It holds no terminal state (see the package doc): the two things it owns are
// the interval the amendment requires — which panes may be read, and for how
// long — and the bound on how many of them there may be.
type Store struct {
	log log.Logger
	src Source

	mu      sync.RWMutex
	watched map[string]struct{}
}

// NewStore returns an empty Store. Nothing is watched until somebody says so.
func NewStore(lg log.Logger, src Source) *Store {
	return &Store{log: lg, src: src, watched: make(map[string]struct{})}
}

// Enrol opens the interval for one pane: from here it may be read, and a
// withdrawal is what closes it.
//
// THE ORDER OF THE THREE CHECKS IS THE POINT. Already-watched first, because a
// caller that lost track of its own enrolment should learn that without a round
// trip; the bound second, for the same reason; and only then the question of
// whether the pane's runtime can be read at all. A pane whose screen is not
// readable is refused BEFORE a watch exists, so a refusal never leaves an
// interval open over a runtime that cannot answer it — which is the difference
// between a pane that says it is not being watched and a pane that is watched
// into silence.
func (s *Store) Enrol(paneID string) error {
	if paneID == "" {
		return fmt.Errorf("paneview: empty pane id")
	}
	s.mu.Lock()
	if _, ok := s.watched[paneID]; ok {
		s.mu.Unlock()
		return ErrAlreadyWatched
	}
	if len(s.watched) >= MaxWatched {
		s.mu.Unlock()
		return ErrTooManyWatched
	}
	s.mu.Unlock()

	// Outside the lock: this can be a round trip to the process that owns the
	// pane, and holding the store while waiting on it would stop every other
	// pane's frame read behind one unreachable host.
	if err := s.src.Available(paneID); err != nil {
		return err
	}

	s.mu.Lock()
	// Re-checked under the lock: two enrolments of one pane racing is the
	// ordinary case (a wrapper that retries), and the second must not make the
	// first's withdrawal close an interval it did not open.
	if _, ok := s.watched[paneID]; ok {
		s.mu.Unlock()
		return ErrAlreadyWatched
	}
	if len(s.watched) >= MaxWatched {
		s.mu.Unlock()
		return ErrTooManyWatched
	}
	s.watched[paneID] = struct{}{}
	s.mu.Unlock()
	s.log.Debug("pane watched", "pane_id", paneID)
	return nil
}

// Withdraw closes the interval and forgets the pane. Idempotent: closing
// something already closed is not an error, because the caller racing a session
// teardown should not have to care who won.
//
// It touches NO runtime. The runtime is the session's and lives exactly as long
// as the PTY does; withdrawing a watch is a statement about who may read, and
// the closing end of the runtime's own interval is the session ending.
func (s *Store) Withdraw(paneID string) {
	s.mu.Lock()
	_, ok := s.watched[paneID]
	delete(s.watched, paneID)
	s.mu.Unlock()
	if ok {
		s.log.Debug("pane unwatched", "pane_id", paneID)
	}
}

// Watched reports whether a pane may be read.
func (s *Store) Watched(paneID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.watched[paneID]
	return ok
}

// Count reports how many panes are watched. For the bound and for tests.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.watched)
}

// Frame reads what is on a watched pane's screen now.
//
// A pane that is not watched is refused rather than read: the interval is the
// only thing that makes a read permitted (AD-6's amendment, whose lifetime
// clause ADR-0066 superseded for WHERE the emulator lives and not for WHO may
// look at it), and a store that answered anyway would be a second, quieter way
// to watch a pane.
func (s *Store) Frame(paneID string) (Frame, error) {
	if !s.Watched(paneID) {
		return Frame{}, fmt.Errorf("%w: %s", ErrNotWatched, paneID)
	}
	return s.src.Screen(paneID)
}

// Replay feeds a capture's bytes to a fresh terminal and answers the screen
// after each entry of through.
//
// `through` is how many chunks have been consumed at each mark, non-decreasing
// — the arithmetic belongs to whoever owns the capture FORMAT
// (internal/agentcapture's ChunksThrough), and it is passed in rather than
// recomputed here because a second derivation of "which chunk belongs to this
// mark" is the kind of pair that agrees until the day it does not.
//
// The replies an ingest produces are DROPPED, and that is correct rather than
// convenient: they are the recorded program's own questions — a cursor report,
// a device status — and the recorded stream already contains whatever answered
// them. Writing them anywhere would be inventing a terminal that answered a
// question twice.
func Replay(newScreen ScreenFactory, g emulator.Geometry, chunks [][]byte, through []int) ([]Frame, error) {
	if newScreen == nil {
		return nil, errors.New("paneview: replay has no screen factory")
	}
	if !g.Valid() {
		return nil, fmt.Errorf("paneview: replay geometry is %dx%d: %w", g.Cols, g.Rows, emulator.ErrOutOfRange)
	}
	term, err := newScreen(g)
	if err != nil {
		return nil, fmt.Errorf("paneview: replay screen: %w", err)
	}
	defer term.Close()

	out := make([]Frame, 0, len(through))
	consumed := 0
	for i, mark := range through {
		if mark < consumed {
			return nil, fmt.Errorf("paneview: replay marks must not go backwards; %d follows %d", mark, consumed)
		}
		if mark > len(chunks) {
			return nil, fmt.Errorf("paneview: replay mark %d of %d chunks: %w", mark, len(chunks), emulator.ErrOutOfRange)
		}
		for ; consumed < mark; consumed++ {
			if _, err := term.Ingest(chunks[consumed]); err != nil {
				return nil, fmt.Errorf("paneview: replay chunk %d: %w", consumed, err)
			}
		}
		f, err := From(term)
		if err != nil {
			return nil, fmt.Errorf("paneview: replay frame %d: %w", i, err)
		}
		// Every byte the caller asked to be shown reached this terminal. It is
		// the whole of the interval the marks describe, which is why a replay
		// is complete where a live session may not be.
		f.Completeness = sessionruntime.CompletenessComplete
		out = append(out, f)
	}
	return out, nil
}
