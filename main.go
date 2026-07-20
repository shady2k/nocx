package main

import (
	"context"
	"embed"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/shady2k/nocx/internal/app"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
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
		return
	}
	wailsRuntime.EventsEmit(ctx, "ws:port", w.backend.WSPort())
}

func (w *WailsApp) shutdown(ctx context.Context) {
	w.backend.Logger.Info("Wails app shutting down")
	w.backend.Shutdown(ctx)
}
