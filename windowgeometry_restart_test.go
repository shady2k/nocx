package main

// The owner's contract (nocx-ouru5.1.6): a window that was resized and moved
// opens at the same size and place on the next start.
//
// These tests drive the two REAL halves of that contract rather than the
// rules in isolation: the coordinator's uistate.Store over a real document
// on disk, reached through the capability service the wire handlers call,
// and the shell's windowGeometry with HostRestoreWindow — the binding the
// renderer calls. The renderer's relay stands where production puts it: a
// sample the shell emits is handed to the store, and the geometry the store
// answers is handed back to the binding.
//
// Only the Wails window itself is a fake. It cannot exist in a test, which
// is exactly why the probe and placer seams are interfaces.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/uistate"
	"github.com/shady2k/nocx/internal/waittest"
)

// coordinatorRelay is the renderer's two legs against one coordinator: the
// sample write and the saved-geometry read, through the same capability
// operation the WebSocket handlers use. Holding the operation (rather than
// the store) keeps the test on the seam the wire actually crosses.
type coordinatorRelay struct {
	op  capability.UIStateOperation
	ctx context.Context
}

func newCoordinatorRelay(store *uistate.Store) *coordinatorRelay {
	return &coordinatorRelay{
		op:  capability.NewUIStateOperation(capability.Gate(capability.GateConfig, 1, time.Second), store),
		ctx: context.Background(),
	}
}

// observe is called from the sampler's goroutine, so a failure is reported
// with Errorf rather than Fatalf: only the test's own goroutine may stop it.
func (r *coordinatorRelay) observe(t *testing.T, sample uistate.Window) {
	err := r.op.Run(r.ctx, func(ctx context.Context, svc capability.UIStateService) error {
		svc.ObserveWindow(ctx, sample)
		return nil
	})
	if err != nil {
		t.Errorf("relay sample: %v", err)
	}
}

func (r *coordinatorRelay) saved(t *testing.T) uistate.Window {
	t.Helper()
	var got uistate.Window
	err := r.op.Run(r.ctx, func(ctx context.Context, svc capability.UIStateService) error {
		got = svc.Window(ctx)
		return nil
	})
	if err != nil {
		t.Fatalf("read saved geometry: %v", err)
	}
	return got
}

// desktopWindow is one platform window: a probe the shell samples and a
// placer it applies to, sharing the state a real window would have. A
// placement therefore changes what the next sample reads, exactly as a
// resize does on screen.
type desktopWindow struct {
	mu       sync.Mutex
	window   uistate.Window
	displays []uistate.Display
}

func newDesktopWindow() *desktopWindow {
	return &desktopWindow{
		window:   uistate.Window{Width: 1024, Height: 768},
		displays: []uistate.Display{{Width: 2560, Height: 1440, Primary: true}},
	}
}

func (d *desktopWindow) Geometry() (uistate.Window, []uistate.Display, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.window, d.displays, true
}

func (d *desktopWindow) set(w uistate.Window) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window = w
}

func (d *desktopWindow) placed() uistate.Window {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.window
}

func (d *desktopWindow) SetSize(width, height int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window.Width, d.window.Height = width, height
}

func (d *desktopWindow) SetPosition(x, y int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window.X, d.window.Y = x, y
}

func (d *desktopWindow) Center() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window.X, d.window.Y = 0, 0
}

func (d *desktopWindow) Maximise() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window.Maximised = true
}

func (d *desktopWindow) Fullscreen() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window.FullScreen = true
}

// openShellWindow starts one window the way main.go plus the renderer's
// bootstrap do: the shell's sampler is created with the window, the renderer
// reads the saved geometry from the coordinator, and HostRestoreWindow
// applies it and starts sampling. The ticker runs on its own goroutine, as
// it does in the app; each wait is on the state the relay produces.
func openShellWindow(t *testing.T, relay *coordinatorRelay, env *desktopWindow) *windowGeometry {
	t.Helper()
	g := newWindowGeometry(env, env, func(sample uistate.Window) {
		// Exactly the renderer's leg: every sample the shell emits is one
		// uistate.window.observe notification.
		relay.observe(t, sample)
	}, time.Millisecond)

	t.Cleanup(func() { stopSampler(t, g) })

	app := &WailsApp{geometry: g}
	saved := relay.saved(t)
	if err := app.HostRestoreWindow(
		saved.Width, saved.Height, saved.X, saved.Y,
		saved.Maximised, saved.FullScreen, saved.Displays,
	); err != nil {
		t.Fatalf("HostRestoreWindow: %v", err)
	}
	return g
}

// resizeAndMove is the person's gesture; the wait is on the sample reaching
// the coordinator, which is the only proof that the sampler is running.
func resizeAndMove(t *testing.T, relay *coordinatorRelay, env *desktopWindow, want uistate.Window) {
	t.Helper()
	env.set(want)
	waittest.WaitFor(t, "the resize to reach the coordinator", func() bool {
		got := relay.saved(t)
		return got.Width == want.Width && got.Height == want.Height && got.X == want.X && got.Y == want.Y
	})
}

// stopSampler ends the shell's sampler and waits for its loop to let go,
// so nothing calls the test after it has finished.
func stopSampler(t *testing.T, g *windowGeometry) {
	t.Helper()
	g.stop()
	g.mu.Lock()
	done := g.done
	g.mu.Unlock()
	if done != nil {
		<-done
	}
}

func requireGeometry(t *testing.T, what string, got, want uistate.Window) {
	t.Helper()
	if got.Width != want.Width || got.Height != want.Height || got.X != want.X || got.Y != want.Y {
		t.Fatalf("%s = %dx%d at %d,%d; want %dx%d at %d,%d",
			what, got.Width, got.Height, got.X, got.Y, want.Width, want.Height, want.X, want.Y)
	}
}

// The three ways a window starts again. Each is a different process
// lifetime on the coordinator side, and the owner's report is about all of
// them: every start, not only the one after an update.
func TestWindowGeometryIsRememberedOnTheNextStart(t *testing.T) {
	want := uistate.Window{Width: 1512, Height: 944, X: 88, Y: 40}

	cases := []struct {
		name string
		// keepsCoordinator is true when the next window finds the same
		// coordinator still running (an adopted backend) instead of reading
		// the document back from disk.
		keepsCoordinator bool
	}{
		{name: "a normal quit", keepsCoordinator: false},
		{name: "the update's restart", keepsCoordinator: false},
		{name: "a start that adopts a running backend", keepsCoordinator: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := want
			dir := t.TempDir()
			store := uistate.New(storage.NewDocumentStore(dir), nil)
			t.Cleanup(func() { _ = store.Close() })

			// First window: nothing saved, so it opens at the default and
			// starts being recorded.
			relay := newCoordinatorRelay(store)
			first := newDesktopWindow()
			firstSampler := openShellWindow(t, relay, first)
			requireGeometry(t, "a first launch", first.placed(),
				uistate.Window{Width: uistate.DefaultWindowWidth, Height: uistate.DefaultWindowHeight})

			// The person resizes and moves. The shell samples it and the
			// renderer relays it to the coordinator.
			resizeAndMove(t, relay, first, expected)
			expected.Displays = uistate.Fingerprint(first.displays)
			requireGeometry(t, "the recorded geometry", relay.saved(t), expected)
			if got := relay.saved(t).Displays; got != expected.Displays {
				t.Fatalf("recorded displays = %q, want %q", got, expected.Displays)
			}

			// Quit.
			stopSampler(t, firstSampler)

			nextStore := store
			if !tc.keepsCoordinator {
				// The coordinator goes away with the old window (a normal
				// quit's grace period can reach this, and a version change
				// replaces it). The geometry has to be on disk for the next
				// coordinator, so the flush is part of the path.
				if err := store.Close(); err != nil {
					t.Fatalf("coordinator close: %v", err)
				}
				nextStore = uistate.New(storage.NewDocumentStore(dir), nil)
				t.Cleanup(func() { _ = nextStore.Close() })
			}

			// Second window: a fresh platform window at the default, the
			// same displays, and the saved geometry handed back.
			second := newDesktopWindow()
			openShellWindow(t, newCoordinatorRelay(nextStore), second)
			requireGeometry(t, "the next start", second.placed(), expected)
		})
	}
}
