package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/update"
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

// A notice is only a notice once somebody has seen it.
//
// The launcher announces what it did BEFORE it can show anything: a dialog
// needs a window, and at that moment in startup there is none (Wails creates
// the window after the services start). showNotices therefore has to survive
// having nowhere to put the notice — and it did not: it drained the recorder
// first and then returned early, so the one message an upgrade depends on was
// dropped on exactly the launch that has to deliver it (nocx-1ebrs.6).
func TestShowNoticesKeepsWhatItCannotShow(t *testing.T) {
	w := &WailsApp{logger: logtest.Slog(t)}
	w.notices.Announce(coordinator.Notice{
		Kind:    coordinator.NoticeSessionsLost,
		Message: "The nocx backend that was already running has been stopped and replaced.",
	})

	// No Wails application in this process: this is the startup case, and it
	// is also the only way to see the difference between "shown" and "thrown
	// away".
	w.showNotices()

	if held := w.notices.drain(); len(held) != 1 {
		t.Fatalf("after a show with no window, %d notice(s) are still waiting; want 1 — "+
			"a notice that is neither shown nor kept is a notice nobody will ever see", len(held))
	}
}

// recordingShell is a shellWindow that remembers what it was asked to do, in
// order. The order is the point: a dialog raised before the window is the
// defect, so "which call came first" is the assertion.
type recordingShell struct {
	calls   []string
	notices []coordinator.Notice
}

func (s *recordingShell) Show() { s.calls = append(s.calls, "show") }

func (s *recordingShell) Present(n coordinator.Notice) {
	s.calls = append(s.calls, "present")
	s.notices = append(s.notices, n)
}

// The bead's regression test, at the seam the launch resolves on.
//
// A launch that replaced an incompatible coordinator has to end with a window —
// that is what the person was left without, on the one launch every existing
// user takes — and the one sentence explaining what happened has to reach it,
// after the window and not before.
func TestAReplacementEndsWithAWindowAndThenTheNotice(t *testing.T) {
	shell := &recordingShell{}
	w := &WailsApp{
		logger: logtest.Slog(t),
		shell:  shell,
		launchCoordinatorFn: func(context.Context, string) (coordinator.Launch, error) {
			return coordinator.Launch{
				Hello:    coordinator.Hello{WSAddress: "127.0.0.1:4323", WSToken: "token"},
				Spawned:  true,
				Replaced: true,
			}, nil
		},
	}
	w.notices.Announce(coordinator.Notice{
		Kind:    coordinator.NoticeSessionsLost,
		Message: "The nocx backend that was already running has been stopped and replaced.",
	})

	if got := w.ResolveBackend(); !got.OK {
		t.Fatalf("ResolveBackend() = %#v, want a resolved backend after a replacement", got)
	}

	if len(shell.calls) != 2 || shell.calls[0] != "show" || shell.calls[1] != "present" {
		t.Fatalf("the shell was asked for %v; want [show present] — the window first, then "+
			"what the launcher did", shell.calls)
	}
	if len(shell.notices) != 1 || shell.notices[0].Kind != coordinator.NoticeSessionsLost {
		t.Fatalf("the shell was given %#v; want the sessions-lost notice", shell.notices)
	}
	if left := w.notices.drain(); len(left) != 0 {
		t.Fatalf("%d notice(s) still recorded after they were shown; want 0", len(left))
	}
}

// THE SHELL RAISES A WINDOW, IT DOES NOT BUILD ONE (ADR-0095).
//
// v0.6.1's Linux AppImage crashed on every launch, before WebKit's helper
// processes were spawned, with a Go register dump and `signal arrived during
// cgo execution` inside gtk_application_window_new. The path was the one this
// test stands on: afterLaunch asked the shell to raise the window during
// startup, and `WebviewWindow.Show` — which shows only what exists — CREATES
// the platform window when there is none, on the GTK3 main thread, before
// `g_application_run` has registered the application.
//
// So the rule is stated where it can be checked: a window the platform has not
// built is not raised, and one it has, is.
func TestTheShellRaisesOnlyAWindowThePlatformHasBuilt(t *testing.T) {
	t.Run("not built yet: nothing is created", func(t *testing.T) {
		window := &raisableDouble{built: false}
		shell := &wailsShell{window: window, logger: logtest.Slog(t)}

		shell.Show()

		if window.shows != 0 {
			t.Fatalf("the shell asked Wails for a window %d time(s) before the platform built one; "+
				"want 0 — Wails' Show CREATES the window at that moment, and on GTK3 that is "+
				"gtk_application_window_new before g_application_run, which segfaults", window.shows)
		}
	})

	t.Run("built and on screen: raised", func(t *testing.T) {
		window := &raisableDouble{built: true}
		shell := &wailsShell{window: window, logger: logtest.Slog(t)}

		shell.Show()

		if window.shows != 1 {
			t.Fatalf("the shell raised the window %d time(s); want 1 — a launch that has a window "+
				"on screen must end with it in front", window.shows)
		}
	})
}

// raisableDouble is the window Wails owns, reduced to the two questions
// [wailsShell.Show] asks it.
type raisableDouble struct {
	built bool
	shows int
}

func (w *raisableDouble) IsVisible() bool { return w.built }

func (w *raisableDouble) Show() application.Window {
	w.shows++
	return nil
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

// ---------------------------------------------------------------------------
// CheckForUpdate — the seam the renderer's startup check goes through
// (nocx-1ebrs.36)
// ---------------------------------------------------------------------------

// signedUpdateServer serves a signed manifest whose only artefact matches the
// platform this test runs on, so the check path is exercised the way the
// shipped app exercises it on every host.
func signedUpdateServer(t *testing.T, version string) (*httptest.Server, []ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := update.NewPlatform().ArtifactID()
	m := update.Manifest{
		Version:  version,
		Released: "2026-10-21T00:00:00Z",
		NotesURL: "https://example.com/releases/v" + version,
		Artifacts: []update.Artifact{{
			OS:     id.OS,
			Arch:   id.Arch,
			Format: id.Format,
			URL:    "https://example.com/nocx-" + version,
			SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			Size:   1,
		}},
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(body)
		case "/manifest.json.sig":
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, []ed25519.PublicKey{pub}
}

// updaterBuiltAsMainBuildsIt wires the updater exactly as ServiceStartup does
// — GitHubManifestFetcher over a nil client — pointed at the test server.
func updaterBuiltAsMainBuildsIt(t *testing.T, srv *httptest.Server, keyring []ed25519.PublicKey) update.Updater {
	t.Helper()
	fetcher := update.NewGitHubManifestFetcher(nil)
	fetcher.BaseURL = srv.URL
	return update.NewUpdater(update.UpdaterConfig{
		Platform:       update.NewPlatform(),
		Fetcher:        fetcher,
		Keyring:        keyring,
		CurrentVersion: "0.0.1",
		InstallPath:    filepath.Join(t.TempDir(), "nocx"),
		Logger:         log.NewSlogAdapter(logtest.Slog(t)),
	})
}

// An update newer than the running version must be REPORTED through the seam
// the renderer calls at startup, not swallowed. On 0.6.0 this seam panicked
// inside the fetcher on every call (nocx-1ebrs.36). Red first.
func TestCheckForUpdateReportsANewerVersionThroughTheAppSeam(t *testing.T) {
	srv, keyring := signedUpdateServer(t, "9.9.9")
	w := &WailsApp{
		updater: updaterBuiltAsMainBuildsIt(t, srv, keyring),
		logger:  logtest.Slog(t),
	}

	info, err := w.CheckForUpdate()
	if err != nil {
		t.Fatalf("check through the app seam: %v", err)
	}
	if info == nil {
		t.Fatal("an update newer than the running version was swallowed — the seam returned null")
	}
	if info.Version != "9.9.9" {
		t.Errorf("reported version = %q, want 9.9.9", info.Version)
	}
}

// The paired failure path: a server that errors must come back out of the
// seam as an error the renderer can surface, not as a silent null.
func TestCheckForUpdateReportsAFailedCheckThroughTheAppSeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	w := &WailsApp{
		updater: updaterBuiltAsMainBuildsIt(t, srv, []ed25519.PublicKey{testPublicKey(t)}),
		logger:  logtest.Slog(t),
	}

	info, err := w.CheckForUpdate()
	if err == nil {
		t.Fatal("a failed check must be reported as an error, not swallowed")
	}
	if !strings.Contains(err.Error(), "update check failed") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if info != nil {
		t.Errorf("a failed check must not report an update, got %+v", info)
	}
}

// testPublicKey mints a throwaway key so a failing check can still get past
// keyring construction — the failure happens in the fetch, before any key is
// consulted.
func testPublicKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// ---------------------------------------------------------------------------
// SetUpdateChannel — the window binding that makes the stored setting select
// the manifest the next check fetches (nocx-1ebrs.35.1, ADR-0097)
// ---------------------------------------------------------------------------

// signedChannelServer serves TWO signed manifests, one per channel root, with
// different versions, and records which root each request hit — so a test can
// prove the SELECTED channel was fetched, not just that a fetch happened.
func signedChannelServer(t *testing.T, stable, nightly string) (*httptest.Server, []ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := update.NewPlatform().ArtifactID()
	manifest := func(version string) []byte {
		m := update.Manifest{
			Version:  version,
			Released: "2026-10-21T00:00:00Z",
			NotesURL: "https://example.com/releases/v" + version,
			Artifacts: []update.Artifact{{
				OS:     id.OS,
				Arch:   id.Arch,
				Format: id.Format,
				URL:    "https://example.com/nocx-" + version,
				SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
				Size:   1,
			}},
		}
		body, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	bodies := map[string][]byte{"/stable": manifest(stable), "/nightly": manifest(nightly)}
	sigs := make(map[string][]byte, len(bodies))
	for root, body := range bodies {
		sigs[root] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)))
	}
	var mu sync.Mutex
	fetched := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		root := path.Dir(r.URL.Path)
		body, ok := bodies[root]
		if !ok {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		fetched[root]++
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".sig") {
			_, _ = w.Write(sigs[root])
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, []ed25519.PublicKey{pub}
}

// channelUpdaterAsMainBuildsIt wires the updater the way ServiceStartup does,
// both channel roots pointed at the test server, and hands back the fetcher
// the channel binding flips.
func channelUpdaterAsMainBuildsIt(t *testing.T, srv *httptest.Server, keyring []ed25519.PublicKey) (update.Updater, *update.GitHubManifestFetcher) {
	t.Helper()
	fetcher := update.NewGitHubManifestFetcher(nil)
	fetcher.BaseURL = srv.URL + "/stable"
	fetcher.NightlyBaseURL = srv.URL + "/nightly"
	return update.NewUpdater(update.UpdaterConfig{
		Platform:       update.NewPlatform(),
		Fetcher:        fetcher,
		Keyring:        keyring,
		CurrentVersion: "0.0.1",
		InstallPath:    filepath.Join(t.TempDir(), "nocx"),
		Logger:         log.NewSlogAdapter(logtest.Slog(t)),
	}), fetcher
}

// The criterion the bead names: a STORED setting that does not select the
// corresponding channel disproves the feature. Here the store is represented
// by the binding the renderer calls with the setting's value, and the proof
// is which channel root the next check actually fetched.
func TestSetUpdateChannelSelectsTheManifestTheNextCheckFetches(t *testing.T) {
	srv, keyring := signedChannelServer(t, "1.1.1", "2.2.2-nightly")
	updater, fetcher := channelUpdaterAsMainBuildsIt(t, srv, keyring)
	w := &WailsApp{updater: updater, updateFetch: fetcher, logger: logtest.Slog(t)}

	// Default: stable, before anybody opts in.
	info, err := w.CheckForUpdate()
	if err != nil {
		t.Fatalf("stable check: %v", err)
	}
	if info == nil || info.Version != "1.1.1" {
		t.Fatalf("default channel fetch = %+v, want the stable 1.1.1", info)
	}

	err = w.SetUpdateChannel("nightly")
	if err != nil {
		t.Fatalf("select nightly: %v", err)
	}
	info, err = w.CheckForUpdate()
	if err != nil {
		t.Fatalf("nightly check: %v", err)
	}
	if info == nil || info.Version != "2.2.2-nightly" {
		t.Fatalf("nightly channel fetch = %+v, want 2.2.2-nightly — the stored setting did not select the channel", info)
	}

	// Back to stable, by the same road the renderer takes.
	err = w.SetUpdateChannel("stable")
	if err != nil {
		t.Fatalf("select stable: %v", err)
	}
	info, err = w.CheckForUpdate()
	if err != nil {
		t.Fatalf("second stable check: %v", err)
	}
	if info == nil || info.Version != "1.1.1" {
		t.Fatalf("stable channel fetch = %+v, want 1.1.1", info)
	}
}

// An unknown channel is refused and changes nothing: the next check keeps
// the channel it had, rather than guessing what "beta" might mean.
func TestSetUpdateChannelRefusesAnUnknownChannel(t *testing.T) {
	srv, keyring := signedChannelServer(t, "1.1.1", "2.2.2-nightly")
	updater, fetcher := channelUpdaterAsMainBuildsIt(t, srv, keyring)
	w := &WailsApp{updater: updater, updateFetch: fetcher, logger: logtest.Slog(t)}

	if err := w.SetUpdateChannel("beta"); err == nil {
		t.Fatal("an unknown channel must be refused, not silently mapped")
	}
	info, err := w.CheckForUpdate()
	if err != nil {
		t.Fatalf("check after a refused channel: %v", err)
	}
	if info == nil || info.Version != "1.1.1" {
		t.Fatalf("fetch after a refused channel = %+v, want the unchanged stable 1.1.1", info)
	}
}

// The settings declaration and the window binding are two surfaces over one
// vocabulary (ADR-0097): every value the Select offers must be a channel the
// binding accepts, or a person could pick a setting the updater refuses.
func TestUpdateChannelSettingValuesAreChannelsTheWindowAccepts(t *testing.T) {
	for _, o := range settings.UpdateChannel.Options() {
		if _, err := update.ParseChannel(o.Value); err != nil {
			t.Errorf("update.channel offers %q, but the window refuses it: %v", o.Value, err)
		}
	}
	// The default is one of the options (asserted in internal/settings),
	// so the loop above already covers it.
}
