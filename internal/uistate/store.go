package uistate

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

// DefaultDebounce is how long the store waits for changes to stop before it
// writes. Longer than the gap between two frames of a window drag, shorter
// than the gap between a user releasing the mouse and reaching for Cmd-Q.
const DefaultDebounce = 500 * time.Millisecond

// DefaultSampleInterval is how often Watch asks the platform where the window
// is. Wails v2 offers no moved/resized callback, so a poll is the only seam
// there is; combined with the debounce above, a drag of any length costs
// exactly one write, 500ms after it stops.
const DefaultSampleInterval = 250 * time.Millisecond

// timerHandle is the part of *time.Timer this package uses. It exists so the
// debounce can be driven deterministically from a test instead of by waiting —
// a test that sleeps for the debounce is a test that depends on timing, which
// AGENTS.md forbids and which would be the first thing to go flaky.
type timerHandle interface {
	Stop() bool
	Reset(d time.Duration) bool
}

// afterFunc has time.AfterFunc's shape.
type afterFunc func(d time.Duration, f func()) timerHandle

func realAfterFunc(d time.Duration, f func()) timerHandle { return time.AfterFunc(d, f) }

// Probe is what the platform can tell us about the live window. main.go
// implements it over the Wails runtime and nothing else may: it is the one
// place a Wails context exists, and keeping the interface here is what lets
// every rule above it be tested without a display.
type Probe interface {
	// Geometry reports the window's current size, position and states, and
	// the displays attached right now. ok is false when the platform cannot
	// answer — the sample is then discarded rather than recorded as zeros.
	Geometry() (window Window, displays []Display, ok bool)
}

// Store is the single owner of the UI-state document (AD-8). Reads are served
// from memory; writes are coalesced and land on disk once changes stop.
type Store struct {
	doc storage.DocumentStore
	log *slog.Logger

	debounce time.Duration
	after    afterFunc

	mu    sync.Mutex
	state Document
	dirty bool
	timer timerHandle
	// closed makes Close idempotent and stops a timer that fires during
	// shutdown from scheduling another write behind the final one.
	closed bool
}

// New opens the UI-state document. It never fails: an absent document is an
// ordinary state, and an unreadable one costs the user their window size, not
// their launch. log may be nil.
func New(doc storage.DocumentStore, log *slog.Logger) *Store {
	return newStore(doc, log, DefaultDebounce, realAfterFunc)
}

func newStore(doc storage.DocumentStore, log *slog.Logger, debounce time.Duration, after afterFunc) *Store {
	if log == nil {
		log = slog.Default()
	}
	s := &Store{
		doc:      doc,
		log:      log,
		debounce: debounce,
		after:    after,
		state:    defaultDocument(),
	}
	s.load()
	return s
}

// load reads the document, repairing what it can and falling back to defaults
// for what it cannot. Every failure here is a warning and a default — see the
// table in ADR-0033 §4. It is deliberately quiet about absence: a first launch
// is not a problem worth a log line above Debug.
func (s *Store) load() {
	var stored Document
	found, err := s.doc.Read(DocumentName, &stored)
	if err != nil {
		s.log.Warn("uistate: document unreadable, starting from defaults", "error", err)
		return
	}
	if !found {
		s.log.Debug("uistate: no document yet, starting from defaults")
		return
	}

	raw, err := json.Marshal(stored)
	if err != nil {
		s.log.Warn("uistate: document unusable, starting from defaults", "error", err)
		return
	}
	migrated, err := module.Migrate(raw, storage.SchemaVersion(stored.SchemaVersion))
	if err != nil {
		// Includes storage.ErrVersionTooNew: a document written by a newer
		// build is left exactly as it is and simply not used. Truncating it
		// would cost the user their layout on the build that understands it.
		s.log.Warn("uistate: document version not understood, starting from defaults",
			"error", err, "storedVersion", stored.SchemaVersion)
		return
	}
	var doc Document
	if err := json.Unmarshal(migrated, &doc); err != nil {
		s.log.Warn("uistate: document unusable after migration, starting from defaults", "error", err)
		return
	}
	s.state = sanitise(doc)
}

// Window reports the recorded geometry.
func (s *Store) Window() Window {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Window
}

// SetWindow records geometry and schedules a write. Callers are expected to
// call it freely — the debounce is the store's business, not theirs, which is
// what keeps the write policy in one place instead of one place per caller.
func (s *Store) SetWindow(w Window) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Window == w {
		return
	}
	s.state.Window = w
	s.markDirtyLocked()
}

// Layout reports the renderer's half of the document.
func (s *Store) Layout() Layout {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Layout{Sidebar: s.state.Sidebar, ActiveTab: s.state.ActiveTab}
}

// SetLayout records the renderer's half and schedules a write. The sidebar
// width is clamped here as well as on read, so the wire cannot install a value
// the panel could not lay out.
func (s *Store) SetLayout(l Layout) {
	l.Sidebar.Width = ClampSidebarWidth(l.Sidebar.Width)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Sidebar == l.Sidebar && s.state.ActiveTab == l.ActiveTab {
		return
	}
	s.state.Sidebar = l.Sidebar
	s.state.ActiveTab = l.ActiveTab
	s.markDirtyLocked()
}

// markDirtyLocked starts or restarts the coalescing window. Restarting rather
// than letting the first timer run is what makes it a debounce: the write
// happens after changes STOP, so a drag of any length is one write.
func (s *Store) markDirtyLocked() {
	if s.closed {
		return
	}
	s.dirty = true
	if s.timer == nil {
		s.timer = s.after(s.debounce, s.flush)
		return
	}
	s.timer.Reset(s.debounce)
}

// flush is the timer's callback: write whatever the state is now.
func (s *Store) flush() {
	s.mu.Lock()
	if !s.dirty || s.closed {
		s.mu.Unlock()
		return
	}
	s.dirty = false
	doc := s.state
	s.mu.Unlock()

	if err := s.doc.Write(DocumentName, doc); err != nil {
		// The value stays applied in the running app and the next change
		// retries. There is no UI to contradict here — nothing in the product
		// promises this write succeeded — so a warning is the whole degrade.
		s.log.Warn("uistate: could not write document", "error", err)
	}
}

// Close writes any pending state synchronously and stops the timer, so a clean
// quit inside the debounce window loses nothing. It is safe to call twice.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	dirty := s.dirty
	s.dirty = false
	doc := s.state
	s.mu.Unlock()

	if !dirty {
		return nil
	}
	if err := s.doc.Write(DocumentName, doc); err != nil {
		s.log.Warn("uistate: could not write document at shutdown", "error", err)
		return err
	}
	return nil
}

// Placer applies a placement to the live window. main.go implements it over
// the Wails runtime, next to Probe and for the same reason: this is the only
// place a window exists, and keeping the interface here is what lets the
// sequencing below be tested without a display.
//
// The methods are the platform's own vocabulary — states are entered, not
// computed — so Maximise and Fullscreen take no argument: they are called only
// when the saved state says so.
type Placer interface {
	SetSize(width, height int)
	SetPosition(x, y int)
	Center()
	Maximise()
	Fullscreen()
}

// RestoreAndWatch puts the window back where the user left it and then records
// where they put it, until ctx is cancelled. It is the whole of the window half
// of this package's job, and it is ONE loop on purpose.
//
// THE WINDOW IS NOT THERE YET, AND THAT IS ORDINARY. Under Wails v3 the
// composition root runs during service startup, which is before the platform
// window is realised: `WebviewWindow.Size()` answers (0,0) while the window has
// no implementation behind it. The root used to probe once, at exactly that
// moment, and return when the probe could not answer — which skipped starting
// the sampler for the entire session. Nothing was ever recorded, the document
// kept the zeros it was born with, and every launch restored them as the
// default size (nocx-39vhn).
//
// So readiness is not a precondition here, it is the first thing the loop waits
// for: each tick asks the platform, the first answer places the window, and
// every answer after that is a sample. A window that never becomes readable
// costs one interface call per tick and nothing else — there is no path through
// this function that stops watching while the process lives, which is the
// property the old shape could not state.
//
// Placement happens before the first sample and never after one. That ordering
// is load-bearing in the other direction too: a sample taken before the restore
// had been applied would record where the platform happened to open the window
// and overwrite the position being restored.
//
// Sampling rather than subscribing is deliberate and is the whole of the write
// side: the store coalesces what the poll sees, so a drag of any length costs
// one write half a second after it stops. A still window costs one interface
// call per tick and no writes at all — SetWindow returns immediately when
// nothing moved.
func (s *Store) RestoreAndWatch(ctx context.Context, p Probe, w Placer, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultSampleInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	placed := false
	for {
		if placed {
			s.Sample(p)
		} else if _, displays, ok := p.Geometry(); ok {
			place(w, Restore(s.Window(), displays))
			placed = true
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// place applies one Placement. Separated from the decision above it for the
// reason Restore is separated from the probe: what to do is a rule that can be
// tested, and doing it is a handful of platform calls that cannot.
func place(w Placer, p Placement) {
	w.SetSize(p.Width, p.Height)
	if p.UsePosition {
		w.SetPosition(p.X, p.Y)
	} else {
		// Either nothing was saved or the displays are not the ones the
		// position was recorded on. Centring is the visible answer; the saved
		// position stays in the document, so plugging the monitor back in
		// restores the old arrangement.
		w.Center()
	}
	// States, not pixels: entering them is what makes leaving them land on the
	// normal geometry set just above.
	if p.Maximise {
		w.Maximise()
	}
	if p.FullScreen {
		w.Fullscreen()
	}
}

// Sample takes one reading and folds it into the recorded state.
//
// Exported because RestoreAndWatch's loop is untestable without a clock and
// this is the part with the behaviour in it: a reading the platform could not
// give is discarded rather than recorded as zeros, and a maximised window
// carries its normal geometry forward (see Observe).
func (s *Store) Sample(p Probe) {
	live, displays, ok := p.Geometry()
	if !ok {
		return
	}
	live.Displays = Fingerprint(displays)
	s.SetWindow(Observe(s.Window(), live))
}
