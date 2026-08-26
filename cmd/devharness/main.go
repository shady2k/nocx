// Command devharness runs the nocx backend (WS transport + real PTY) without
// the Wails GUI, so a headless browser (Playwright) can drive the real frontend
// on a machine with no display / WebKitGTK. Dev-only; never shipped.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/shady2k/nocx/internal/app"
)

func main() {
	opts := []app.Option{}
	if addr := os.Getenv("NOCX_WS_ADDR"); addr != "" {
		opts = append(opts, app.WithWSAddr(addr))
	}
	// The e2e cases that are ABOUT the passphrase path say so here. Portable,
	// unlike DBUS_SESSION_BUS_ADDRESS, which only means anything on Linux —
	// on macOS the backend went to the real Security framework instead, and
	// with a disposable $HOME that raised a "Keychain not found" dialog on the
	// developer's own screen once per start (nocx-o4hg).
	if os.Getenv("NOCX_NO_SYSTEM_KEYSTORE") == "1" {
		opts = append(opts, app.WithoutSystemKeystore())
	}
	a, err := app.New(opts...)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx); err != nil {
		panic(err)
	}
	// Machine-readable lines the runner greps for.
	fmt.Printf("WSPORT=%d\n", a.WSPort())
	fmt.Printf("WSTOKEN=%s\n", a.WSToken())
	// Where the backend log file lives — the dev stand says it, so the
	// log is found instead of hunted for (the P0 that was diagnosed from
	// a JSON file's mtime).
	fmt.Printf("LOGFILE=%s\n", a.LogFilePath())
	_ = os.Stdout.Sync()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	a.Shutdown(ctx)
}
