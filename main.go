package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/shady2k/nocx/internal/app"
	"github.com/shady2k/nocx/internal/update"
	"github.com/shady2k/nocx/internal/version"
)

//go:embed all:frontend/dist
var assets embed.FS

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

	err = wails.Run(&options.App{
		Title:     "nocx",
		Width:     1024,
		Height:    768,
		MinWidth:  640,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		EnableDefaultContextMenu: false,
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
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHidden(),
		},
		Debug: options.Debug{
			OpenInspectorOnStartup: true,
		},
		OnStartup:  wailsApp.startup,
		OnShutdown: wailsApp.shutdown,
		Bind: []interface{}{
			wailsApp,
		},
	})
	if err != nil {
		slog.Error("application error", "error", err)
		os.Exit(1)
	}
}

type WailsApp struct {
	backend *app.App
	ctx     context.Context

	// updateInfo holds the most recent Check result. Apply takes no
	// arguments — it applies the update that Check already verified.
	updateInfo *update.UpdateInfo
}

func (w *WailsApp) startup(ctx context.Context) {
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

	// Wire the updater with the real install path and platform.
	w.backend.Updater = update.NewUpdater(update.UpdaterConfig{
		Platform:       update.NewPlatform(),
		Fetcher:        update.NewGitHubManifestFetcher(nil),
		Keyring:        nil, // populated by release pipeline via ldflags
		CurrentVersion: version.Version,
		InstallPath:    installPath,
		Logger:         w.backend.Logger,
	})

	// Settle any transaction in flight from a previous launch.
	if err := w.backend.Updater.Reconcile(ctx); err != nil {
		w.backend.Logger.Warn("update reconcile at startup failed", "error", err)
	}

	if err := w.backend.Start(ctx); err != nil {
		w.backend.Logger.Error("failed to start backend", "error", err)
	}
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

func (w *WailsApp) shutdown(ctx context.Context) {
	w.backend.Logger.Info("Wails app shutting down")
	w.backend.Shutdown(ctx)
}

func (w *WailsApp) GetWSPort() int {
	return w.backend.WSPort()
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
