package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/shady2k/nocx/internal/app"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/notify/wailsadapter"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/uistate"
	"github.com/shady2k/nocx/internal/update"
	"github.com/shady2k/nocx/internal/version"
)

//go:embed all:frontend/dist
var assets embed.FS

// mainWindowName is the shell's name for the one window nocx opens. It is
// how anything outside main() reaches that window back through the v3 window
// manager — the notification click path below is the first such caller.
const mainWindowName = "main"

// errFocusSessionUnrouted is the half of a notification click the backend
// cannot yet deliver. The click reaches the shell, which raises the window;
// activating the tab that holds the named session is the renderer's job and
// the backend has no channel to ask for it (nocx-jiwq.1). Reported as an
// error for the same reason ErrBadgeBounce is: an absence that says so beats
// a call that returns nil and delivers half of what it promised.
var errFocusSessionUnrouted = errors.New("notification click raised the window, but the tab holding the session was not activated: the backend has no control-plane channel to ask the renderer (nocx-jiwq.1)")

func main() {
	// Checked before any backend or window exists so CI's release smoke check
	// (distribution design §5) and a user's `nocx --version` print the linked
	// build metadata and exit, never opening a terminal.
	if versionRequested() {
		fmt.Printf("nocx %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return
	}

	backend, err := app.New()
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}

	wailsApp := &WailsApp{backend: backend}

	// The Wails v3 shell. The window is created before Run; on Linux the
	// platform defers actually loading the webview until activation inside
	// Run, which happens after ServiceStartup — so the frontend's first
	// binding calls (GetWSPort/GetWSToken) resolve against a started backend,
	// preserving the v2 OnStartup ordering this composition root relied on.
	shell := application.New(application.Options{
		Name:        "nocx",
		Description: "A local-first, Warp-style terminal",
		Assets: application.AssetOptions{
			// Bundled, not plain: it serves /wails/runtime.js, which the v3
			// frontend runtime's HTTP transport fetches for every call.
			Handler: application.BundledAssetFileServer(assets),
		},
		Services: []application.Service{
			application.NewService(wailsApp),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		OnShutdown: wailsApp.shutdown,
	})

	// The tab strip IS the title bar, Tabby-style: no title text and no
	// second row stealing ~28px of terminal. TitleBarHiddenInset keeps the
	// traffic lights and insets them, so the strip needs left padding to
	// clear them and a drag region on its empty part — see .tabbar in
	// frontend/src/style.css, which is the other half of this decision.
	// TitleBarHidden, not TitleBarHiddenInset: the two differ only by
	// UseToolbar, and that NSToolbar left the window unrestorable after
	// minimising (nocx-dqg; cf. wailsapp/wails#1319). We keep the hidden
	// title and full-size content, and lose only the extra inset of the
	// traffic lights.
	// THE SIZE THE WINDOW OPENS AT, from the UI-state document (ADR-0033).
	// Wails wants it before there is a window, and therefore before the screen
	// manager can say what is attached — so this pass takes the saved size and
	// the minimum, and `restoreWindow` does the display-aware half once the
	// window exists: the position, maximised and full-screen. Two passes
	// rather than one is what makes the FIRST frame the right size instead of
	// 1024x768 followed by a jump.
	opening := uistate.Restore(backend.UIState.Window(), nil)

	window := shell.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       mainWindowName,
		Title:                      "nocx",
		Width:                      opening.Width,
		Height:                     opening.Height,
		MinWidth:                   uistate.MinWindowWidth,
		MinHeight:                  uistate.MinWindowHeight,
		DefaultContextMenuDisabled: true,
		// FILES DROPPED ON THE WINDOW REACH GO, NOT THE RENDERER. In the
		// desktop shell a drop delivers absolute paths on this machine, and
		// R2 says the renderer may never learn one — so Go takes the drop,
		// mints a source ticket per file and tells the renderer only a name
		// and a size (handleFilesDropped, below).
		//
		// The tab strip is unaffected, and that is checked rather than
		// hoped: v3's runtime installs document-level listeners that return
		// immediately unless the drag's `types` contain `Files`
		// (window.ts:712 in v3.0.0-beta.9), while a tab row's drag carries
		// application/x-nocx-tab (frontend/src/layout/strip-drag.ts). The
		// regression test is frontend/src/tab.test.tsx — "a tab drag is not
		// a files drag", written so a future runtime bump that widened that
		// check cannot break reordering silently.
		EnableFileDrop: true,
		// DevTools/Inspector, opened on startup when NOCX_DEVTOOLS=1.
		//
		// There is no other way into a console here, and that is deliberate on
		// both sides: DefaultContextMenuDisabled is true above, and the terminal
		// surface preventDefaults `contextmenu` to paste — so WebKit's "Inspect
		// Element" is gone and cannot come back. An env flag rather than an
		// edit-and-rebuild, because the thing you want to inspect is usually a
		// state you already have on screen.
		//
		// Wails v3 opens the inspector only when devtools are enabled, which
		// defaults to true in non-production builds, so this cannot open an
		// inspector in the shipped app whatever the environment says.
		OpenInspectorOnStartup: os.Getenv("NOCX_DEVTOOLS") == "1",
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHidden,
		},
	})
	window.Show()
	wailsApp.window = window

	// The window drop is the second of the upload feature's two gestures
	// (the first is the native picker, behind dialog.openFileForUpload).
	// Registered here because this is where the window exists; the handler
	// itself resolves no destination — see handleFilesDropped.
	window.OnWindowEvent(events.Common.WindowFilesDropped, wailsApp.handleFilesDropped)
	wailsApp.screens = shell.Screen.GetAll

	if err := shell.Run(); err != nil {
		slog.Error("application error", "error", err)
		os.Exit(1)
	}
}

// WailsApp is the bound service (v3) that was the bound struct (v2): the
// frontend reaches the backend through these methods over the Wails runtime.
type WailsApp struct {
	backend *app.App
	ctx     context.Context

	// notifications is the v3 notifications service, started by hand in
	// ServiceStartup (it is not registered as a Wails service, because a
	// service whose startup fails aborts app.Run) and shut down in
	// ServiceShutdown.
	notifications *notifications.NotificationService

	// updateInfo holds the most recent Check result. Apply takes no
	// arguments — it applies the update that Check already verified.
	updateInfo *update.UpdateInfo

	// stopGeometry cancels the window-geometry sampler at shutdown, so no
	// poll outlives the process.
	stopGeometry context.CancelFunc

	// window is the shell's one window, handed over after it is created so
	// the geometry restore has something to place. v3 gives a window object
	// where v2 gave a context, which is why this is a field rather than a
	// runtime call.
	window *application.WebviewWindow

	// screens reads the attached displays. A function rather than the manager
	// itself, so the probe stays testable and this file keeps the only
	// dependency on the shell.
	screens func() []*application.Screen
}

// Log logs a message from the frontend.
func (w *WailsApp) Log(message string) {
	w.backend.Log(message)
}

// LogFilePath reports where the backend log file lives, so a running
// desktop session can say where its log is instead of it being guessed
// from a file's mtime. "" means file logging is unavailable (stderr only).
func (w *WailsApp) LogFilePath() string {
	return w.backend.LogFilePath()
}

// ServiceStartup is the v3 lifecycle hook that replaced the v2 OnStartup
// callback: it runs once, during app.Run, before the webview loads the
// frontend (see main). All composition-root wiring lives here for the same
// reason it lived in v2's OnStartup: this is the only place the Wails
// context and shell exist to back the OS conveniences.
//
//wails:ignore
func (w *WailsApp) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	w.ctx = ctx
	w.backend.Logger.Info("Wails app starting up")

	// Derive the install path from the running executable.
	// On macOS this points into the .app bundle; on Linux it's the
	// AppImage path. The Platform seam handles the OS differences.
	execPath, err := os.Executable()
	if err != nil {
		w.backend.Logger.Warn("cannot determine executable path", "error", err)
	}
	installPath := upgradeInstallPath(execPath)

	// The trust root for every update this build will ever accept. It is
	// compiled in (internal/update/keyring.go); the field used to be a literal
	// nil with a comment claiming the release pipeline filled it via ldflags,
	// which nothing did and nothing could, so every production update check
	// failed on an empty keyring before it compared versions (nocx-nfu5.1).
	//
	// A keyring that will not decode costs the user their update check and
	// nothing else: the app starts, and VerifyManifest then refuses every
	// manifest, which is the direction to fail in.
	keyring, err := update.ReleaseKeyring()
	if err != nil {
		w.backend.Logger.Error("release keyring unusable; updates cannot be verified on this build", "error", err)
	}

	// Wire the updater with the real install path and platform.
	w.backend.Updater = update.NewUpdater(update.UpdaterConfig{
		Platform:       update.NewPlatform(),
		Fetcher:        update.NewGitHubManifestFetcher(nil),
		Keyring:        keyring,
		CurrentVersion: version.Version,
		InstallPath:    installPath,
		Logger:         w.backend.Logger,
	})

	// The native file and directory dialogs are one control-plane
	// capability (AD-1): the renderer reaches the Wails runtime through
	// dialog.openFile and dialog.openDirectory on the WebSocket, and this
	// is the only place the shell exists to back them.
	// Wired before Start so no renderer request can observe the unset
	// state. The dev-web harness never runs this — the method then reports
	// itself unavailable and the surfaces fall back to typing paths.
	w.backend.SetDialogService(&wailsDialogService{
		app: application.Get(),
		// The upload half of the picker mints into the backend's own store,
		// which is the SAME store the window drop mints into and the same
		// one a later files.upload claims from. One mint, one owner.
		sources: w.backend.UploadSources,
	})

	// The native browser-open is the same control-plane shape as the file
	// dialog: the renderer reaches the Wails runtime through shell.openUrl
	// on the WebSocket, and this is the only place the shell exists to back
	// it. Wired before Start; the dev-web harness never runs this, and the
	// method then reports itself unavailable and the panel toasts.
	w.backend.SetUrlOpener(&wailsUrlOpener{app: application.Get()})

	// The desktop attention surface, behind the notify router's banner route
	// (ADR-0029). Same shape as the two above and for the same reason: the
	// v3 notifications service locates its D-Bus connection here. Wired
	// before Start, so no raise can observe the unset state; the dev-web
	// harness and cmd/devharness never run this, and their raises stay
	// visible failed deliveries.
	//
	// The notifications service is started by hand rather than registered as
	// a Wails service: a registered service whose ServiceStartup fails aborts
	// app.Run (v3 services.go), and on Linux the service's startup connects
	// the session D-Bus, which a bus-less host lacks. v2 failed per call;
	// this keeps that contract — the app starts, and banners on a bus-less
	// host fail loudly per raise. ServiceShutdown is called by the framework
	// because WailsApp is itself a registered service.
	ns := notifications.New()
	w.notifications = ns
	notificationsUp := true
	if err := ns.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		notificationsUp = false
		w.backend.Logger.Warn("notification service unavailable; banners will fail per raise", "error", err)
	}
	//
	// Focus is the composition root's half of a banner click: the adapter
	// decodes the sessionId the banner carried and hands it here. Raising
	// the window is the part the shell owns and can do now; activating the
	// tab that holds that session is the renderer's, and the backend has no
	// channel to ask for it yet (nocx-jiwq.1 — a control-plane notification
	// naming the session, which the renderer resolves to a tab, AD-1 and
	// AD-7). Until that lands the click lands on the window and the unrouted
	// session is named in the log, rather than every click being discarded
	// because no seam could be supplied at all. No surface offers
	// click-to-focus until the renderer half exists.
	host := wailsadapter.New(wailsadapter.Deps{
		Service: ns,
		Log:     w.backend.Slog(),
		// THE ROOT IS THE CALLER WITH REAL KNOWLEDGE, and the adapter's
		// default says so: v3.0.0-beta.9 exposes no availability probe, so
		// the default assumes a surface exists and lets every failure land at
		// send time — "PermissionUnavailable stays reachable through this
		// seam where a caller has real knowledge" (host.go). This is that
		// caller. It has just watched ServiceStartup fail and has already
		// written the reason into the log; passing that verdict on is what
		// was missing.
		//
		// Without it the host was born NotDetermined over a service that had
		// not started, and the startup resolve then called into it. On macOS
		// that reaches +[UNUserNotificationCenter currentNotificationCenter],
		// which throws NSInternalInconsistencyException when the process has
		// no bundle — an Objective-C exception, so not an error Go can
		// recover: the whole application aborted. Any unbundled run took it,
		// which is every `make dev` on a Mac, one line after the log had
		// stated the service was unavailable. The same call underlies a
		// raise, so "banners fail loudly per raise" was in fact "the process
		// dies per raise" on that build.
		IsAvailable: func() bool { return notificationsUp },
		Focus: func(sessionID string) error {
			win, ok := application.Get().Window.GetByName(mainWindowName)
			if !ok {
				return fmt.Errorf("notification click: window %q is gone", mainWindowName)
			}
			win.Focus()
			return fmt.Errorf("%w (sessionId %s)", errFocusSessionUnrouted, sessionID)
		},
	})
	w.backend.SetAttentionHost(host)

	// Resolve the OS's authorization state, and ask for it once when it has
	// never been asked. Without this the host stays PermissionNotDetermined
	// for the life of the process and every banner is refused with
	// ErrNotRequested — including on a machine that has already authorized
	// nocx. Nothing else calls these: spec §6.4 gives the job to a settings
	// control, no surface has ever grown one, and so the composition root is
	// where it has to happen or the whole banner route is unreachable.
	//
	// Off the startup path deliberately. On macOS the check waits on the OS
	// for up to 15s and the request for as long as the user takes to answer
	// (the runtime caps it at 180s), and ServiceStartup runs before the
	// webview loads — inline, either one would hold the window shut.
	go w.resolveNotificationPermission(host)

	// Settle any transaction in flight from a previous launch.
	if err := w.backend.Updater.Reconcile(ctx); err != nil {
		w.backend.Logger.Warn("update reconcile at startup failed", "error", err)
	}

	if err := w.backend.Start(ctx); err != nil {
		w.backend.Logger.Error("failed to start backend", "error", err)
	}

	w.restoreWindow()
	return nil
}

// resolveNotificationPermission moves the host out of PermissionNotDetermined,
// which is the state it is born in and the state in which every banner is
// refused.
//
// Refresh first: it never prompts, and on a machine that already authorized
// nocx — a reinstall, an upgrade, a second launch — it is the whole answer.
// Only when the OS says authorization was never requested does this prompt,
// and macOS shows that prompt once per install; an app that never requests
// also never appears in System Settings > Notifications, so skipping it would
// leave the user no way to authorize nocx at all.
//
// A denial is an outcome, not a failure: it is recorded and the banner route
// then fails with ErrDenied per raise, which is what the router surfaces as a
// failed delivery.
func (w *WailsApp) resolveNotificationPermission(host *wailsadapter.Host) {
	ctx := context.Background()
	perm, err := host.Refresh(ctx)
	if err != nil {
		// No surface is a STATED outcome, not a failure to read one: the
		// warning naming the reason has already been logged above, and
		// repeating it as "could not read" would describe a call that was
		// deliberately never made.
		if errors.Is(err, notify.ErrUnavailable) {
			w.backend.Logger.Info("notifications unavailable; no authorization to resolve")
			return
		}
		w.backend.Logger.Warn("could not read notification authorization", "error", err)
		return
	}
	if perm == wailsadapter.PermissionNotDetermined {
		perm, err = host.RequestAuthorization(ctx)
		if err != nil && !errors.Is(err, wailsadapter.ErrDenied) {
			w.backend.Logger.Warn("notification authorization request failed", "error", err)
			return
		}
	}
	w.backend.Logger.Info("notification authorization resolved", "permission", perm.String())
}

// restoreWindow hands the window to internal/uistate and gets out of the way,
// which is the whole of nocx-mqie.1 on this side of the seam.
//
// No decision is made here. Where the window goes is uistate.Restore's, from
// the saved geometry and the displays attached now; WHEN it goes there is
// uistate.RestoreAndWatch's, because it depends on a window that does not exist
// yet at the moment this runs. Both are rules, and a rule that can only run
// with a window on a screen is a rule nobody can test — so this function owns
// two adapters over the Wails API and nothing else.
//
// THE WINDOW IS NOT THERE YET WHEN THIS IS CALLED. ServiceStartup runs inside
// application.Run, before the platform window is realised (v3 runs the pending
// window runnables after the services are up), so a probe here answers (0,0)
// and `ok=false`. This used to be read as a failure and it ended the feature
// for the session: the one-shot probe below the warning returned before the
// sampler was started, so nothing was ever recorded, the document kept its
// zeros, and every launch opened at the default (nocx-39vhn). The waiting is
// inside RestoreAndWatch now, where a fake probe can be made to answer late.
//
// PORTED FROM WAILS v2 AT THE MERGE. The worker built this against the v2
// runtime package (`runtime.WindowSetSize`, `runtime.ScreenGetAll`) because
// its branch was cut from main, where the v3 migration had not landed; the
// seam it built — a Probe the rules are tested against without a display —
// is exactly what made the port a change of two call sites rather than of the
// policy. Nothing in internal/uistate moved.
func (w *WailsApp) restoreWindow() {
	if w.window == nil {
		return
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	w.stopGeometry = cancel
	// Save-on-change, by sampling. v3 does raise move and resize events, and
	// they would be a smaller seam — but the store already coalesces what the
	// poll sees, so a drag of any length costs one write half a second after
	// it stops either way, and swapping the mechanism at a merge would be a
	// change nobody had tested. Filed rather than done here.
	go w.backend.UIState.RestoreAndWatch(
		watchCtx,
		wailsWindowProbe{window: w.window, screens: w.screens},
		wailsWindowPlacer{window: w.window},
		uistate.DefaultSampleInterval,
	)
}

// wailsWindowPlacer applies a placement through the Wails v3 API. Its own type
// rather than more methods on the probe: reading where the window is and
// putting it somewhere are two jobs, and the store asks for them through two
// interfaces. The adapters exist because v3's builders return the window for
// chaining and uistate.Placer, which is about doing a thing rather than
// building one, returns nothing.
type wailsWindowPlacer struct {
	window *application.WebviewWindow
}

func (p wailsWindowPlacer) SetSize(width, height int) { p.window.SetSize(width, height) }
func (p wailsWindowPlacer) SetPosition(x, y int)      { p.window.SetPosition(x, y) }
func (p wailsWindowPlacer) Center()                   { p.window.Center() }
func (p wailsWindowPlacer) Maximise()                 { p.window.Maximise() }
func (p wailsWindowPlacer) Fullscreen()               { p.window.Fullscreen() }

// wailsWindowProbe reads the live window and the attached displays through the
// Wails v3 API. It is the only implementation of uistate.Probe and it lives
// here because this is the only place a window exists — keeping the interface
// on the other side is what lets every rule above it be tested without a
// display.
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
		// The window is not laid out yet (or is minimised). A zero is not a
		// geometry, and recording it would restore a window with no size.
		return uistate.Window{}, nil, false
	}
	x, y := p.window.Position()

	var displays []uistate.Display
	if p.screens != nil {
		// An empty list is reported as no displays, which uistate reads as
		// "unknown" and treats as a mismatch: when we cannot tell, we open
		// somewhere visible.
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

// wailsDialogService opens the platform file and directory pickers through
// the Wails runtime. The renderer never calls it directly; it is the backend
// of the dialog.openFile and dialog.openDirectory control-plane methods.
//
// The platform-adapter contract (transport.DialogService) permits observing
// ctx and dismissing the dialog where the native API allows it. This adapter
// is the case where it does NOT: the v3 open-file dialog has no cancel
// handle once the picker is shown, so the transport's context is
// deliberately ignored and the call returns only when the user acts. The
// transport keeps the capability busy until then (see ws_dialog.go), so a
// reconnect never stacks a second picker over this one.
type wailsDialogService struct {
	app *application.App
	// sources is the mint for upload source tickets. OpenFileForUpload is
	// the only method that touches it, and it is what makes this adapter a
	// transport.UploadPicker as well as a transport.DialogService.
	sources *transport.SourceTicketStore
}

func (d *wailsDialogService) OpenFile(_ context.Context) (string, error) {
	return d.app.Dialog.OpenFile().
		CanChooseFiles(true).
		SetTitle("Choose a private key").
		AddFilter("All files", "*").
		PromptForSingleSelection()
}

// OpenDirectory is the same v3 open dialog restricted to directories:
// CanChooseFiles(false) is what makes a file unselectable, so a caller
// expecting a folder cannot be handed a file. It is bound by the same
// contract as OpenFile above — the picker cannot be dismissed from here, so
// the transport's context is deliberately ignored and the capability stays
// busy until the user acts.
func (d *wailsDialogService) OpenDirectory(_ context.Context) (string, error) {
	return d.app.Dialog.OpenFile().
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		SetTitle("Choose a folder").
		PromptForSingleSelection()
}

// OpenFileForUpload is the same native picker asked a different question,
// and it answers with a TICKET rather than a path (transport.UploadPicker,
// design R2). The path the runtime returns is handed straight to the mint
// and never leaves this function: what goes back over the wire is an opaque
// id, a base name and a size.
//
// The runtime's own error is returned as-is — the picker has not chosen a
// file yet, so there is no path in it to leak. The mint's refusals are
// worded without the path by contract (internal/transport/ws_upload_source.go).
func (d *wailsDialogService) OpenFileForUpload(_ context.Context) (transport.SourcePick, error) {
	path, err := d.app.Dialog.OpenFile().
		CanChooseFiles(true).
		SetTitle("Choose a file to upload").
		AddFilter("All files", "*").
		PromptForSingleSelection()
	if err != nil {
		return transport.SourcePick{}, err
	}
	if path == "" {
		// Cancelled. An empty ticket, not an error — the renderer reads it
		// as "no change", the way dialog.openFile's empty path already works.
		return transport.SourcePick{}, nil
	}
	return d.sources.Mint(path)
}

// handleFilesDropped is the window-drop mint site: Wails hands over the
// dropped absolute paths and every attribute of the element that carried
// data-file-drop-target, and the backend turns them into source tickets the
// renderer can use but could not have authored.
//
// IT RESOLVES NO DESTINATION. The renderer reads data-session-id off the
// notification, finds its own binding and calls files.upload like any other
// caller, so the native gesture goes through the same authorised route
// rather than becoming a second addressing scheme that skips the
// connection's session set (design §5.5).
func (w *WailsApp) handleFilesDropped(event *application.WindowEvent) {
	ctx := event.Context()
	files := ctx.DroppedFiles()
	attrs := map[string]string{}
	if details := ctx.DropTargetDetails(); details != nil && details.Attributes != nil {
		attrs = details.Attributes
	}
	// The COUNT, never the names: a filename is a path here, and the log is
	// a file on disk. The store's own errors are worded the same way.
	if err := w.backend.UploadSources.Dropped(files, attrs); err != nil {
		w.backend.Logger.Warn("dropped files were not offered for upload",
			"error", err, "count", len(files))
	}
}

// wailsUrlOpener opens a URL in the system browser through the Wails
// runtime. The renderer never calls it directly; it is the backend of the
// shell.openUrl control-plane method. v3's Browser.OpenURL reports failure,
// unlike v2's fire-and-forget BrowserOpenURL — an unwired opener is the
// other failure this seam can surface, and that is the dev-web
// configuration.
type wailsUrlOpener struct {
	app *application.App
}

func (o *wailsUrlOpener) OpenURL(_ context.Context, url string) error {
	return o.app.Browser.OpenURL(url)
}

// upgradeInstallPath derives the path to the installed bundle from the
// running executable's path. On macOS, the .app is 3 levels above the
// binary; on Linux, it is the executable itself (the AppImage).
func upgradeInstallPath(execPath string) string {
	if execPath == "" {
		return ""
	}
	// On macOS the binary lives at nocx.app/Contents/MacOS/nocx.
	// Walk up to the .app.
	dir := filepath.Dir(execPath)
	for {
		if filepath.Ext(dir) == ".app" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Not inside a .app — return the executable itself (Linux AppImage).
	return execPath
}

func (w *WailsApp) shutdown() {
	w.backend.Logger.Info("Wails app shutting down")
	// The sampler stops BEFORE the backend and before the window goes: it
	// reads the live window on every tick, and one more tick after the window
	// is gone would be a read of something that no longer exists. The final
	// write is the backend's job — App.Shutdown flushes the store.
	if w.stopGeometry != nil {
		w.stopGeometry()
	}
	w.backend.Shutdown(w.ctx)
}

// ServiceShutdown tears down the manually-started notifications service.
// The framework calls it because WailsApp is itself a registered service;
// it runs after the OnShutdown hook (backend teardown).
//
//wails:ignore
func (w *WailsApp) ServiceShutdown() error {
	if w.notifications != nil {
		return w.notifications.ServiceShutdown()
	}
	return nil
}

func (w *WailsApp) GetWSPort() int {
	return w.backend.WSPort()
}

func (w *WailsApp) GetWSToken() string {
	return w.backend.WSToken()
}

// CheckForUpdate fetches and verifies the signed release manifest.
// Returns an update description if a newer version is available,
// or null when already current or on a dev build.
func (w *WailsApp) CheckForUpdate() *update.UpdateInfo {
	info, err := w.backend.Updater.Check(w.ctx)
	if err != nil {
		w.backend.Logger.Warn("update check failed", "error", err)
		return nil
	}
	w.updateInfo = info
	return info
}

// ApplyUpdate applies a previously checked update. No arguments —
// the update info is already verified and held in backend state.
func (w *WailsApp) ApplyUpdate() error {
	if w.updateInfo == nil {
		return fmt.Errorf("no update available — call CheckForUpdate first")
	}
	return w.backend.Updater.Apply(w.ctx, w.updateInfo)
}

// ReportHealthy signals that the frontend is running correctly.
// Called once the initial tab's renderer mounted and its PTY session
// opened (§7.5). Only then does the updater finalise a pending update.
func (w *WailsApp) ReportHealthy() error {
	return w.backend.Updater.ReportHealthy(w.ctx)
}

// GetUpdateState returns the updater state for the UI notice.
// "pending" means an update was applied and is waiting for a restart;
// empty string means nothing in flight.
func (w *WailsApp) GetUpdateState() string {
	// Reconcile at startup to settle any in-flight transaction.
	// On first call, this detects a pending restart state.
	_ = w.backend.Updater.Reconcile(w.ctx)
	// For now, return empty — the actual state detection will be
	// refined once Reconcile returns a richer status.
	return ""
}

// versionRequested reports whether the process was invoked only to print its
// version. Both spellings that Go's flag package accepts are honoured; the app
// takes no other flags today, so a plain launch always returns false.
func versionRequested() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-version" {
			return true
		}
	}
	return false
}
