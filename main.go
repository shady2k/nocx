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

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/log"
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
	// Whatever the launcher said a person must be told, said. After the
	// startup path rather than inside it, for the same main-thread reason as
	// the fatal dialog above.
	go w.showNotices()
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
