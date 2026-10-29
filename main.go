package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/desktopidentity"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/notify/wailsadapter"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/uistate"
	"github.com/shady2k/nocx/internal/update"
	"github.com/shady2k/nocx/internal/update/serverbin"
	"github.com/shady2k/nocx/internal/version"
)

//go:embed all:frontend/dist
var assets embed.FS

// mainWindowName is the shell's name for the one window nocx opens. It is
// how anything outside main() reaches that window back through the v3 window
// manager.
const mainWindowName = "main"

// appName is what the OS and the menu bar call this application. It is one
// constant because two spellings of it show up in one place a person looks at:
// the bundle-less Wails name and the title of the application menu.
const appName = "nocx"

// launchTimeout bounds the whole find-or-raise-the-coordinator sequence,
// including a spawn and its readiness wait. It is the outer bound on how
// long a window can sit blank before it either has a backend or says why it
// has not: never a hang, always an answer (design §4).
const launchTimeout = 45 * time.Second

func main() {
	if handled, err := releaseDiagnostic(context.Background(), os.Args[1:], os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	// Checked before any window exists so CI's release smoke check
	// (distribution design §5) and a user's `nocx --version` print the linked
	// build metadata and exit, never opening a terminal.
	if version.Requested(os.Args[1:]) {
		fmt.Printf("nocx %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return
	}

	// The window's own logger, on stderr. THE BACKEND'S LOG IS NOT THIS
	// PROCESS'S ANY MORE: nocx-server owns the sessions, so it owns the log
	// file that records what they did. What is left here is the handful of
	// lines about finding a coordinator, which is exactly what a person
	// needs when the window comes up blank.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	wailsApp := &WailsApp{logger: logger}

	// WHO DECIDES A QUIT, AND WHO OWNS THE KEYS THAT ASK FOR ONE. See
	// [quitSeam]: the menu bar's item and the Cmd+Q chord are two keys to one
	// decision, and the chord is claimed by the window rather than inherited
	// from a menu nobody here wrote.
	quit := &quitSeam{logger: logger}
	wailsApp.quit = quit

	// The Wails v3 shell. The window is created before Run; on Linux the
	// platform defers actually loading the webview until activation inside
	// Run, which happens after ServiceStartup. The frontend's first
	// ResolveBackend binding call therefore sees the held startup outcome.
	shell := application.New(application.Options{
		Name:        appName,
		Description: "A local-first, Warp-style terminal",
		Assets: application.AssetOptions{
			// Bundled, not plain: it serves /wails/runtime.js, which the v3
			// frontend runtime's HTTP transport fetches for every call.
			Handler: application.BundledAssetFileServer(assets),
		},
		Services: []application.Service{
			application.NewService(&desktopIdentityService{}),
			application.NewService(wailsApp),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		Linux: application.LinuxOptions{
			// GTK uses this for its Wayland app_id and X11 window class.
			// GtkApplication has a separate ID, set by the first service.
			ProgramName: desktopidentity.ID,
		},
		OnShutdown: wailsApp.shutdown,
	})

	// The quit ends in Wails' own Quit, which is the path AppKit's terminate
	// takes too (OnShutdown above, then the process).
	quit.quit = shell.Quit

	// THE MENU BAR IS OURS NOW, because the item a person reads there has to be
	// the one this app decided on — on macOS, which is the only platform where
	// this application shows a menu bar (no window here sets
	// WebviewWindowOptions.UseApplicationMenu). Setting it here and not inside
	// Run is the supported order: Menu.Set stores the menu and Wails installs
	// it once the application is running (macosApp.setApplicationMenu), which
	// is also the only moment AppKit accepts a main menu.
	if runtime.GOOS == "darwin" {
		menu, _ := quit.menu(appName, shell.Browser.OpenURL)
		shell.Menu.Set(menu)
	}

	// THE SIZE THE WINDOW OPENS AT. Wails wants it before there is a window.
	// The saved geometry lives in the UI-state document, which the
	// COORDINATOR owns now — this process has no store to read and must not
	// open a second one over the daemon's (ADR-0043 is the same argument one
	// level up). So the opening pass asks the rule for its default with
	// nothing saved, which is what it has always answered for a first launch,
	// and the real geometry is restored once the renderer has read it from
	// the coordinator — HostRestoreWindow below, the client-host shape of
	// design D3 (ADR-0098).
	opening := uistate.Restore(uistate.Window{}, nil)

	window := shell.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       mainWindowName,
		Title:                      "nocx",
		Width:                      opening.Width,
		Height:                     opening.Height,
		MinWidth:                   uistate.MinWindowWidth,
		MinHeight:                  uistate.MinWindowHeight,
		DefaultContextMenuDisabled: true,
		// THE QUIT CHORD IS CLAIMED BY THE WINDOW, not by the page and not by
		// the menu bar alone (nocx-1ebrs.13). Wails hands this map to
		// -[WebviewWindow performKeyEquivalent:], which runs before the
		// responder chain — i.e. before a WKWebView can answer the chord
		// itself, which is exactly how Cmd+Q went missing while the menu's
		// Quit item worked. See [quitSeam.keyBindings].
		KeyBindings: quit.keyBindings(runtime.GOOS),
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
			// The tab strip IS the title bar, Tabby-style: no title text and
			// no second row stealing ~28px of terminal. TitleBarHidden, not
			// TitleBarHiddenInset: the two differ only by UseToolbar, and that
			// NSToolbar left the window unrestorable after minimising
			// (nocx-dqg; cf. wailsapp/wails#1319).
			TitleBar: application.MacTitleBarHidden,
		},
	})
	// The window is NOT shown from here, and the call that used to be here did
	// nothing: `WebviewWindow.Show` returns early while the platform
	// implementation is nil, and it is nil until `shell.Run` below builds it.
	// The one thing that actually puts the window on screen is Wails' own
	// order-front on the webview's first finished navigation. So the shell is
	// handed to the service, and the startup path raises it from a place where
	// the call works — see afterLaunch (nocx-1ebrs.6).
	wailsApp.window = window
	wailsApp.shell = &wailsShell{window: window, logger: logger}
	// The window half of the UI-state document (ADR-0098). The shell owns
	// the native window and the coordinator owns the document, so the shell
	// restores from what the renderer relays and samples for the renderer to
	// relay back — see HostRestoreWindow for the ordering that makes that
	// safe.
	wailsApp.geometry = newWindowGeometry(
		wailsWindowProbe{window: window, screens: shell.Screen.GetAll},
		wailsWindowPlacer{window: window},
		func(sample uistate.Window) { window.EmitEvent(windowSampledEvent, sample) },
		uistate.DefaultSampleInterval,
	)

	if err := shell.Run(); err != nil {
		logger.Error("application error", "error", err)
		os.Exit(1)
	}
}

// shellWindow is the part of the shell the launch path needs: the window
// itself, and a place to put a sentence a person must not miss.
//
// Two calls, one owner. The launcher announces BEFORE anything can be shown —
// that ordering is deliberate (a stop that then fails must still leave the
// person told), so the notice waits in [noticeRecorder] until there is a
// window to put it over. This is that window, abstracted so the ordering rule
// can be written down and checked in a test at all (nocx-1ebrs.6).
type shellWindow interface {
	// Show raises the window.
	Show()
	// Present shows one notice as a dialog a person has to dismiss.
	Present(notice coordinator.Notice)
}

// raisableWindow is the one window this shell may raise, stated as the two
// questions [wailsShell.Show] asks of it.
//
// IT IS NOT `*application.WebviewWindow`, WHICH WOULD BE THE OBVIOUS TYPE, for
// the same reason every other platform rule here is behind a seam: the defect
// below is a rule about WHEN Show may be called, and a rule with no way to
// state it is a rule nothing can hold. See [wailsShell.Show].
type raisableWindow interface {
	// IsVisible reports whether the platform has BUILT the window and put it
	// on screen. It is false for as long as the platform implementation does
	// not exist, which is the whole of the question Show has to ask.
	IsVisible() bool
	// Show raises the window.
	Show() application.Window
}

// wailsShell is the shellWindow this process actually has.
type wailsShell struct {
	window raisableWindow
	logger *slog.Logger
}

// Show raises the window, and ONLY one the platform has already built.
//
// THE GUARD IS THE FIX FOR THE v0.6.1 RELEASE BLOCKER, and it is here rather
// than at a call site because the call it wraps is a create-or-show: any caller
// that reached Wails' Show a moment too early would recreate this defect, and a
// guard at the seam is one place instead of one per caller.
//
// `WebviewWindow.Show` is not a show: with no platform
// implementation it calls `WebviewWindow.Run`, which CREATES the platform
// window (`impl = newWindowImpl(w)`, then the platform's own `run`). Called
// from the startup path — which is inside `App.Run` BEFORE that function
// reaches the platform's main loop — that creation is on the main thread with
// the application not yet registered, and GTK3 answers with a SIGSEGV inside
// cgo:
//
//	SIGSEGV: segmentation violation addr=0x18
//	signal arrived during cgo execution
//	_Cfunc_gtk_application_window_new ← windowNew ← (*linuxWebviewWindow).run
//	  ← WebviewWindow.Run ← WebviewWindow.Show ← wailsShell.Show
//	  ← afterLaunch ← ServiceStartup ← App.Run
//
// Measured on the v0.6.1 AppImage: every mutation of the release gate died this
// way, before WebKit's own helper processes were spawned, so the gate could not
// even report the helper-spawn failure it exists for (ADR-0095). Wails builds
// and shows the window itself a moment later — `linuxWebviewWindow.run` shows
// it unless the window is Hidden, and the darwin one does so from
// WebViewDidFinishNavigation — so nothing is lost by raising only what exists.
func (s *wailsShell) Show() {
	if s.window == nil || !s.window.IsVisible() {
		return
	}
	s.window.Show()
}

// Present raises one dialog, over a window that is already on screen.
//
// A DIALOG, not a log line and not a toast: D4 permits A1 to kill an
// incompatible coordinator and lose its sessions, and requires that the loss be
// said out loud. A person who has just watched their SSH connections die has to
// be told that is what happened, and a message that can be missed is not
// telling them.
//
// It stays UNATTACHED to the window on purpose. A Wails sheet needs a visible
// window to attach to and is dropped silently when there is not one, which is
// the failure this bead is about; a modal with a window behind it has no such
// condition. What the notice needed was the window, not a parent.
func (s *wailsShell) Present(notice coordinator.Notice) {
	app := application.Get()
	if app == nil {
		// Nothing to raise it with. Said rather than swallowed — the caller
		// has already decided this notice is not droppable.
		s.logger.Warn("nocx: no application to show a notice on", "kind", string(notice.Kind))
		return
	}
	app.Dialog.Warning().
		SetTitle("The nocx backend was replaced").
		SetMessage(notice.Message).
		Show()
}

// BackendResolution is the result of one coordinator discovery attempt.
// Token is returned only to the renderer through this binding; it is never
// written to logs, argv or the spawned daemon's environment.
type BackendResolution struct {
	OK      bool   `json:"ok"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Remedy  string `json:"remedy"`
}

// WailsApp is the bound service (v3) the frontend reaches over the Wails
// runtime.
//
// IT IS A WINDOW AND A LAUNCHER, and no longer a backend (design D3). The
// sessions, the vault, the stores and the WebSocket live in nocx-server, a
// process of their own that outlives this window; what this struct holds is
// how to reach that process and what to tell the person when reaching it
// went badly.
type WailsApp struct {
	logger          *slog.Logger
	window          *application.WebviewWindow
	restartWindowFn func(string) error

	// launchCoordinatorFn is the launcher seam used by ResolveBackend. The
	// production value is nil, which selects launchCoordinator; tests can
	// exercise both binding outcomes without starting a daemon.
	launchCoordinatorFn func(context.Context, string) (coordinator.Launch, error)

	// launchMu serializes discovery attempts and protects launchFailure. The
	// renderer normally provides single-flight, but the binding remains safe
	// when callers arrive concurrently.
	launchMu sync.Mutex

	// launchFailure holds a startup failure until the first binding call can
	// report it to the renderer. A later call retries the launcher.
	launchFailure *coordinator.LaunchFailure

	// notices holds what the launcher said a person must be told — today
	// only that an incompatible coordinator was replaced and its sessions
	// died (D4). Shown as a dialog once the shell can raise one.
	notices noticeRecorder

	// shell is the window surface a notice is raised on, and nil until main has
	// created the window. A nil shell means "nothing can be shown yet": every
	// notice stays recorded rather than being dropped, because the one message
	// this exists for is the one an upgrade depends on (nocx-1ebrs.6).
	shell shellWindow

	// updater applies signed releases to the installed bundle. It belongs to
	// the WINDOW rather than to the daemon in A1: it is about the files on
	// disk, not about the sessions, and the health report that certifies an
	// update is a renderer call that arrives here. It is wired with the
	// probe below, so the health report certifies a PAIR (D4).
	updater update.Updater

	// probe tells the updater which coordinator answered this window. It
	// is constructed empty, before the launcher has run, and filled by the
	// launch — see [coordinator.LaunchProbe] for why that order is forced.
	probe *coordinator.LaunchProbe

	// updateInfo holds the most recent Check result. Apply takes no
	// arguments — it applies the update that Check already verified.
	updateInfo *update.UpdateInfo

	// updateInstallPath is chosen with the updater and reused for relaunch.
	updateInstallPath string

	// updatePlacementErr remains visible when placement prevents construction.
	updatePlacementErr error

	// updateFetch is the manifest fetcher the updater was wired with, held
	// so SetUpdateChannel can flip which channel's manifest the next check
	// reads (ADR-0097). nil exactly when updater is: a window that cannot
	// check for updates cannot switch its channel either.
	updateFetch *update.GitHubManifestFetcher

	// notifications is the v3 notifications service, started by hand rather
	// than registered (see ServiceStartup), and torn down in
	// ServiceShutdown. nil when it never started.
	notifications *notifications.NotificationService

	// attention is the desktop attention surface this window IMPLEMENTS for
	// the coordinator (design D3, A1.2). The coordinator has no desktop, so
	// it asks a client to raise a banner; this is what raises it. nil until
	// ServiceStartup has built it, and on a host whose notification service
	// would not start it is still built — every raise then fails loudly per
	// call, which is the contract that predates the split.
	attention *wailsadapter.Host

	// geometry owns this window's half of the UI-state document's window
	// record (ADR-0098): applying the saved placement once the renderer has
	// read it from the coordinator, and sampling the live window for the
	// renderer to relay back. nil until main has created the window.
	geometry *windowGeometry

	// quit is the one place a quit is decided (ADR-0093): the chord, the
	// menu item and FinishPendingUpdate all end in [quitSeam.request]. nil
	// until main wires it, and a FinishPendingUpdate on a window without
	// one refuses rather than stopping sessions it cannot then leave.
	quit *quitSeam

	// finishStopKeptCoordinatorFn is the seam FinishPendingUpdate stops a
	// kept coordinator through. The production value is nil, which selects
	// stopKeptCoordinator; tests stand on the decision without signalling
	// a real process.
	finishStopKeptCoordinatorFn func(ctx context.Context) (bool, error)
}

// Log logs a message from the frontend.
//
// It reaches this process's stderr rather than the backend's log file: the
// renderer is this window's, and the file belongs to the daemon now.
func (w *WailsApp) Log(message string) {
	w.logger.Info("frontend", "message", message)
}

// RestartWindow starts the installed build, then quits this window. The new
// Wails process runs the ordinary startup path, which finds the compatible
// coordinator and preserves its live sessions.
func (w *WailsApp) RestartWindow() error {
	installPath := w.updateInstallPath
	if installPath == "" {
		return errors.New("the update install target is unavailable; check write access to the installation directory and restart nocx")
	}
	if w.quit == nil {
		return errors.New("this window cannot quit the application")
	}
	start := w.restartWindowFn
	if start == nil {
		start = startInstalledApplication
	}
	if err := start(installPath); err != nil {
		return fmt.Errorf("starting the installed nocx build: %w", err)
	}
	w.quit.request(quitFromUpdateRestart)
	return nil
}

// startInstalledApplication starts the bundle or AppImage exchanged by the
// updater. macOS waits for Launch Services to accept the `open` request. On
// Linux, successful process creation is the launch boundary: it does not prove
// the new AppImage stayed healthy. The child is waited asynchronously so its
// process resources are reaped if it exits after this app quits.
func startInstalledApplication(installPath string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("/usr/bin/open", "-n", installPath).Run()
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("restart after update is not supported on %s", runtime.GOOS)
	}
	cmd := exec.Command(installPath)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// LogFilePath reports where the backend log file lives. "" means
// unavailable, which is the honest answer while the log belongs to a process
// this one only talks to over a socket: the discovery handshake carries four
// facts and this is not one of them. Restoring it is the client host's
// (design D3, A1.2) — it needs a control-plane call, not a guess at a path.
func (w *WailsApp) LogFilePath() string {
	return ""
}

// ServiceStartup is the v3 lifecycle hook that replaced the v2 OnStartup
// callback: it runs once, during app.Run, before the webview loads the
// frontend (see main). It is where the coordinator is found or raised,
// because that is the last moment before a binding call can arrive and the
// first moment at which a failure can be shown to a person.
//
//wails:ignore
func (w *WailsApp) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	w.logger.Info("nocx window starting up", "version", version.Version, "commit", version.Commit)

	// The trust root for every update this build will ever accept. It is
	// compiled in (internal/update/keyring.go). A keyring that will not
	// decode costs the user their update check and nothing else: the app
	// starts, and VerifyManifest then refuses every manifest, which is the
	// direction to fail in.
	execPath, err := os.Executable()
	if err != nil {
		w.logger.Warn("cannot determine executable path", "error", err)
	}
	// Construct the pair probe before reconciliation and attach it only after
	// launch. Until then it refuses to certify health (D4).
	w.probe = coordinator.NewLaunchProbe()
	cfg, fetch, _, keyringErr := productionWindowUpdaterConfig(log.NewSlogAdapter(w.logger), w.probe)
	if keyringErr != nil {
		w.logger.Error("release keyring unusable; updates cannot be verified on this build", "error", keyringErr)
	}
	updatePaths, updatePathsErr := storage.NewAppPaths()
	if updatePathsErr == nil {
		w.updater, w.updateInstallPath, updatePathsErr = newWindowUpdater(execPath, updatePaths.DataDir(), cfg)
	}
	if updatePathsErr != nil {
		w.updatePlacementErr = updatePathsErr
		w.logger.Warn("update placement unavailable", "error", updatePathsErr)
	}
	if w.updater != nil {
		w.updateFetch = fetch
	}
	// Settle any transaction in flight from a previous launch.
	if w.updater != nil {
		if reconcileErr := w.updater.Reconcile(ctx); reconcileErr != nil {
			w.logger.Warn("update reconcile at startup failed", "error", reconcileErr)
		}
	}

	launchCtx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	launch, err := w.runLaunch(launchCtx, execPath)
	if err != nil {
		// Keep the typed outcome until the frontend can ask for it. The
		// window must remain alive so Retry can run the launcher again.
		failure := launchFailure(err)
		w.launchMu.Lock()
		w.launchFailure = failure
		w.launchMu.Unlock()
		w.logger.Error("nocx cannot reach its backend", "error", err)
		return nil
	}
	// From here a health report can certify: the updater can name the
	// backend that answered this window, and refuse anything else.
	w.probe.Attach(launch)
	w.logger.Info("nocx window has a backend",
		"version", launch.Hello.Build.Version,
		"commit", launch.Hello.Build.Commit,
		"protocol", launch.Hello.Protocol,
		"wsAddress", launch.Hello.WSAddress,
		"spawned", launch.Spawned,
		"replaced", launch.Replaced,
	)
	// The desktop attention surface this window implements for the
	// coordinator (design D3). Built AFTER the launcher, because a window
	// with no backend quits and never presents anything.
	w.startAttention(ctx)

	// THE WINDOW IS NOT RAISED HERE, and the call that used to be here is what
	// blocked v0.6.1's Linux release: Wails builds a window only once the
	// services have returned (`App.Run` dispatches the pending window after
	// this hook), so asking for it from here asks Wails to CREATE it, on the
	// GTK3 path before `g_application_run` has registered the application —
	// a SIGSEGV inside cgo, reproduced from this line (ADR-0095, and
	// [wailsShell.Show] for the stack).
	//
	// Nothing waits on it: Wails shows the window itself when it builds it, and
	// the notice the launcher recorded is presented from ResolveBackend — the
	// renderer's first call, which cannot happen until the webview has loaded
	// and the window is therefore on screen (nocx-1ebrs.6).
	return nil
}

// startAttention brings up the notification surface this window offers the
// coordinator.
//
// The notifications service is started BY HAND rather than registered as a
// Wails service: a registered service whose ServiceStartup fails aborts
// app.Run (v3 services.go), and on Linux the service's startup connects the
// session D-Bus, which a bus-less host lacks. Failing per call is the older
// contract and the right one — the app starts, and banners on a bus-less host
// fail loudly per raise. ServiceShutdown is called by the framework because
// WailsApp is itself a registered service.
func (w *WailsApp) startAttention(ctx context.Context) {
	ns := notifications.New()
	w.notifications = ns
	notificationsUp := true
	if err := ns.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		notificationsUp = false
		w.logger.Warn("notification service unavailable; banners will fail per raise", "error", err)
	}
	w.attention = wailsadapter.New(wailsadapter.Deps{
		Service: ns,
		Log:     w.logger,
		// THE ROOT IS THE CALLER WITH REAL KNOWLEDGE, and the adapter's
		// default says so: v3.0.0-beta.9 exposes no availability probe, so
		// the default assumes a surface exists and lets every failure land
		// at send time. This is the caller that has just watched
		// ServiceStartup fail and written the reason into the log; passing
		// that verdict on is what stops the host being born NotDetermined
		// over a service that never started — on macOS that reached
		// +[UNUserNotificationCenter currentNotificationCenter] in a process
		// with no bundle, an Objective-C exception Go cannot recover, and
		// the whole application aborted.
		IsAvailable: func() bool { return notificationsUp },
		Focus:       w.reportAttentionActivated,
	})

	// Resolve the OS's authorization state, and ask for it once when it has
	// never been asked. Without this the host stays PermissionNotDetermined
	// for the life of the process and every banner is refused with
	// ErrNotRequested — including on a machine that has already authorized
	// nocx.
	//
	// Off the startup path deliberately. On macOS the check waits on the OS
	// for up to 15s and the request for as long as the user takes to answer,
	// and ServiceStartup runs before the webview loads — inline, either one
	// would hold the window shut.
	go w.resolveNotificationPermission(w.attention)
}

// reportAttentionActivated is what a banner click does in this process, and
// it is deliberately almost nothing: it tells the renderer, which tells the
// coordinator (host.attentionActivated).
//
// THE SHELL DOES NOT DECIDE WHAT A CLICK MEANS. Which window is raised and
// which pane is focused depend on which connection holds that session, and
// only the coordinator knows that — this window may not even be the one
// showing the pane. Raising the window here would be the shell owning a
// decision AD-3 keeps on the other side; the coordinator asks for the raise
// back through window.focus when it has decided.
//
// The Wails event is the only channel available: the WebSocket belongs to the
// renderer, and this callback runs in the shell.
func (w *WailsApp) reportAttentionActivated(sessionID string) error {
	if w.window == nil {
		return fmt.Errorf("notification click: window %q is gone", mainWindowName)
	}
	w.window.EmitEvent(attentionActivatedEvent, sessionID)
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
			w.logger.Info("notifications unavailable; no authorization to resolve")
			return
		}
		w.logger.Warn("could not read notification authorization", "error", err)
		return
	}
	if perm == wailsadapter.PermissionNotDetermined {
		perm, err = host.RequestAuthorization(ctx)
		if err != nil && !errors.Is(err, wailsadapter.ErrDenied) {
			w.logger.Warn("notification authorization request failed", "error", err)
			return
		}
	}
	w.logger.Info("notification authorization resolved", "permission", perm.String())
}

// ServiceShutdown tears down the manually-started notifications service. The
// framework calls it because WailsApp is itself a registered service.
//
//wails:ignore
func (w *WailsApp) ServiceShutdown() error {
	if w.geometry != nil {
		w.geometry.stop()
	}
	if w.notifications != nil {
		return w.notifications.ServiceShutdown()
	}
	return nil
}

// runLaunch keeps ServiceStartup and ResolveBackend on one launcher path.
func (w *WailsApp) runLaunch(ctx context.Context, execPath string) (coordinator.Launch, error) {
	if w.launchCoordinatorFn != nil {
		return w.launchCoordinatorFn(ctx, execPath)
	}
	return w.launchCoordinator(ctx, execPath)
}

// ResolveBackend runs one discovery attempt. A startup failure is reported
// once before a later call retries, so the renderer sees the original reason
// without turning startup into a second launch.
func (w *WailsApp) ResolveBackend() BackendResolution {
	w.launchMu.Lock()
	defer w.launchMu.Unlock()

	// This binding is the renderer's first call, so it is the first moment at
	// which the window is known to be on screen — the webview has loaded and is
	// running. Everything that must not be said to an empty screen is said
	// here: the window, and then whatever the launcher did on the way.
	w.afterLaunch()

	if failure := w.launchFailure; failure != nil {
		w.launchFailure = nil
		return backendFailureResolution(failure)
	}

	ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
	defer cancel()
	execPath, err := os.Executable()
	if err != nil {
		failure := coordinator.NewLaunchFailure(
			coordinator.FailureProfileUnusable,
			"The application executable path could not be resolved.",
			"Check that nocx can read its installation, then retry.",
			err,
		)
		return backendFailureResolution(failure)
	}
	launch, err := w.runLaunch(ctx, execPath)
	if err != nil {
		if w.logger != nil {
			w.logger.Error("nocx backend discovery failed", "error", err)
		}
		return backendFailureResolution(launchFailure(err))
	}
	if w.probe != nil {
		w.probe.Attach(launch)
	}
	if w.attention == nil && w.window != nil {
		w.startAttention(context.Background())
	}
	return backendResolution(launch.Hello)
}

func launchFailure(err error) *coordinator.LaunchFailure {
	if failure, ok := coordinator.AsLaunchFailure(err); ok {
		return failure
	}
	return coordinator.NewLaunchFailure(
		coordinator.FailureNotReady,
		"The nocx backend could not be reached.",
		"Retry the launch, then check the backend's startup log if it continues.",
		err,
	)
}

func backendFailureResolution(failure *coordinator.LaunchFailure) BackendResolution {
	return BackendResolution{
		Kind:    string(failure.Kind),
		Message: failure.Message,
		Remedy:  failure.Remedy,
	}
}

func backendResolution(hello coordinator.Hello) BackendResolution {
	host, portText, err := net.SplitHostPort(hello.WSAddress)
	if err != nil {
		return backendFailureResolution(coordinator.NewLaunchFailure(
			coordinator.FailureNotReady,
			"The nocx backend reported an invalid WebSocket address.",
			"Retry the launch, then check the backend's startup log if it continues.",
			err,
		))
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return backendFailureResolution(coordinator.NewLaunchFailure(
			coordinator.FailureNotReady,
			"The nocx backend reported an invalid WebSocket port.",
			"Retry the launch, then check the backend's startup log if it continues.",
			err,
		))
	}
	return BackendResolution{
		OK:    true,
		Host:  host,
		Port:  port,
		Token: hello.WSToken,
	}
}

func profilePathHint() string {
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, storage.AppDirName)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", storage.AppDirName)
	}
	return storage.AppDirName
}

func serverPathHint(execPath, dataDir string) string {
	if runtime.GOOS == "darwin" {
		return serverbin.SiblingPath(execPath)
	}
	return filepath.Join(dataDir, serverbin.DirName)
}

func serverBinaryRemedy() string {
	if runtime.GOOS == "linux" {
		return "Repair the nocx server binary under ~/.local/share/nocx/bin, then retry."
	}
	if runtime.GOOS == "darwin" {
		return "Reinstall nocx, then retry."
	}
	return "Reinstall nocx, then retry."
}

// launchCoordinator finds the running nocx-server or raises one.
//
// This is the composition root for the launcher: every seam it depends on —
// the dial, the spawn, the stop, the surface a notice appears on — is a real
// implementation constructed here and nowhere else (AD-8), so the launcher
// itself can be tested without putting a process on the machine.
func (w *WailsApp) launchCoordinator(ctx context.Context, execPath string) (coordinator.Launch, error) {
	paths, err := storage.NewAppPaths()
	if err != nil {
		profilePath := profilePathHint()
		return coordinator.Launch{}, coordinator.NewLaunchFailure(
			coordinator.FailureProfileUnusable,
			fmt.Sprintf("The profile directories at %s could not be read or written.", profilePath),
			fmt.Sprintf("Check that %s can be read and written, then retry.", profilePath),
			err,
		)
	}
	// Where this build's nocx-server must be spawned from. On darwin that
	// is the binary beside this executable inside the bundle; on Linux it
	// is a versioned copy outside the AppImage, whose FUSE mount does not
	// survive this process (design §4). serverbin owns that split and the
	// reasoning for it; this is the composition root handing it the three
	// facts it must not guess.
	serverPath, err := serverbin.New(serverbin.NewOSFS(), log.NewSlogAdapter(w.logger)).
		Resolve(ctx, serverbin.Target{
			GOOS:    runtime.GOOS,
			ExePath: execPath,
			DataDir: paths.DataDir(),
			Version: version.Version,
		})
	if err != nil {
		pathHint := serverPathHint(execPath, paths.DataDir())
		return coordinator.Launch{}, coordinator.NewLaunchFailure(
			coordinator.FailureServerBinaryUnusable,
			fmt.Sprintf("The nocx server binary at %s could not be installed or used.", pathHint),
			serverBinaryRemedy(),
			err,
		)
	}

	dir := coordinator.RuntimeDir(paths)
	self := coordinator.ClientIdentity{
		Version:  version.Version,
		Commit:   version.Commit,
		Protocol: coordinator.ProtocolVersion,
	}
	client, err := coordinator.NewClient(coordinator.ClientConfig{
		Socket: coordinator.SocketPathIn(dir),
		Self:   self,
		Dialer: coordinator.SystemDialer{},
		Logger: w.logger,
	})
	if err != nil {
		return coordinator.Launch{}, err
	}
	launcher, err := coordinator.NewLauncher(coordinator.LauncherConfig{
		Dir:    dir,
		Self:   self,
		Client: client,
		Spawner: coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{
			Path:   serverPath,
			Logger: w.logger,
		}),
		Stopper:   coordinator.NewSignalStopper(coordinator.SignalStopperConfig{Logger: w.logger}),
		Announcer: &w.notices,
		Logger:    w.logger,
	})
	if err != nil {
		return coordinator.Launch{}, err
	}
	return launcher.Launch(ctx)
}

// noticeRecorder is the launcher's [coordinator.Announcer]: it collects what
// a person must be told while there is not yet a surface to tell them on.
//
// The launcher runs before the renderer has connected to anything, so a
// notice cannot travel over the control plane; and by design the launcher
// must not know how this shell shows things. So it states the fact here, and
// the shell shows it as soon as it can.
type noticeRecorder struct {
	mu      sync.Mutex
	notices []coordinator.Notice
}

func (r *noticeRecorder) Announce(n coordinator.Notice) {
	r.mu.Lock()
	r.notices = append(r.notices, n)
	r.mu.Unlock()
}

func (r *noticeRecorder) drain() []coordinator.Notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	drained := r.notices
	r.notices = nil
	return drained
}

// afterLaunch raises the window, then says what the launcher did.
//
// ONE FUNCTION, because the order is the contract. A notice raised before there
// is a window is a modal with nothing behind it, and that is not a cosmetic
// detail: on the launch that follows an upgrade the person dismissed the notice
// and got no window at all, on the one launch every existing user takes
// (nocx-1ebrs.6).
//
// CALLED FROM ResolveBackend AND NOWHERE ELSE, and that is a boundary rather
// than a shortage of call sites: ResolveBackend is the renderer's first call,
// so the webview is running and the window is therefore on screen — while the
// startup path runs before Wails has built a window at all, and asking for it
// there makes Wails create it on the GTK3 main thread before the application is
// registered (ADR-0095, [wailsShell.Show]).
//
// It is idempotent by construction: Show is, and the recorder drains once.
func (w *WailsApp) afterLaunch() {
	if w.shell != nil {
		w.shell.Show()
	}
	w.showNotices()
}

// showNotices raises one dialog per recorded notice, and KEEPS the ones it has
// nowhere to raise.
//
// The recorder is drained only once a surface exists. It used to be drained
// first and the absence of a Wails application checked second, so a notice that
// arrived before the window existed — which is every notice the launcher raises
// — was taken out of the recorder and shown to nobody (nocx-1ebrs.6).
func (w *WailsApp) showNotices() {
	if w.shell == nil {
		return
	}
	for _, n := range w.notices.drain() {
		w.logger.Warn("nocx: telling the user what the launcher did",
			"kind", string(n.Kind), "message", n.Message)
		w.shell.Present(n)
	}
}

func (w *WailsApp) shutdown() {
	// The daemon is NOT stopped here. It outliving this window is the whole
	// point of moving it out (design §1); when the last client detaches it
	// ends itself after a grace period, which is nocx-server's decision and
	// not a window's.
	w.logger.Info("nocx window shutting down")
}

// ── quitting ──────────────────────────────────────────────────────────────

// THE QUIT KEY IS THE WINDOW'S, NOT THE MENU BAR'S AND NOT THE PAGE'S.
//
// Measured on the v0.6.0 clean-Mac pass (nocx-1ebrs.13): choosing
// nocx > Quit nocx ended the process, and pressing Cmd+Q with the command
// editor focused did nothing. nocx wrote no menu at all, so the item in the
// menu bar was AppKit's, and a WKWebView in the key window answers a
// modifier-key chord before the main menu's key equivalent is consulted — the
// "the item works, its shortcut does not" signature.
//
// So the chord is claimed HERE, by the window, through the one hook that runs
// before the webview sees the key (Wails' -[WebviewWindow
// performKeyEquivalent:] consults the window's KeyBindings and consumes the
// event when one matches), and the menu item is written here too, so that the
// accelerator a person reads in the menu bar is a decision this application
// made rather than one AppKit synthesised. Both routes end in
// [quitSeam.request]: one behaviour, two keys.
//
// FOCUS IS NOT PART OF THE QUESTION, and that is the property the fix buys. A
// window key equivalent is claimed before the responder chain runs, so it does
// not matter what holds the keyboard — the pane's input element, the command
// editor, a sidebar row, or nothing. Nothing in the renderer claims meta+q
// either (grep `metaKey` across frontend/src finds the save, copy, paste,
// sidebar, snippet and ask chords, and none for `q`), so there is no second
// surface the chord could reach first.
//
// AND THE DAEMON IS NOT ASKED. Sessions — including one whose shell
// integration never sent its hello — live in nocx-server. This window holds no
// session state, so no session can hold a quit open: [quitSeam.request] makes
// the decision and calls Quit, and the only thing a quit runs in this process
// is [WailsApp.shutdown], which logs.

// quitChord is the accelerator that quits nocx.
//
// ITS SPELLING IS THE WINDOW'S, NOT THE MENU BAR'S. Wails compares the
// accelerator it parses out of a registered binding against the one it parses
// out of the NSEvent, and the darwin window builds that event string itself as
// lower-case modifiers joined to the key ("cmd+q"). Both spellings canonicalise
// to Cmd+Q, and writing the event side's own spelling is what keeps this one
// string instead of two that have to stay in step.
const quitChord = "cmd+q"

// quitCause names the key a quit came from. It exists for the log: a line that
// says only "quitting" cannot tell a working chord from a working menu item,
// which is the difference this defect turned on.
type quitCause string

const (
	quitFromMenu  quitCause = "menu"
	quitFromChord quitCause = "chord"
	// quitFromUpdateFinish is the explicit held-update action that stops the
	// kept coordinator before quitting. It is distinct from the session-
	// preserving update restart below.
	quitFromUpdateFinish  quitCause = "update-finish"
	quitFromUpdateRestart quitCause = "update-restart"
)

// quitSeam is the one place a quit is decided.
type quitSeam struct {
	logger *slog.Logger

	// quit ends the application: the Wails Quit, wired in main. It is a field
	// so a test can stand on the decision instead of on the process.
	quit func()
}

// request takes the decision, says who asked, and performs it.
func (q *quitSeam) request(cause quitCause) {
	q.logger.Info("nocx quitting", "cause", string(cause))
	q.quit()
}

// keyBindings is the window's claim on the quit chord, in the shape
// WebviewWindowOptions.KeyBindings takes. goos is passed rather than read for
// the same reason [serverbin.Target] takes it: a rule that differs per platform
// is a rule a test has to be able to state for the platform it is not on.
//
// NIL OFF macOS. Cmd+Q is a Mac convention and what it means to a person is
// this application's decision. Ctrl+Q is not: it is XON, a key a person sends
// to the PROGRAM in the pane to resume output it paused, and a platform whose
// menu bar does not show this chord must not take that key from the terminal.
func (q *quitSeam) keyBindings(goos string) map[string]func(application.Window) {
	if goos != "darwin" {
		return nil
	}
	return map[string]func(application.Window){
		quitChord: func(application.Window) { q.request(quitFromChord) },
	}
}

// menu is the menu bar nocx shows on macOS.
//
// THE APPLICATION MENU IS WRITTEN HERE, and the other five are the platform's.
// That split is the whole decision: Wails' own application menu carries a Quit
// ROLE item, whose click AppKit sends as `terminate:` — the item that worked
// during this defect, and the one an application cannot log, test or label for
// itself. File, Edit, View and Window remain Wails' menus; Help is written
// here because Wails' default Learn More points to its framework homepage.
func (q *quitSeam) menu(name string, openURL func(string) error) (*application.Menu, func(*application.Context)) {
	menu := application.NewMenu()

	appMenu := menu.AddSubmenu(name)
	appMenu.AddRole(application.About)
	appMenu.AddSeparator()
	appMenu.AddRole(application.ServicesMenu)
	appMenu.AddSeparator()
	appMenu.AddRole(application.Hide)
	appMenu.AddRole(application.HideOthers)
	appMenu.AddRole(application.ShowAll)
	appMenu.AddSeparator()
	q.addQuitItem(appMenu, name)

	menu.AddRole(application.FileMenu)
	menu.AddRole(application.EditMenu)
	menu.AddRole(application.ViewMenu)
	menu.AddRole(application.WindowMenu)
	helpMenu := menu.AddSubmenu("Help")
	openDocumentation := func(*application.Context) {
		if err := openURL("https://shady2k.github.io/nocx/"); err != nil {
			q.logger.Warn("could not open nocx documentation", "error", err)
		}
	}
	helpMenu.Add("Learn More").OnClick(openDocumentation)
	return menu, openDocumentation
}

// addQuitItem writes the item a person reads as "Quit nocx" and adds it to m.
//
// It is a plain item and not [application.Role]'s Quit on purpose: a role item
// carries the platform's own action, so a click on it never reaches this
// process' decision. This one calls [quitSeam.request], the same decision the
// chord calls.
func (q *quitSeam) addQuitItem(m *application.Menu, name string) *application.MenuItem {
	return m.Add("Quit " + name).
		SetAccelerator(quitChord).
		OnClick(func(*application.Context) { q.request(quitFromMenu) })
}

// ── the client host: what this window IMPLEMENTS for the coordinator ──────
//
// The coordinator has no window (design §1). A native file picker, a browser
// open, a desktop banner, a dock badge and a window raise are things only a
// shell can do, so the coordinator ASKS an attached client for them
// (host.request) and the client answers (host.resolved). The renderer is the
// half that speaks the WebSocket; these bindings are the half that speaks the
// platform, and between them is one hop through the Wails runtime.
//
// NOTHING HERE DECIDES ANYTHING (AD-3). Whether a URL may be opened, whether
// a second picker may stack, which pane a click focuses — every one of those
// is settled on the coordinator's side before the ask is sent. These methods
// perform the effect and report what happened, and their errors are the
// platform's own words.

// attentionActivatedEvent is the Wails event this window emits when a person
// clicks a banner. The renderer forwards it to the coordinator; see
// reportAttentionActivated for why the shell does not act on it itself.
const attentionActivatedEvent = "nocx:attentionActivated"

// HostOpenFile opens the platform file picker and returns the chosen ABSOLUTE
// path, or "" when the person cancelled.
//
// It cannot be dismissed from here — the v3 open dialog has no cancel handle
// once shown — so the call returns only when the person acts. That is the
// contract transport.DialogService has always documented for a
// non-cooperative adapter, and the coordinator's capacity-one waiting gate is
// what keeps a second picker from stacking meanwhile.
func (w *WailsApp) HostOpenFile() (string, error) {
	app := application.Get()
	if app == nil {
		return "", errors.New("no Wails application in this process")
	}
	return app.Dialog.OpenFile().
		CanChooseFiles(true).
		SetTitle("Choose a file").
		AddFilter("All files", "*").
		PromptForSingleSelection()
}

// HostOpenDirectory is the same v3 open dialog restricted to directories:
// CanChooseFiles(false) is what makes a file unselectable, so a caller
// expecting a folder cannot be handed one. Same cancellation contract as
// HostOpenFile.
func (w *WailsApp) HostOpenDirectory() (string, error) {
	app := application.Get()
	if app == nil {
		return "", errors.New("no Wails application in this process")
	}
	return app.Dialog.OpenFile().
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		SetTitle("Choose a folder").
		PromptForSingleSelection()
}

// HostOpenUrl opens a URL in the system browser. The coordinator has already
// refused anything that is not an http(s) URL with a host (ws_openurl.go);
// this side adds no second gate, because a second answer to one question is
// how the two drift apart.
func (w *WailsApp) HostOpenUrl(url string) error {
	app := application.Get()
	if app == nil {
		return errors.New("no Wails application in this process")
	}
	return app.Browser.OpenURL(url)
}

// HostBanner presents one desktop notification banner. Title and body reach
// the OS verbatim; sessionId rides in the notification's payload and comes
// back on a click.
func (w *WailsApp) HostBanner(title, body, sessionID string) error {
	if w.attention == nil {
		return errors.New("this window has no notification surface")
	}
	return w.attention.Banner(context.Background(), notify.Event{
		Title:     title,
		Body:      body,
		SessionID: sessionID,
	})
}

// HostBadge sets the dock badge count; 0 clears it. The Wails host does not
// implement it (nocx-3a40), and it says so loudly rather than pretending to
// have delivered.
func (w *WailsApp) HostBadge(count int) error {
	if w.attention == nil {
		return errors.New("this window has no notification surface")
	}
	return w.attention.Badge(context.Background(), count)
}

// HostBounce requests the attention bounce. Same absence as HostBadge, same
// loud error.
func (w *WailsApp) HostBounce() error {
	if w.attention == nil {
		return errors.New("this window has no notification surface")
	}
	return w.attention.Bounce(context.Background())
}

// HostFocusWindow brings this window to the front. The coordinator asks for
// it when it has decided a click should land here; the shell only raises.
func (w *WailsApp) HostFocusWindow() error {
	if w.window == nil {
		return fmt.Errorf("window %q is gone", mainWindowName)
	}
	w.window.Focus()
	return nil
}

// HostRestoreWindow puts the window back where the person left it, from the
// geometry the renderer just read over the control plane (ADR-0098).
//
// THE SHELL PERFORMS AND DECIDES NOTHING: what the saved geometry means on
// these displays — the clamp, whether the position can be trusted, which
// state to re-enter — is uistate.Restore's, applied through uistate.Apply.
// The renderer calls this once at bootstrap, after the backend has answered
// uistate.window.get; the sampler starts here and only here, so a launch can
// never record its own opening over the geometry being restored. A renderer
// reload calls it again, and the second call is deliberately inert.
func (w *WailsApp) HostRestoreWindow(width, height, x, y int, maximised, fullScreen bool, displays string) error {
	if w.geometry == nil {
		return fmt.Errorf("window %q is gone", mainWindowName)
	}
	w.geometry.restore(uistate.Window{
		Width:      width,
		Height:     height,
		X:          x,
		Y:          y,
		Maximised:  maximised,
		FullScreen: fullScreen,
		Displays:   displays,
	})
	return nil
}

// CheckForUpdate fetches and verifies the signed release manifest.
// Returns an update description if a newer version is available,
// null when already current or on a dev build, and the check's own
// error when the check failed. A failure is a state the renderer
// surfaces (About, nocx-1ebrs.36) — returning null for it made a dead
// check indistinguishable from "no update", which is how 0.6.0 never
// offered anybody 0.6.1.
func (w *WailsApp) CheckForUpdate() (*update.UpdateInfo, error) {
	if w.updater == nil {
		if w.updatePlacementErr != nil {
			return nil, w.updatePlacementErr
		}
		return nil, nil
	}
	info, err := w.updater.Check(context.Background())
	if err != nil {
		w.logger.Warn("update check failed", "error", err)
		return nil, fmt.Errorf("update check failed: %w", err)
	}
	w.updateInfo = info
	return info, nil
}

// SetUpdateChannel selects which signed manifest the next check fetches:
// stable (the default) or nightly (ADR-0097). The renderer calls this with
// the persisted update.channel setting — at startup and whenever the person
// changes it — so the stored value, not a window-side guess, decides the
// channel. An unknown channel is an error rather than a fallback: the two
// there are were decided, and a third spelling is a bug to surface.
func (w *WailsApp) SetUpdateChannel(channel string) error {
	if w.updateFetch == nil {
		return errors.New("the updater is not available in this window")
	}
	c, err := update.ParseChannel(channel)
	if err != nil {
		return err
	}
	w.updateFetch.SetChannel(c)
	w.logger.Info("update channel selected", "channel", c.String())
	return nil
}

// ApplyUpdate applies a previously checked update. No arguments —
// the update info is already verified and held in window state.
func (w *WailsApp) ApplyUpdate() error {
	if w.updater == nil {
		if w.updatePlacementErr != nil {
			return w.updatePlacementErr
		}
		return errors.New("the updater is not available in this window")
	}
	if w.updateInfo == nil {
		return errors.New("no update available — call CheckForUpdate first")
	}
	return w.updater.Apply(context.Background(), w.updateInfo)
}

// ReportHealthy signals that the frontend is running correctly.
// Called once the initial tab's renderer mounted and its PTY session
// opened (§7.5). Only then does the updater finalise a pending update.
//
// It certifies a PAIR: the updater asks the launch probe which coordinator
// answered this window, and finalises only when that backend is the version
// this update installed (D4). A mixed pair — the old daemon surviving the
// bundle swap — is refused here and the rollback journal is left intact.
func (w *WailsApp) ReportHealthy() error {
	if w.updater == nil {
		return errors.New("the updater is not available in this window")
	}
	return w.updater.ReportHealthy(context.Background())
}

// GetUpdateState returns what a person can be told about an update in
// flight: nothing (the ordinary launch), a pending restart, or the held
// state ADR-0101 defines — a protocol-compatible OLDER coordinator is
// still serving a healthy window, the update waits for it to turn over,
// and the surface offers to finish it with a full restart, never a force.
//
// Reconcile runs first so a journal that the launch is about to settle is
// not read as pending work.
func (w *WailsApp) GetUpdateState() update.UpdateState {
	if w.updater == nil {
		return update.UpdateState{}
	}
	_ = w.updater.Reconcile(context.Background())
	return w.updater.State(context.Background())
}

// FinishPendingUpdate finishes a held update on the person's word: it stops
// the kept coordinator — every session it is running ends, which is what
// the button says — and quits, so the next start raises the new build's
// coordinator and the health report certifies the pair.
//
// It REFUSES unless the updater holds exactly the ADR-0101 state: a
// pending transaction held by an older compatible coordinator. A pending
// update nothing is holding needs an ordinary restart, not a killer; and
// an update in any other state has its own remedy. The refusal is the
// never-force half of the decision — the only path to a stop is a person
// pressing the button this method exists behind.
func (w *WailsApp) FinishPendingUpdate() error {
	if w.updater == nil {
		return errors.New("the updater is not available in this window")
	}
	if w.quit == nil {
		return errors.New("this window cannot quit the application")
	}
	ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
	defer cancel()

	state := w.updater.State(ctx)
	if !state.Pending || !state.HeldByOlderCoordinator {
		return fmt.Errorf("no update is waiting for a coordinator restart — the update state is %+v", state)
	}

	stop := w.finishStopKeptCoordinatorFn
	if stop == nil {
		stop = w.stopKeptCoordinator
	}
	stopped, err := stop(ctx)
	if err != nil {
		return fmt.Errorf("stopping the kept coordinator: %w", err)
	}
	w.logger.Info("finishing a held update",
		"version", state.Version,
		"stoppedCoordinator", stopped,
	)
	// The quit is the same decision the chord and the menu item reach
	// (ADR-0093), named as its own cause: a person reading the log can
	// tell an update-finish from a key press.
	w.quit.request(quitFromUpdateFinish)
	return nil
}

// stopKeptCoordinator stops the older coordinator a held update is waiting
// on — and nothing else. A sighting that already answers THIS build's
// version has turned over on its own (a stop, a reboot): stopping it would
// end fresh sessions for nothing, and the next ordinary start certifies
// the update without this window doing anything. No coordinator at all is
// the same case one step further on.
func (w *WailsApp) stopKeptCoordinator(ctx context.Context) (bool, error) {
	paths, err := storage.NewAppPaths()
	if err != nil {
		return false, fmt.Errorf("resolving the profile directories: %w", err)
	}
	dir := coordinator.RuntimeDir(paths)
	client, err := coordinator.NewClient(coordinator.ClientConfig{
		Socket: coordinator.SocketPathIn(dir),
		Self: coordinator.ClientIdentity{
			Version:  version.Version,
			Commit:   version.Commit,
			Protocol: coordinator.ProtocolVersion,
		},
		Dialer: coordinator.SystemDialer{},
		Logger: w.logger,
	})
	if err != nil {
		return false, err
	}
	sighting, err := client.Hello(ctx)
	switch {
	case errors.Is(err, coordinator.ErrNoCoordinator):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("asking the discovery socket who is serving: %w", err)
	}
	if sighting.Hello.Build.Version == version.Version {
		w.logger.Info("the held update's coordinator has already turned over; nothing to stop",
			"pid", sighting.PID, "version", sighting.Hello.Build.Version)
		return false, nil
	}
	if err := coordinator.NewSignalStopper(coordinator.SignalStopperConfig{Logger: w.logger}).Stop(ctx, sighting); err != nil {
		return false, err
	}
	return true, nil
}

// newWindowUpdater is the single owner of desktop update placement. Restart
// retains this destination rather than deriving it from the running copy.
func newWindowUpdater(executable, stateDir string, cfg update.UpdaterConfig) (update.Updater, string, error) {
	// THE PROBE IS NOT OPTIONAL, and refusing here is what stops a wiring
	// regression from being invisible (nocx-1ebrs.66.1.1). Without it the
	// updater cannot certify the pair after an exchange, so every update would
	// download, swap and then roll back — and the only trace would be a log
	// line. This call exists because the dead-code ratchet found exactly that:
	// a change removed `w.probe = coordinator.NewLaunchProbe()` from the
	// startup path, nothing failed, and only CI's reachability check noticed
	// that no caller was left.
	if cfg.Coordinator == nil {
		return nil, "", errors.New("the window updater needs the launch probe: without it no update can ever be certified")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.From(context.Background()).Warn("update home resolution failed", "error", err)
		return nil, "", errors.New("cannot find your home folder — check your account settings and try again")
	}
	target, err := update.ResolveInstallTarget(runtime.GOOS, executable, os.Getenv("APPIMAGE"), home, func(dir string) error {
		probeErr := update.ProbeInstallDirectory(dir)
		if probeErr != nil {
			log.From(context.Background()).Warn("update install directory probe failed", "path", dir, "error", probeErr)
		}
		return probeErr
	})
	if err != nil {
		return nil, "", err
	}
	cfg.InstallPath, cfg.StateDir = target, stateDir
	return update.NewUpdater(cfg), target, nil
}
