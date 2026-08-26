// Command devharness runs the nocx backend (WS transport + real PTY) without
// the Wails GUI, so a headless browser (Playwright) can drive the real frontend
// on a machine with no display / WebKitGTK. Dev-only; never shipped.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
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
	// The envelope measurement (nocx-d6gn4.9), printed on the way out so a
	// dev stand can be driven by hand — ask the assistant things, then stop
	// it and read what the ledger says about how DEEP the dependent chains
	// were. Off unless asked for: an ordinary e2e run has no use for it.
	if os.Getenv("NOCX_ENVELOPE_REPORT") == "1" {
		printEnvelopeReport(ctx, a)
	}
	a.Shutdown(ctx)
}

// printEnvelopeReport writes one line per assistant run, in the same
// grep-friendly shape as the WSPORT/WSTOKEN lines above. Depth is the figure
// the program-carrier experiment turns on: it is the LONGEST CHAIN of
// dependent calls, never the call count, and a run of six independent calls
// is a depth of one.
func printEnvelopeReport(ctx context.Context, a *app.App) {
	runs, truncated, err := a.MeasureAgentRuns(ctx)
	if err != nil {
		fmt.Printf("ENVELOPE_ERROR=%v\n", err)
		return
	}
	if len(runs) == 0 {
		// Said explicitly: "nothing recorded" and "recorded nothing to
		// report" are different, and a silent exit reads as the second.
		fmt.Println("ENVELOPE_RUNS=0 (no assistant tool calls carrying an envelope are recorded)")
		return
	}
	for _, r := range runs {
		fmt.Printf("ENVELOPE run=%s invocations=%d depth=%d edges=%d candidates=%d approvals=%d descriptors=%s\n",
			r.RunID, r.Invocations, r.MaxDependencyDepth, r.Edges, r.Candidates,
			r.ApprovalsAsked, strings.Join(r.Descriptors, ","))
	}
	if truncated {
		fmt.Println("ENVELOPE_TRUNCATED=1 (the read hit its limit; these are the most recent entries, not all of them)")
	}
	_ = os.Stdout.Sync()
}
