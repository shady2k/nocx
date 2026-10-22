package main

import (
	"context"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/shady2k/nocx/internal/uistate"
)

// windowSampledEvent is the Wails event this window emits whenever its
// geometry CHANGES. The renderer relays each one to the coordinator as a
// uistate.window.observe notification (ADR-0098) — the same one-way street
// as attentionActivatedEvent, and for the same reason: the WebSocket
// belongs to the renderer, and this loop runs in the shell.
const windowSampledEvent = "nocx:windowSampled"

// windowGeometry is the shell's half of the UI-state document's window
// record (ADR-0098). The document lives in the coordinator and the window
// lives here, and neither can reach the other directly, so this type owns
// the two things only a window can do: apply the saved placement once,
// and report where the window actually is.
//
// IT DECIDES NOTHING. What a saved geometry MEANS — the clamp, whether
// the position can be trusted on these displays, which state to re-enter
// — is uistate.Restore's, and the maximised fold on the way back is the
// store's. This type reads a probe, applies a placement, and tells the
// renderer what it saw.
//
// The sampler starts only when a restore has been applied (or explicitly
// skipped because nothing was saved), never before: a sample taken first
// would record where the platform happened to open the window and
// overwrite the very geometry being restored. That ordering was
// load-bearing when the loop lived in the store (nocx-39vhn) and it is
// load-bearing here.
type windowGeometry struct {
	probe    uistate.Probe
	place    uistate.Placer
	emit     func(uistate.Window)
	interval time.Duration

	mu      sync.Mutex
	applied bool
	started bool
	last    uistate.Window
	cancel  context.CancelFunc
	done    chan struct{}
}

// newWindowGeometry wires the shell's window half. emit receives only
// changes; production sends them to the renderer as a Wails event.
func newWindowGeometry(probe uistate.Probe, place uistate.Placer, emit func(uistate.Window), interval time.Duration) *windowGeometry {
	if interval <= 0 {
		interval = uistate.DefaultSampleInterval
	}
	return &windowGeometry{probe: probe, place: place, emit: emit, interval: interval}
}

// restore applies the saved placement and starts the sampler. A second
// call does neither: the renderer calls the binding on every reload, and
// re-applying a stale read would move a window the person has since
// dragged. A saved geometry with no size at all skips the placement —
// there is nothing to restore — and still starts the sampler, which is
// how a first launch begins to be recorded.
func (g *windowGeometry) restore(saved uistate.Window) {
	g.mu.Lock()
	if g.applied {
		g.mu.Unlock()
		return
	}
	g.applied = true
	g.mu.Unlock()

	if saved.Width > 0 || saved.Height > 0 {
		// The displays come from the probe: the fingerprint rule needs
		// what is attached NOW, and only the shell can ask. A probe that
		// cannot answer yields no displays, which Restore reads as
		// "position cannot be trusted" — size still applies, the window
		// is centred — and that is the honest degrade.
		_, displays, _ := g.probe.Geometry()
		uistate.Apply(g.place, uistate.Restore(saved, displays))
	}
	g.start()
}

// start runs the sampling loop until stop. Safe to call twice; the
// second call is the no-op that keeps restore's contract simple.
func (g *windowGeometry) start() {
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return
	}
	g.started = true
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	done := make(chan struct{})
	g.done = done
	g.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(g.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.tick()
			}
		}
	}()
}

// stop ends the sampling loop. The final state is whatever the last emit
// carried: the store's own Close flushes its debounce, so a quit inside
// the window loses nothing that has already been relayed.
func (g *windowGeometry) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
}

// tick takes one reading and emits it if it differs from the last. A
// reading the platform could not give is discarded rather than recorded
// as zeros — the rule the store's own sampler carried before the cutover
// (nocx-39vhn), moved to the one process that still holds a probe.
func (g *windowGeometry) tick() {
	live, displays, ok := g.probe.Geometry()
	if !ok {
		return
	}
	live.Displays = uistate.Fingerprint(displays)

	g.mu.Lock()
	same := live == g.last
	g.last = live
	g.mu.Unlock()
	if same {
		return
	}
	g.emit(live)
}

// wailsWindowProbe reads the live window and the attached displays
// through the Wails v3 API. It is the only implementation of
// uistate.Probe and it lives here because this is the only place a
// window exists — keeping the interface on the other side is what lets
// every rule above it be tested without a display.
type wailsWindowProbe struct {
	window  *application.WebviewWindow
	screens func() []*application.Screen
}

func (p wailsWindowProbe) Geometry() (uistate.Window, []uistate.Display, bool) {
	if p.window == nil {
		return uistate.Window{}, nil, false
	}
	width, height := p.window.Size()
	if width <= 0 || height <= 0 {
		// The window is not laid out yet (or is minimised). A zero is
		// not a geometry, and recording it would restore a window with
		// no size.
		return uistate.Window{}, nil, false
	}
	x, y := p.window.Position()

	var displays []uistate.Display
	if p.screens != nil {
		// An empty list is reported as no displays, which uistate reads
		// as "unknown" and treats as a mismatch: when we cannot tell, we
		// open somewhere visible.
		for _, s := range p.screens() {
			if s == nil {
				continue
			}
			displays = append(displays, uistate.Display{
				Primary: s.IsPrimary,
				Width:   s.Size.Width,
				Height:  s.Size.Height,
			})
		}
	}

	return uistate.Window{
		Width:      width,
		Height:     height,
		X:          x,
		Y:          y,
		Maximised:  p.window.IsMaximised(),
		FullScreen: p.window.IsFullscreen(),
	}, displays, true
}

// wailsWindowPlacer applies a placement through the Wails v3 API. Its own
// type rather than more methods on the probe: reading where the window
// is and putting it somewhere are two jobs, and uistate asks for them
// through two interfaces.
type wailsWindowPlacer struct {
	window *application.WebviewWindow
}

func (p wailsWindowPlacer) SetSize(width, height int) { p.window.SetSize(width, height) }
func (p wailsWindowPlacer) SetPosition(x, y int)      { p.window.SetPosition(x, y) }
func (p wailsWindowPlacer) Center()                   { p.window.Center() }
func (p wailsWindowPlacer) Maximise()                 { p.window.Maximise() }
func (p wailsWindowPlacer) Fullscreen()               { p.window.Fullscreen() }
