package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/shady2k/nocx/internal/coordinator"
)

func TestResolveBackendReportsSuccessAndFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		w := &WailsApp{
			logger: slog.Default(),
			launchCoordinatorFn: func(context.Context, string) (coordinator.Launch, error) {
				return coordinator.Launch{Hello: coordinator.Hello{
					WSAddress: "127.0.0.1:4321",
					WSToken:   "capability-token",
				}}, nil
			},
		}

		got := w.ResolveBackend()
		want := BackendResolution{
			OK:    true,
			Host:  "127.0.0.1",
			Port:  4321,
			Token: "capability-token",
		}
		if got != want {
			t.Fatalf("ResolveBackend() = %#v, want %#v", got, want)
		}
	})

	t.Run("failure", func(t *testing.T) {
		cause := errors.New("server could not start")
		failure := coordinator.NewLaunchFailure(
			coordinator.FailureNotReady,
			"The backend is not ready.",
			"Retry the launch.",
			cause,
		)
		w := &WailsApp{
			logger: slog.Default(),
			launchCoordinatorFn: func(context.Context, string) (coordinator.Launch, error) {
				return coordinator.Launch{}, failure
			},
		}

		got := w.ResolveBackend()
		want := BackendResolution{
			Kind:    string(coordinator.FailureNotReady),
			Message: failure.Message,
			Remedy:  failure.Remedy,
		}
		if got != want {
			t.Fatalf("ResolveBackend() = %#v, want %#v", got, want)
		}
	})
}

func TestResolveBackendReturnsHeldStartupFailureBeforeRetry(t *testing.T) {
	failure := coordinator.NewLaunchFailure(
		coordinator.FailureProfileUnusable,
		"The profile directories could not be used.",
		"Check the profile directories, then retry.",
		nil,
	)
	attempts := 0
	w := &WailsApp{
		logger:        slog.Default(),
		launchFailure: failure,
		launchCoordinatorFn: func(context.Context, string) (coordinator.Launch, error) {
			attempts++
			return coordinator.Launch{Hello: coordinator.Hello{
				WSAddress: "127.0.0.1:4322",
				WSToken:   "new-token",
			}}, nil
		},
	}

	if got := w.ResolveBackend(); got.Kind != string(coordinator.FailureProfileUnusable) {
		t.Fatalf("first ResolveBackend() = %#v, want held failure", got)
	}
	if attempts != 0 {
		t.Fatalf("first ResolveBackend launched %d times, want 0", attempts)
	}
	if got := w.ResolveBackend(); !got.OK || got.Port != 4322 || got.Token != "new-token" {
		t.Fatalf("retry ResolveBackend() = %#v, want successful retry", got)
	}
	if attempts != 1 {
		t.Fatalf("retry launched %d times, want 1", attempts)
	}
}

// ── the quit chord and the quit item (nocx-1ebrs.13) ─────────────────────
//
// THE OWNER'S REGRESSION IS "Cmd+Q with the command editor focused quits". A
// Go test cannot press a key on a Mac, so it drives the callback AppKit itself
// invokes for that chord — the window's binding for it — and stops at the
// DECISION rather than at the process: [quitSeam.quit] is the seam, and a test
// records the decision instead of ending the run.
//
// What it does not simulate is focus, and that is the point rather than a gap:
// a window key equivalent is claimed before the responder chain runs, so the
// answer cannot depend on which surface holds the keyboard. A test that
// pretended to focus a DOM element here would assert a state this process has
// no access to and would pass through the defect the bead describes.

// quittingApp returns a seam whose decision a test can read, plus the number
// of times that decision was taken.
func quittingApp(t *testing.T) (*quitSeam, *int) {
	t.Helper()
	asked := 0
	return &quitSeam{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		quit:   func() { asked++ },
	}, &asked
}

func TestQuitChordQuitsTheApplication(t *testing.T) {
	quit, asked := quittingApp(t)

	bindings := quit.keyBindings("darwin")
	if bindings == nil {
		t.Fatal("darwin window claims no key bindings, so Cmd+Q reaches the webview")
	}
	// THE SPELLING IS PART OF THE TEST. "cmd+q" is what the window builds out
	// of the NSEvent it receives, and an entry under any other spelling is an
	// entry Wails will not match against that event.
	chord, ok := bindings["cmd+q"]
	if !ok {
		t.Fatalf("darwin window claims %v, want the quit chord under its event spelling \"cmd+q\"", bindingKeys(bindings))
	}

	chord(nil)

	if *asked != 1 {
		t.Fatalf("Cmd+Q took the quit decision %d times, want 1", *asked)
	}
}

func TestQuitChordStaysTheShellsKey(t *testing.T) {
	quit, asked := quittingApp(t)

	// Ctrl+Q is XON: a person sends it to the program in the pane to resume
	// output it paused. Claiming it on a platform whose menu bar does not
	// advertise the chord would take that key away from the terminal.
	if bindings := quit.keyBindings("linux"); bindings != nil {
		t.Fatalf("linux claims %v, want no claim on a key that belongs to the program", bindingKeys(bindings))
	}
	if *asked != 0 {
		t.Fatalf("no chord was pressed, but the quit decision was taken %d times", *asked)
	}
}

func TestQuitMenuItemIsOursAndCarriesTheChord(t *testing.T) {
	quit, _ := quittingApp(t)

	// A menu with no items is enough: the item is built here and added to the
	// application menu by [quitSeam.menu]. What this pins is that the item a
	// person reads in the menu bar is OUR item, labelled and accelerated by the
	// same constants the chord uses — the platform's role item carries AppKit's
	// own label and action, which is the item that worked while the chord did
	// not.
	item := quit.addQuitItem(application.NewMenu(), "nocx")

	if got, want := item.Label(), "Quit nocx"; got != want {
		t.Fatalf("menu item label = %q, want %q", got, want)
	}
	accelerator := item.GetAccelerator()
	if !strings.HasSuffix(accelerator, "+Q") {
		t.Fatalf("menu item accelerator = %q, want a command-or-control modifier and Q", accelerator)
	}
}

// TestQuitAsksNothingOfTheBackend is the stuck-session clause of the bead's
// DONE WHEN. A pane whose shell integration never reached its hello lives in
// nocx-server; this window holds no session state, and the only thing a quit
// runs in this process is [WailsApp.shutdown], which logs. So no session,
// stuck or otherwise, can hold a quit open — asserted by making the launcher
// fail the test if a quit touches it.
func TestQuitAsksNothingOfTheBackend(t *testing.T) {
	w := &WailsApp{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		launchCoordinatorFn: func(context.Context, string) (coordinator.Launch, error) {
			t.Error("the quit path asked the launcher for a backend")
			return coordinator.Launch{}, errors.New("unreachable")
		},
	}
	quit, asked := quittingApp(t)
	quit.quit = func() { w.shutdown(); *asked++ }

	quit.keyBindings("darwin")["cmd+q"](nil)

	if *asked != 1 {
		t.Fatalf("quit decision taken %d times, want 1", *asked)
	}
}

func bindingKeys(bindings map[string]func(application.Window)) []string {
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
