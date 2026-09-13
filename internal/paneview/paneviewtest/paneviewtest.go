// Package paneviewtest is a paneview.Source made of bytes.
//
// # What it is, and what it deliberately is not
//
// The product reads a pane's screen from the runtime beside its PTY, and a unit
// test has no helper to ask. The honest substitute is the same emulator fed the
// same bytes: this package holds a stream per pane and replays it through
// paneview.Replay over internal/emulator/ghostty, so a frame a test asserts on
// is a frame the product would draw.
//
// It is ONLY a source. There is no store in here, no enrolment, no watch set
// and no bound: a test uses the REAL paneview.Store — enrol, withdraw, refusals
// and all — and this supplies the screens behind it. A second store-shaped
// helper would be a second implementation of the interval the product's store
// already is, and the tests would then be asserting about that one.
package paneviewtest

import (
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneview"
)

// Store builds the REAL store over a byte-backed source, and returns both: the
// store for a product seam, the source for the test to feed.
func Store(lg log.Logger) (*paneview.Store, *Screens) {
	s := New()
	return paneview.NewStore(lg, s), s
}

// Views is the store and the bytes behind it, for a test that does both.
//
// The store is EMBEDDED and not re-implemented: every method a test calls on it
// — Enrol, Withdraw, Frame, Watched, Count — is the product's, with the
// product's refusals. The two things added are the SOURCE's Feed and the two
// statements a fixture always writes (declare a size, open the interval).
type Views struct {
	*paneview.Store
	src *Screens
}

// NewViews returns an empty store over a byte-backed source.
func NewViews(lg log.Logger) *Views {
	store, src := Store(lg)
	return &Views{Store: store, src: src}
}

// Source is the byte source, for a test that needs to stop it answering.
func (v *Views) Source() *Screens { return v.src }

// Feed appends bytes to a watched pane's stream.
func (v *Views) Feed(paneID string, b []byte) { v.src.Feed(paneID, b) }

// Size declares a pane's geometry and makes it available, for a stand that
// opens sessions it cannot enumerate up front: the pane exists from the moment
// the session does, which is what the helper's runtime is in production.
func (v *Views) Size(paneID string, cols, rows int) { v.src.Size(paneID, cols, rows) }

// Watch declares a pane's geometry and opens the interval over it.
func (v *Views) Watch(paneID string, cols, rows int) error {
	return Watch(v.Store, v.src, paneID, cols, rows)
}

// Watch declares a pane and opens the interval over it — the two statements
// every fixture writes, in the order the store's own contract requires: the
// interval is opened only over a pane the source can answer for.
//
// It is a function and not a type: the store, the interval and the refusals are
// the product's, and a test that wanted a second implementation of any of them
// would be asserting about that one instead.
func Watch(store *paneview.Store, src *Screens, paneID string, cols, rows int) error {
	src.Size(paneID, cols, rows)
	return store.Enrol(paneID)
}

// Screens is the source: one stream per pane, replayed from byte zero on every
// read.
type Screens struct {
	mu    sync.Mutex
	panes map[string]*pane
}

// New returns an empty source. Nothing is available until a pane is declared.
func New() *Screens { return &Screens{panes: map[string]*pane{}} }

// Size declares the geometry a pane's bytes are replayed at, and makes the pane
// available.
//
// The size is stated here rather than taken from the stream because a stream
// has none: it is what the session was opened at, and a read that invented a
// geometry would make every width assertion depend on the reader.
func (s *Screens) Size(paneID string, cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.panes[paneID]
	if !ok {
		p = &pane{}
		s.panes[paneID] = p
	}
	p.cols, p.rows = cols, rows
}

// Feed appends bytes to a pane's stream.
//
// The geometry is NOT taken here: it belongs to the session, and a pane fed
// before anybody declared one fails at the READ with a named error rather than
// being replayed at a size this package invented.
func (s *Screens) Feed(paneID string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.panes[paneID]
	if !ok {
		p = &pane{}
		s.panes[paneID] = p
	}
	p.data = append(p.data, b...)
}

// Set pins a pane to an exact frame, for a test whose subject is a reading
// rather than a stream: a rule's predicate reads one row, and painting the
// bytes that produce it is more machinery than the assertion needs.
func (s *Screens) Set(paneID string, f paneview.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panes[paneID] = &pane{fixed: &f}
}

// Forget drops a pane, for a test that needs a source which stops answering.
func (s *Screens) Forget(paneID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.panes, paneID)
}

func (s *Screens) Available(paneID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.panes[paneID]; !ok {
		return fmt.Errorf("paneviewtest: %s has no runtime in this source", paneID)
	}
	return nil
}

func (s *Screens) Screen(paneID string) (paneview.Frame, error) {
	s.mu.Lock()
	p, ok := s.panes[paneID]
	if !ok {
		s.mu.Unlock()
		return paneview.Frame{}, fmt.Errorf("paneviewtest: %s has no runtime in this source", paneID)
	}
	if p.fixed != nil {
		f := *p.fixed
		s.mu.Unlock()
		return f, nil
	}
	cols, rows, data := p.cols, p.rows, append([]byte(nil), p.data...)
	s.mu.Unlock()
	if cols <= 0 || rows <= 0 {
		return paneview.Frame{}, errors.New("paneviewtest: pane has no declared size")
	}
	frames, err := paneview.Replay(ghostty.New, emulator.Geometry{Cols: cols, Rows: rows}, [][]byte{data}, []int{1})
	if err != nil {
		return paneview.Frame{}, err
	}
	return frames[0], nil
}

type pane struct {
	cols, rows int
	data       []byte
	// fixed, when set, is answered as-is: a test that wants an exact frame
	// rather than the one a stream produces.
	fixed *paneview.Frame
}
