package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/shady2k/nocx/internal/app"
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
}

func (w *WailsApp) startup(ctx context.Context) {
	w.ctx = ctx
	w.backend.Logger.Info("Wails app starting up")
	if err := w.backend.Start(ctx); err != nil {
		w.backend.Logger.Error("failed to start backend", "error", err)
	}
}

func (w *WailsApp) shutdown(ctx context.Context) {
	w.backend.Logger.Info("Wails app shutting down")
	w.backend.Shutdown(ctx)
}

func (w *WailsApp) GetWSPort() int {
	return w.backend.WSPort()
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
