package uistate

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

// ── Fingerprint ────────────────────────────────────────────────────────

func TestFingerprintIsOrderIndependent(t *testing.T) {
	// The platform's enumeration order is not guaranteed stable across
	// launches. If it leaked into the fingerprint, every restart would report
	// "the displays changed" and no position would ever be restored.
	a := []Display{
		{Width: 1920, Height: 1080},
		{Width: 2560, Height: 1440, Primary: true},
	}
	b := []Display{
		{Width: 2560, Height: 1440, Primary: true},
		{Width: 1920, Height: 1080},
	}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatalf("order changed the fingerprint: %q vs %q", Fingerprint(a), Fingerprint(b))
	}
}

func TestFingerprintDistinguishesDisplaySets(t *testing.T) {
	two := []Display{{Width: 2560, Height: 1440, Primary: true}, {Width: 1920, Height: 1080}}
	one := []Display{{Width: 2560, Height: 1440, Primary: true}}
	if Fingerprint(two) == Fingerprint(one) {
		t.Fatal("unplugging a monitor must change the fingerprint")
	}
	// A resized (or rescaled) display is a different arrangement too.
	resized := []Display{{Width: 2560, Height: 1440, Primary: true}, {Width: 1280, Height: 720}}
	if Fingerprint(two) == Fingerprint(resized) {
		t.Fatal("a different display size must change the fingerprint")
	}
}

func TestFingerprintOfNoDisplaysIsUnknown(t *testing.T) {
	if got := Fingerprint(nil); got != "" {
		t.Fatalf("Fingerprint(nil) = %q, want the empty 'unknown' marker", got)
	}
}

func TestFingerprintIsReadable(t *testing.T) {
	// The string lands in a file a human may open; it says so in the doc
	// comment, so it is asserted rather than left as an intention.
	got := Fingerprint([]Display{{Width: 2560, Height: 1440, Primary: true}, {Width: 1920, Height: 1080}})
	want := "2:1920x1080,2560x1440p"
	if got != want {
		t.Fatalf("Fingerprint = %q, want %q", got, want)
	}
}

// ── Restore ────────────────────────────────────────────────────────────

func TestRestoreOnTheSameDisplaysKeepsSizeAndPosition(t *testing.T) {
	displays := []Display{{Width: 2560, Height: 1440, Primary: true}}
	saved := Window{Width: 1440, Height: 900, X: 120, Y: 64, Displays: Fingerprint(displays)}

	got := Restore(saved, displays)

	if !got.UsePosition {
		t.Fatal("same displays: the position must be restored")
	}
	if got.X != 120 || got.Y != 64 {
		t.Fatalf("position = (%d,%d), want (120,64)", got.X, got.Y)
	}
	if got.Width != 1440 || got.Height != 900 {
		t.Fatalf("size = %dx%d, want 1440x900", got.Width, got.Height)
	}
}

func TestRestoreOnAMissingDisplayDropsThePosition(t *testing.T) {
	// The bug this is the whole point of: geometry saved with a second
	// monitor attached, reopened without it. A window at x=3000 on a single
	// 1920px display is one the user cannot see and cannot drag.
	twoDisplays := []Display{
		{Width: 1920, Height: 1080, Primary: true},
		{Width: 1920, Height: 1080},
	}
	saved := Window{Width: 1400, Height: 900, X: 3000, Y: 40, Displays: Fingerprint(twoDisplays)}

	got := Restore(saved, []Display{{Width: 1920, Height: 1080, Primary: true}})

	if got.UsePosition {
		t.Fatal("the display set changed: the saved position must NOT be applied")
	}
	if got.Width != 1400 || got.Height != 900 {
		t.Fatalf("size = %dx%d; only the position is dropped, not the size", got.Width, got.Height)
	}
}

func TestRestoreWithoutDisplayInformationDropsThePosition(t *testing.T) {
	// When the platform cannot tell us what is attached, we open somewhere
	// visible rather than guessing.
	saved := Window{Width: 1400, Height: 900, X: 3000, Y: 40, Displays: "2:1920x1080,1920x1080p"}
	if Restore(saved, nil).UsePosition {
		t.Fatal("no display information: the saved position must not be applied")
	}
}

func TestRestoreOfANeverSavedWindowIsTheVisibleDefault(t *testing.T) {
	got := Restore(Window{}, []Display{{Width: 2560, Height: 1440, Primary: true}})

	if got.UsePosition {
		t.Fatal("nothing was ever saved: there is no position to apply")
	}
	if got.Width != DefaultWindowWidth || got.Height != DefaultWindowHeight {
		t.Fatalf("size = %dx%d, want the declared default %dx%d",
			got.Width, got.Height, DefaultWindowWidth, DefaultWindowHeight)
	}
}

func TestRestoreClampsASizeLargerThanTheDisplay(t *testing.T) {
	// Saved on a 4K display, reopened on a laptop. The fingerprint differs, so
	// the position goes; the size has to be clamped or the window opens with
	// its controls off the bottom of the screen.
	big := []Display{{Width: 3840, Height: 2160, Primary: true}}
	saved := Window{Width: 3200, Height: 2000, X: 10, Y: 10, Displays: Fingerprint(big)}

	got := Restore(saved, []Display{{Width: 1440, Height: 900, Primary: true}})

	if got.Width > 1440 || got.Height > 900 {
		t.Fatalf("size = %dx%d, want it clamped to the 1440x900 display", got.Width, got.Height)
	}
}

func TestRestoreClampsOnAMatchingFingerprintToo(t *testing.T) {
	// The clamp is not part of the mismatch path: a hand-edited document on an
	// unchanged machine must not produce an unusable window either.
	displays := []Display{{Width: 1440, Height: 900, Primary: true}}
	saved := Window{Width: 99999, Height: 99999, X: 0, Y: 0, Displays: Fingerprint(displays)}

	got := Restore(saved, displays)

	if got.Width != 1440 || got.Height != 900 {
		t.Fatalf("size = %dx%d, want it clamped to the display even when the displays match", got.Width, got.Height)
	}
}

func TestRestoreEnforcesTheMinimumSize(t *testing.T) {
	displays := []Display{{Width: 2560, Height: 1440, Primary: true}}
	saved := Window{Width: 10, Height: 10, Displays: Fingerprint(displays)}

	got := Restore(saved, displays)

	if got.Width != MinWindowWidth || got.Height != MinWindowHeight {
		t.Fatalf("size = %dx%d, want the declared minimum %dx%d",
			got.Width, got.Height, MinWindowWidth, MinWindowHeight)
	}
}

func TestRestoreCarriesMaximisedAndFullScreenAsStates(t *testing.T) {
	displays := []Display{{Width: 2560, Height: 1440, Primary: true}}
	saved := Window{
		Width: 1200, Height: 800, X: 10, Y: 10,
		Maximised: true, Displays: Fingerprint(displays),
	}

	got := Restore(saved, displays)

	if !got.Maximise {
		t.Fatal("a maximised window must be restored maximised")
	}
	if got.Width != 1200 || got.Height != 800 {
		t.Fatalf("size = %dx%d; the NORMAL geometry is what unmaximising must land on", got.Width, got.Height)
	}

	full := Restore(Window{FullScreen: true, Displays: Fingerprint(displays)}, displays)
	if !full.FullScreen {
		t.Fatal("a full-screen window must be restored full-screen")
	}
}

// ── Observe ────────────────────────────────────────────────────────────

func TestObserveKeepsNormalGeometryWhileMaximised(t *testing.T) {
	// The platform reports the MAXIMISED size as the window size. Recording it
	// would give a window that looks maximised and unmaximises to the wrong
	// place, which is the "states, not pixels" rule in ADR-0033 §6.4.
	prev := Window{Width: 1200, Height: 800, X: 40, Y: 40, Displays: "1:2560x1440p"}
	live := Window{Width: 2560, Height: 1400, X: 0, Y: 0, Maximised: true, Displays: "1:2560x1440p"}

	got := Observe(prev, live)

	if got.Width != 1200 || got.Height != 800 || got.X != 40 || got.Y != 40 {
		t.Fatalf("got %+v, want the normal geometry carried forward", got)
	}
	if !got.Maximised {
		t.Fatal("the maximised flag must be recorded")
	}
}

func TestObserveRecordsANormalWindowVerbatim(t *testing.T) {
	prev := Window{Width: 1200, Height: 800}
	live := Window{Width: 1400, Height: 900, X: 12, Y: 24, Displays: "1:2560x1440p"}
	if got := Observe(prev, live); got != live {
		t.Fatalf("got %+v, want %+v", got, live)
	}
}

func TestObserveAcceptsAWindowThatStartedMaximised(t *testing.T) {
	// Nothing normal was ever recorded, so there is nothing to carry forward
	// and the maximised size is better than zeros.
	live := Window{Width: 2560, Height: 1400, Maximised: true}
	if got := Observe(Window{}, live); got.Width != 2560 {
		t.Fatalf("got %+v, want the live size when no normal geometry exists", got)
	}
}

// ── ClampSidebarWidth ──────────────────────────────────────────────────

func TestClampSidebarWidth(t *testing.T) {
	cases := map[string]struct{ in, want int }{
		"inside the bounds": {320, 320},
		"below the minimum": {100, MinSidebarWidth},
		"above the maximum": {900, MaxSidebarWidth},
		"absent":            {0, DefaultSidebarWidth},
		"negative":          {-5, DefaultSidebarWidth},
	}
	for name, tc := range cases {
		if got := ClampSidebarWidth(tc.in); got != tc.want {
			t.Errorf("%s: ClampSidebarWidth(%d) = %d, want %d", name, tc.in, got, tc.want)
		}
	}
}

// ── Store: reading ─────────────────────────────────────────────────────

func TestNewOnAnAbsentDocumentYieldsWorkingDefaults(t *testing.T) {
	// Absence is an ordinary state, never an error path a user sees.
	s := New(storage.NewDocumentStore(t.TempDir()), quietLogger())
	defer func() { _ = s.Close() }()

	if got := s.Window(); got != (Window{}) {
		t.Fatalf("Window() = %+v, want the zero window", got)
	}
	if got := s.Layout().Sidebar.Width; got != DefaultSidebarWidth {
		t.Fatalf("sidebar width = %d, want the default %d", got, DefaultSidebarWidth)
	}
	// And the default is a value that WORKS: Restore turns it into a window.
	p := Restore(s.Window(), []Display{{Width: 1440, Height: 900, Primary: true}})
	if p.Width < MinWindowWidth || p.Height < MinWindowHeight {
		t.Fatalf("defaults do not produce a usable window: %+v", p)
	}
}

func TestNewOnAnUnparseableDocumentYieldsDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DocumentName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(storage.NewDocumentStore(dir), quietLogger())
	defer func() { _ = s.Close() }()

	if got := s.Layout().Sidebar.Width; got != DefaultSidebarWidth {
		t.Fatalf("sidebar width = %d, want the default after a bad document", got)
	}
}

func TestNewOnAnUnreadableDocumentYieldsDefaults(t *testing.T) {
	// The failure path for every external call this code makes: the read
	// fails, and the app still starts.
	s := New(&failingDocStore{readErr: errors.New("permission denied")}, quietLogger())
	defer func() { _ = s.Close() }()

	if got := s.Layout().Sidebar.Width; got != DefaultSidebarWidth {
		t.Fatalf("sidebar width = %d, want the default after an unreadable document", got)
	}
}

func TestNewOnADocumentFromANewerBuildLeavesItAlone(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"schemaVersion":99,"sidebar":{"width":300}}`)
	path := filepath.Join(dir, DocumentName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(storage.NewDocumentStore(dir), quietLogger())
	defer func() { _ = s.Close() }()

	if got := s.Layout().Sidebar.Width; got != DefaultSidebarWidth {
		t.Fatalf("sidebar width = %d, want the default: a version we cannot read is not used", got)
	}
	// And nothing was truncated: the build that understands it still can.
	after, err := os.ReadFile(path) //nolint:gosec // path is the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatalf("document was rewritten: %s", after)
	}
}

func TestNewRepairsOneBadFieldWithoutDiscardingTheRest(t *testing.T) {
	// A single unknown or absurd field must not cost the user their geometry.
	dir := t.TempDir()
	body := `{"schemaVersion":1,"window":{"width":1400,"height":900,"x":10,"y":20,` +
		`"displays":"1:1440x900p"},"sidebar":{"width":99999,"collapsed":true},"activeTab":"pane-7"}`
	if err := os.WriteFile(filepath.Join(dir, DocumentName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(storage.NewDocumentStore(dir), quietLogger())
	defer func() { _ = s.Close() }()

	if got := s.Layout().Sidebar.Width; got != MaxSidebarWidth {
		t.Fatalf("sidebar width = %d, want it clamped to %d", got, MaxSidebarWidth)
	}
	if w := s.Window(); w.Width != 1400 || w.X != 10 {
		t.Fatalf("window = %+v, want the geometry kept even though a sibling field was repaired", w)
	}
	if !s.Layout().Sidebar.Collapsed || s.Layout().ActiveTab != "pane-7" {
		t.Fatalf("layout = %+v, want the good fields kept", s.Layout())
	}
}

// ── Store: writing, and the debounce ───────────────────────────────────

func TestAChangeIsNotWrittenUntilChangesStop(t *testing.T) {
	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)

	s.SetWindow(Window{Width: 1000, Height: 700})
	if rec.writes() != 0 {
		t.Fatalf("wrote %d times before the debounce elapsed; a drag must not hammer the disk", rec.writes())
	}

	clock.fire()
	if rec.writes() != 1 {
		t.Fatalf("wrote %d times after the debounce elapsed, want exactly 1", rec.writes())
	}
}

func TestADragOfManyEventsCostsOneWrite(t *testing.T) {
	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)

	for w := 800; w < 900; w++ {
		s.SetWindow(Window{Width: w, Height: 700})
	}
	if got := clock.resets(); got != 99 {
		t.Fatalf("the coalescing window was restarted %d times, want one per change after the first", got)
	}
	if rec.writes() != 0 {
		t.Fatalf("wrote %d times mid-drag", rec.writes())
	}

	clock.fire()

	if rec.writes() != 1 {
		t.Fatalf("100 changes cost %d writes, want 1", rec.writes())
	}
	if got := rec.last().Window.Width; got != 899 {
		t.Fatalf("wrote width %d, want the last value of the drag (899)", got)
	}
}

func TestAnUnchangedSampleWritesNothing(t *testing.T) {
	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)

	s.SetWindow(Window{Width: 1000, Height: 700})
	clock.fire()
	before := rec.writes()

	s.SetWindow(Window{Width: 1000, Height: 700})
	clock.fire()

	if rec.writes() != before {
		t.Fatalf("a still window produced a write: %d then %d", before, rec.writes())
	}
}

func TestCloseFlushesTheLastChange(t *testing.T) {
	// A clean quit inside the debounce window must lose nothing.
	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)

	s.SetWindow(Window{Width: 1234, Height: 777})
	if rec.writes() != 0 {
		t.Fatal("wrote before the debounce elapsed")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rec.writes() != 1 {
		t.Fatalf("Close wrote %d times, want 1", rec.writes())
	}
	if got := rec.last().Window.Width; got != 1234 {
		t.Fatalf("Close wrote width %d, want 1234", got)
	}
}

func TestCloseIsIdempotentAndSilencesTheTimer(t *testing.T) {
	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)

	s.SetWindow(Window{Width: 900, Height: 600})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A timer already in flight when shutdown began must not write behind it.
	clock.fire()
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if rec.writes() != 1 {
		t.Fatalf("wrote %d times across shutdown, want 1", rec.writes())
	}
}

func TestAFailedWriteIsSurvivedAndRetriedOnTheNextChange(t *testing.T) {
	// Every external call has a test where it fails. Here the value stays
	// applied in the running app; the next change tries again.
	fail := &failingDocStore{writeErr: errors.New("disk full")}
	clock := &fakeTimer{}
	s := newStore(fail, quietLogger(), DefaultDebounce, clock.after)

	s.SetWindow(Window{Width: 1000, Height: 700})
	clock.fire()
	if fail.writeCalls != 1 {
		t.Fatalf("write attempts = %d, want 1", fail.writeCalls)
	}
	if got := s.Window().Width; got != 1000 {
		t.Fatalf("a failed write reverted the in-memory value to %d", got)
	}

	fail.writeErr = nil
	s.SetWindow(Window{Width: 1100, Height: 700})
	clock.fire()
	if fail.writeCalls != 2 {
		t.Fatalf("write attempts = %d, want the next change to retry", fail.writeCalls)
	}
}

// ── The round trip a user actually performs ────────────────────────────

func TestGeometrySurvivesARestartOnTheSameDisplays(t *testing.T) {
	// Resize, move, quit, relaunch — through the real DocumentStore and a
	// real file, because "it round-trips" is the claim being made.
	dir := t.TempDir()
	displays := []Display{{Width: 2560, Height: 1440, Primary: true}}
	probe := &fakeProbe{
		window:   Window{Width: 1440, Height: 900, X: 120, Y: 64},
		displays: displays,
	}

	first := New(storage.NewDocumentStore(dir), quietLogger())
	first.Sample(probe)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := New(storage.NewDocumentStore(dir), quietLogger())
	defer func() { _ = second.Close() }()

	got := Restore(second.Window(), displays)
	if got.Width != 1440 || got.Height != 900 || got.X != 120 || got.Y != 64 || !got.UsePosition {
		t.Fatalf("after a restart the window is %+v, want the geometry it was left at", got)
	}
}

func TestGeometryFromAVanishedDisplayFallsBackToAVisibleWindow(t *testing.T) {
	// The same round trip, with the second monitor unplugged in between.
	dir := t.TempDir()
	twoDisplays := []Display{
		{Width: 1920, Height: 1080, Primary: true},
		{Width: 1920, Height: 1080},
	}
	probe := &fakeProbe{
		window:   Window{Width: 1400, Height: 900, X: 2400, Y: 30},
		displays: twoDisplays,
	}

	first := New(storage.NewDocumentStore(dir), quietLogger())
	first.Sample(probe)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := New(storage.NewDocumentStore(dir), quietLogger())
	defer func() { _ = second.Close() }()

	got := Restore(second.Window(), []Display{{Width: 1920, Height: 1080, Primary: true}})
	if got.UsePosition {
		t.Fatalf("restored the off-screen position %+v", got)
	}
	if got.Width > 1920 || got.Height > 1080 {
		t.Fatalf("size %dx%d does not fit the remaining display", got.Width, got.Height)
	}
}

func TestSampleDiscardsAnUnavailableReading(t *testing.T) {
	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)

	s.Sample(&fakeProbe{unavailable: true})
	clock.fire()

	if rec.writes() != 0 {
		t.Fatal("a probe that could not answer was recorded as zeros")
	}
}

func TestSetLayoutClampsTheWidthComingOffTheWire(t *testing.T) {
	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)

	s.SetLayout(Layout{Sidebar: Sidebar{Width: 999999}})
	clock.fire()

	if got := s.Layout().Sidebar.Width; got != MaxSidebarWidth {
		t.Fatalf("sidebar width = %d, want it clamped to %d", got, MaxSidebarWidth)
	}
	if got := rec.last().Sidebar.Width; got != MaxSidebarWidth {
		t.Fatalf("wrote sidebar width %d, want %d", got, MaxSidebarWidth)
	}
}

func TestTheDocumentIsWrittenAsWholePixels(t *testing.T) {
	// The whole of nocx-mqie.3's second symptom: 206.3828125 px on a Settings
	// page came from storing a getBoundingClientRect() result verbatim.
	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)

	s.SetLayout(Layout{Sidebar: Sidebar{Width: 206}})
	clock.fire()

	raw, err := json.Marshal(rec.last())
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Sidebar struct {
			Width json.Number `json:"width"`
		} `json:"sidebar"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Sidebar.Width.Int64(); err != nil {
		t.Fatalf("sidebar width %q is not a whole number", probe.Sidebar.Width)
	}
}

// ── Fakes ──────────────────────────────────────────────────────────────

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeTimer drives the debounce from the test rather than from the clock.
type fakeTimer struct {
	mu       sync.Mutex
	fn       func()
	resetted int
}

func (f *fakeTimer) after(_ time.Duration, fn func()) timerHandle {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fn = fn
	return f
}

func (f *fakeTimer) Stop() bool { return true }

func (f *fakeTimer) Reset(time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetted++
	return true
}

func (f *fakeTimer) resets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetted
}

// fire runs the pending callback, standing in for the debounce elapsing.
func (f *fakeTimer) fire() {
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
}

type recordingDocStore struct {
	mu   sync.Mutex
	docs []Document
}

func (r *recordingDocStore) Read(string, any) (bool, error) { return false, nil }
func (r *recordingDocStore) Delete(string) error            { return nil }

func (r *recordingDocStore) Write(_ string, doc any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := doc.(Document)
	if !ok {
		return errors.New("unexpected document type")
	}
	r.docs = append(r.docs, d)
	return nil
}

func (r *recordingDocStore) writes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.docs)
}

func (r *recordingDocStore) last() Document {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.docs) == 0 {
		return Document{}
	}
	return r.docs[len(r.docs)-1]
}

type failingDocStore struct {
	readErr    error
	writeErr   error
	writeCalls int
}

func (f *failingDocStore) Read(string, any) (bool, error) { return false, f.readErr }
func (f *failingDocStore) Delete(string) error            { return nil }
func (f *failingDocStore) Write(string, any) error {
	f.writeCalls++
	return f.writeErr
}

type fakeProbe struct {
	window      Window
	displays    []Display
	unavailable bool
}

func (p *fakeProbe) Geometry() (Window, []Display, bool) {
	if p.unavailable {
		return Window{}, nil, false
	}
	return p.window, p.displays, true
}

// ── Restoring against a window that does not exist yet ─────────────────

// THE DEFECT THIS ASSERTS AGAINST. The composition root used to probe the
// window once, and abandon window persistence for the whole session when that
// one probe could not answer:
//
//	_, displays, ok := probe.Geometry()
//	if !ok { return }        // <- the sampler below never started
//
// Under Wails v3 that probe CANNOT answer. Services start before the window is
// realised, `WebviewWindow.Size()` reports (0,0) until the platform window
// exists, and so the one moment the code asked was the one moment there was
// nothing to ask. Nothing was ever sampled, nothing was ever written, and every
// launch read back a document of zeros and opened at the default (nocx-39vhn).
//
// So the contract is stated the other way round: not being ready yet is an
// ordinary state that costs a tick, never the session.
func TestRestoreAndWatchWaitsForAWindowThatIsNotReadyYet(t *testing.T) {
	displays := []Display{{Width: 2560, Height: 1440, Primary: true}}
	saved := Window{Width: 1440, Height: 900, X: 120, Y: 64, Displays: Fingerprint(displays)}

	rec := &recordingDocStore{}
	clock := &fakeTimer{}
	s := newStore(rec, quietLogger(), DefaultDebounce, clock.after)
	s.SetWindow(saved)

	// Silent for the first three asks, exactly as an unrealised window is.
	probe := &lateProbe{
		silentFor: 3,
		window:    Window{Width: 1440, Height: 900, X: 120, Y: 64},
		displays:  displays,
	}
	placer := &fakePlacer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RestoreAndWatch(ctx, probe, placer, time.Millisecond)

	// Waited on the state changing, never on a duration: the tick is an
	// implementation detail and a test that slept for one would be a test about
	// this machine's speed.
	waitFor(t, "the window to be placed once it can answer", func() bool {
		return placer.calls() > 0
	})

	if got := placer.size(); got != [2]int{1440, 900} {
		t.Fatalf("placed at %v, want the saved 1440x900", got)
	}
	if got := placer.position(); got != [2]int{120, 64} {
		t.Fatalf("positioned at %v, want the saved (120,64)", got)
	}
	if placer.centred {
		t.Fatal("the displays match the saved fingerprint, so the position must be used, not centred")
	}
}

// AND IT GOES ON WATCHING. Placement is half the feature; a session that
// restores the window and then records nothing leaves the next launch reading
// the same stale document — which is the shape the defect actually took.
func TestRestoreAndWatchRecordsAfterAnUnreadyStart(t *testing.T) {
	displays := []Display{{Width: 2560, Height: 1440, Primary: true}}
	rec := &recordingDocStore{}
	s := newStore(rec, quietLogger(), time.Millisecond, realAfterFunc)

	probe := &lateProbe{
		silentFor: 2,
		window:    Window{Width: 1280, Height: 800, X: 40, Y: 20},
		displays:  displays,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RestoreAndWatch(ctx, probe, &fakePlacer{}, time.Millisecond)

	waitFor(t, "the moved window to reach the document", func() bool {
		w := s.Window()
		return w.Width == 1280 && w.Height == 800 && w.X == 40 && w.Y == 20
	})
	waitFor(t, "the document to be written", func() bool { return rec.writes() > 0 })
}

// A window that never becomes readable must not spin forever, and must let go
// at shutdown like every other background owner.
func TestRestoreAndWatchStopsWhenTheContextIsCancelled(t *testing.T) {
	s := newStore(&recordingDocStore{}, quietLogger(), DefaultDebounce, (&fakeTimer{}).after)
	probe := &lateProbe{silentFor: 1 << 30}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RestoreAndWatch(ctx, probe, &fakePlacer{}, time.Millisecond)
		close(done)
	}()

	waitFor(t, "the probe to be asked at least once", func() bool { return probe.asked() > 0 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RestoreAndWatch outlived its context")
	}
}

// waitFor blocks until cond holds, failing with what it was waiting for rather
// than with a bare timeout. Polling a condition is what keeps these tests
// independent of how fast the machine is (AGENTS.md: a test may not depend on
// timing).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// lateProbe answers `ok=false` for its first `silentFor` calls, which is what
// an unrealised Wails window does.
type lateProbe struct {
	mu        sync.Mutex
	calls     int
	silentFor int
	window    Window
	displays  []Display
}

func (p *lateProbe) Geometry() (Window, []Display, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls <= p.silentFor {
		return Window{}, nil, false
	}
	return p.window, p.displays, true
}

func (p *lateProbe) asked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type fakePlacer struct {
	mu         sync.Mutex
	n          int
	w, h       int
	x, y       int
	centred    bool
	maximised  bool
	fullscreen bool
}

func (f *fakePlacer) SetSize(w, h int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.w, f.h = w, h
}

func (f *fakePlacer) SetPosition(x, y int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.x, f.y = x, y
}

func (f *fakePlacer) Center() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.centred = true
}

func (f *fakePlacer) Maximise() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maximised = true
}

func (f *fakePlacer) Fullscreen() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fullscreen = true
}

func (f *fakePlacer) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakePlacer) size() [2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return [2]int{f.w, f.h}
}

func (f *fakePlacer) position() [2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return [2]int{f.x, f.y}
}
