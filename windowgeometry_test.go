package main

import (
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/uistate"
	"github.com/shady2k/nocx/internal/waittest"
)

// The shell's half of the window record (ADR-0098): apply the saved
// placement once, then sample the live window for the renderer to relay.
// Every rule here is stated against fakes — the Wails window itself cannot
// exist in a test, which is exactly why the probe, the placer and the
// emitter are seams.

type recordingEmitter struct {
	samples []uistate.Window
}

func (e *recordingEmitter) emit(w uistate.Window) { e.samples = append(e.samples, w) }

func newGeometryForTest(probe uistate.Probe, placer uistate.Placer, e *recordingEmitter) *windowGeometry {
	return newWindowGeometry(probe, placer, e.emit, time.Hour)
}

// A restore applies the saved placement exactly once: the renderer calls
// the binding again on every reload, and re-applying a stale read would
// move a window the person has since dragged.
func TestWindowGeometryRestoreAppliesOnce(t *testing.T) {
	displays := []uistate.Display{{Width: 2560, Height: 1440, Primary: true}}
	saved := uistate.Window{Width: 1440, Height: 900, X: 120, Y: 64, Displays: uistate.Fingerprint(displays)}
	probe := &fakeWindowProbe{window: saved, displays: displays}
	placer := &fakeWindowPlacer{}
	g := newGeometryForTest(probe, placer, &recordingEmitter{})

	g.restore(saved)
	if got := placer.size(); got != [2]int{1440, 900} {
		t.Fatalf("placed at %v, want the saved 1440x900", got)
	}
	if placer.centred {
		t.Fatal("the displays match the saved fingerprint, so the position must be used")
	}

	g.restore(uistate.Window{Width: 640, Height: 480})
	if got := placer.size(); got != [2]int{1440, 900} {
		t.Fatalf("second restore re-applied a placement: now %v", got)
	}
}

// A first launch has nothing saved. The sampler must still start — that
// is how the first geometry gets recorded — but nothing may be applied:
// centring or resizing a window the person is already looking at is not
// restoring anything.
func TestWindowGeometryRestoreWithNothingSavedStartsTheSamplerOnly(t *testing.T) {
	probe := &fakeWindowProbe{
		window:   uistate.Window{Width: 1024, Height: 768},
		displays: []uistate.Display{{Width: 2560, Height: 1440, Primary: true}},
	}
	placer := &fakeWindowPlacer{}
	e := &recordingEmitter{}
	g := newGeometryForTest(probe, placer, e)

	g.restore(uistate.Window{})
	if placer.calls() != 0 {
		t.Fatalf("applied %d placement calls with nothing saved", placer.calls())
	}

	g.tick()
	waittest.WaitFor(t, "the sampler to start with nothing saved", func() bool {
		return len(e.samples) == 1
	})
	if e.samples[0].Width != 1024 {
		t.Fatalf("first sample = %+v, want the window as it opened", e.samples[0])
	}
}

// A reading the platform could not give is discarded, never recorded as
// zeros and never emitted — the relay would otherwise teach the store a
// window with no size (nocx-39vhn's rule, moved to the shell that now
// holds the probe).
func TestWindowGeometryTickDiscardsUnavailableReadings(t *testing.T) {
	probe := &fakeWindowProbe{unavailable: true}
	e := &recordingEmitter{}
	g := newGeometryForTest(probe, &fakeWindowPlacer{}, e)

	for range 3 {
		g.tick()
	}
	if len(e.samples) != 0 {
		t.Fatalf("emitted %d samples from a probe that cannot answer", len(e.samples))
	}
}

// The wire carries changes, not ticks: a still window costs one emit and
// a drag costs one per distinct position, which is what keeps four
// samples a second from becoming four frames a second.
func TestWindowGeometryTickEmitsOnChangeOnly(t *testing.T) {
	probe := &fakeWindowProbe{window: uistate.Window{Width: 1000, Height: 700, X: 10, Y: 10}}
	e := &recordingEmitter{}
	g := newGeometryForTest(probe, &fakeWindowPlacer{}, e)

	g.tick()
	g.tick()
	if len(e.samples) != 1 {
		t.Fatalf("emitted %d samples for an unchanged window, want 1", len(e.samples))
	}

	probe.set(uistate.Window{Width: 1000, Height: 700, X: 40, Y: 10})
	g.tick()
	if len(e.samples) != 2 {
		t.Fatalf("emitted %d samples after a move, want 2", len(e.samples))
	}
}

// The sample carries the display fingerprint computed where the displays
// are, so the store's identity rule reads the same string it would have
// written before the cutover.
func TestWindowGeometryTickFingerprintsTheDisplays(t *testing.T) {
	displays := []uistate.Display{
		{Width: 1920, Height: 1080, Primary: true},
		{Width: 2560, Height: 1440},
	}
	probe := &fakeWindowProbe{window: uistate.Window{Width: 1000, Height: 700}, displays: displays}
	e := &recordingEmitter{}
	g := newGeometryForTest(probe, &fakeWindowPlacer{}, e)

	g.tick()
	if got := e.samples[0].Displays; got != uistate.Fingerprint(displays) {
		t.Fatalf("fingerprint = %q, want %q", got, uistate.Fingerprint(displays))
	}
}

// The loop must let go at shutdown like every other background owner.
func TestWindowGeometryStops(t *testing.T) {
	probe := &fakeWindowProbe{window: uistate.Window{Width: 1000, Height: 700}}
	g := newWindowGeometry(probe, &fakeWindowPlacer{}, func(uistate.Window) {}, time.Millisecond)
	g.start()
	waittest.WaitFor(t, "the loop to take its first tick", func() bool { return probe.asked() > 0 })
	g.stop()
	// Same package, so the done channel is read directly rather than through
	// a helper nothing in production would call.
	waittest.WaitFor(t, "the loop to let go", func() bool {
		g.mu.Lock()
		done := g.done
		g.mu.Unlock()
		if done == nil {
			return false
		}
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

// The binding refuses when the window is gone, the way every host
// binding in this file does, rather than acting on a nil window.
func TestHostRestoreWindowRefusesWithoutAGeometry(t *testing.T) {
	w := &WailsApp{}
	if err := w.HostRestoreWindow(0, 0, 0, 0, false, false, ""); err == nil {
		t.Fatal("HostRestoreWindow on a windowless app must refuse")
	}
}

// fakeWindowProbe is a uistate.Probe a test can steer. Mutex-guarded because
// the sampling loop reads it from its own goroutine.
type fakeWindowProbe struct {
	mu          sync.Mutex
	window      uistate.Window
	displays    []uistate.Display
	unavailable bool
	asks        int
}

func (p *fakeWindowProbe) Geometry() (uistate.Window, []uistate.Display, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asks++
	if p.unavailable {
		return uistate.Window{}, nil, false
	}
	return p.window, p.displays, true
}

func (p *fakeWindowProbe) set(w uistate.Window) {
	p.mu.Lock()
	p.window = w
	p.mu.Unlock()
}

func (p *fakeWindowProbe) asked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asks
}

// fakeWindowPlacer records what was applied.
type fakeWindowPlacer struct {
	n                                        int
	w, h, x, y                               int
	centred, maximised, fullscreen, position bool
}

func (f *fakeWindowPlacer) SetSize(width, height int) { f.n++; f.w, f.h = width, height }
func (f *fakeWindowPlacer) SetPosition(x, y int)      { f.n++; f.position = true; f.x, f.y = x, y }
func (f *fakeWindowPlacer) Center()                   { f.n++; f.centred = true }
func (f *fakeWindowPlacer) Maximise()                 { f.n++; f.maximised = true }
func (f *fakeWindowPlacer) Fullscreen()               { f.n++; f.fullscreen = true }

func (f *fakeWindowPlacer) size() [2]int { return [2]int{f.w, f.h} }

func (f *fakeWindowPlacer) calls() int { return f.n }
