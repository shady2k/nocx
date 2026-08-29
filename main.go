package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/shady2k/nocx/internal/coordinator"
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

// launchTimeout bounds the whole find-or-raise-the-coordinator sequence,
// including a spawn and its readiness wait. It is the outer bound on how
// long a window can sit blank before it either has a backend or says why it
// has not: never a hang, always an answer (design §4).
const launchTimeout = 45 * time.Second

func main() {
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

	// The Wails v3 shell. The window is created before Run; on Linux the
	// platform defers actually loading the webview until activation inside
	// Run, which happens after ServiceStartup — so the frontend's first
	// binding calls (GetWSPort/GetWSToken) resolve after the launcher has
	// found the coordinator, preserving the ordering this composition root
	// has always relied on.
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

	// THE SIZE THE WINDOW OPENS AT. Wails wants it before there is a window.
	// The saved geometry lives in the UI-state document, which the
	// COORDINATOR owns now — this process has no store to read and must not
	// open a second one over the daemon's (ADR-0043 is the same argument one
	// level up). So the opening pass asks the rule for its default with
	// nothing saved, which is what it has always answered for a first launch,
	// and restoring the real geometry moves to the client host with the rest
	// of the native-window surface (design D3, A1.2).
	opening := uistate.Restore(uistate.Window{}, nil)

	window := shell.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       mainWindowName,
		Title:                      "nocx",
		Width:                      opening.Width,
		Height:                     opening.Height,
		MinWidth:                   uistate.MinWindowWidth,
		MinHeight:                  uistate.MinWindowHeight,
		DefaultContextMenuDisabled: true,
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
	window.Show()
	wailsApp.window = window

	if err := shell.Run(); err != nil {
		logger.Error("application error", "error", err)
		os.Exit(1)
	}
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
	logger *slog.Logger
	window *application.WebviewWindow

	// ws is what the launcher learned from the coordinator: the loopback
	// address and the token the renderer needs. Written once, in
	// ServiceStartup, before the webview loads.
	ws coordinator.Hello

	// notices holds what the launcher said a person must be told — today
	// only that an incompatible coordinator was replaced and its sessions
	// died (D4). Shown as a dialog once the shell can raise one.
	notices noticeRecorder

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
}

// Log logs a message from the frontend.
//
// It reaches this process's stderr rather than the backend's log file: the
// renderer is this window's, and the file belongs to the daemon now.
func (w *WailsApp) Log(message string) {
	w.logger.Info("frontend", "message", message)
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
	keyring, err := update.ReleaseKeyring()
	if err != nil {
		w.logger.Error("release keyring unusable; updates cannot be verified on this build", "error", err)
	}
	execPath, err := os.Executable()
	if err != nil {
		w.logger.Warn("cannot determine executable path", "error", err)
	}
	// The pair probe (D4). This window's backend is another process, so
	// the only honest answer to "which coordinator is serving me" is the
	// one the discovery handshake gives — and that answer does not exist
	// yet, because the updater has to Reconcile before the launcher runs
	// (see coordinator.LaunchProbe). So it is wired empty here and
	// attached below. Until it is attached it refuses to certify, which is
	// the direction to fail in.
	w.probe = coordinator.NewLaunchProbe()
	w.updater = update.NewUpdater(update.UpdaterConfig{
		Platform:       update.NewPlatform(),
		Fetcher:        update.NewGitHubManifestFetcher(nil),
		Keyring:        keyring,
		CurrentVersion: version.Version,
		InstallPath:    upgradeInstallPath(execPath),
		Coordinator:    w.probe,
		Logger:         log.NewSlogAdapter(w.logger),
	})
	// Settle any transaction in flight from a previous launch.
	if reconcileErr := w.updater.Reconcile(ctx); reconcileErr != nil {
		w.logger.Warn("update reconcile at startup failed", "error", reconcileErr)
	}

	launchCtx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	launch, err := w.launchCoordinator(launchCtx, execPath)
	if err != nil {
		// A window with no backend is not a window: say why, in a dialog a
		// person can read, and then quit. Asynchronously, because the dialog
		// is dispatched onto the main thread and this hook is running before
		// the event loop that would service it.
		w.logger.Error("nocx cannot reach its backend", "error", err)
		go w.fatal("nocx cannot start", err.Error())
		return nil
	}
	w.ws = launch.Hello
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

	// Whatever the launcher said a person must be told, said. After the
	// startup path rather than inside it, for the same main-thread reason as
	// the fatal dialog above.
	go w.showNotices()
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
	if w.notifications != nil {
		return w.notifications.ServiceShutdown()
	}
	return nil
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
		return coordinator.Launch{}, fmt.Errorf("resolving the profile directories: %w", err)
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
		return coordinator.Launch{}, fmt.Errorf("installing the coordinator binary: %w", err)
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

// showNotices raises one dialog per notice.
//
// A DIALOG, not a log line and not a toast. D4 permits A1 to kill an
// incompatible coordinator and lose its sessions, and requires that the loss
// be said out loud; a person who has just watched their SSH connections die
// has to be told that is what happened, and a message that can be missed is
// not telling them.
func (w *WailsApp) showNotices() {
	for _, n := range w.notices.drain() {
		w.logger.Warn("nocx: telling the user what the launcher did",
			"kind", string(n.Kind), "message", n.Message)
		app := application.Get()
		if app == nil {
			return
		}
		app.Dialog.Warning().
			SetTitle("The nocx backend was replaced").
			SetMessage(n.Message).
			Show()
	}
}

// fatal shows a message and ends the process. Used only for a startup that
// cannot produce a working window.
func (w *WailsApp) fatal(title, message string) {
	app := application.Get()
	if app == nil {
		os.Exit(1)
	}
	app.Dialog.Error().SetTitle(title).SetMessage(message).Show()
	app.Quit()
}

func (w *WailsApp) shutdown() {
	// The daemon is NOT stopped here. It outliving this window is the whole
	// point of moving it out (design §1); when the last client detaches it
	// ends itself after a grace period, which is nocx-server's decision and
	// not a window's.
	w.logger.Info("nocx window shutting down")
}

// GetWSPort reports the port the coordinator's WebSocket is listening on.
//
// The renderer has always been given a port and connects to loopback, so the
// binding keeps its name and its signature and only its SOURCE has changed:
// the address now comes from the discovery socket rather than from a server
// this process started. 0 means the launcher never got one, and the fatal
// dialog above is already on screen saying why.
func (w *WailsApp) GetWSPort() int {
	if w.ws.WSAddress == "" {
		return 0
	}
	_, port, err := net.SplitHostPort(w.ws.WSAddress)
	if err != nil {
		w.logger.Error("the coordinator reported an address with no port", "wsAddress", w.ws.WSAddress, "error", err)
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		w.logger.Error("the coordinator reported a non-numeric port", "wsAddress", w.ws.WSAddress, "error", err)
		return 0
	}
	return n
}

// GetWSToken reports the capability that opens that WebSocket.
//
// It reaches this process over the discovery socket and leaves it only
// through this binding — never a log line, never argv, never the spawned
// daemon's environment (design §6).
func (w *WailsApp) GetWSToken() string {
	return w.ws.WSToken
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

// CheckForUpdate fetches and verifies the signed release manifest.
// Returns an update description if a newer version is available,
// or null when already current or on a dev build.
func (w *WailsApp) CheckForUpdate() *update.UpdateInfo {
	if w.updater == nil {
		return nil
	}
	info, err := w.updater.Check(context.Background())
	if err != nil {
		w.logger.Warn("update check failed", "error", err)
		return nil
	}
	w.updateInfo = info
	return info
}

// ApplyUpdate applies a previously checked update. No arguments —
// the update info is already verified and held in window state.
func (w *WailsApp) ApplyUpdate() error {
	if w.updater == nil {
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

// GetUpdateState returns the updater state for the UI notice.
// "pending" means an update was applied and is waiting for a restart;
// empty string means nothing in flight.
func (w *WailsApp) GetUpdateState() string {
	if w.updater == nil {
		return ""
	}
	// Reconcile at startup to settle any in-flight transaction.
	_ = w.updater.Reconcile(context.Background())
	// For now, return empty — the actual state detection will be
	// refined once Reconcile returns a richer status.
	return ""
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
