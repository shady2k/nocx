package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	nocxlog "github.com/shady2k/nocx/internal/log"
)

// THE DAEMON'S LOG HAS A SINK (nocx-n14oo.1).
//
// It had none. cmd/nocx-helper logs to os.Stderr, which is right for the
// BRIDGE — there stdout is the ssh channel and D22 puts every diagnostic on
// stderr — and wrong for the daemon, which nobody is watching:
// internal/helper/endpoint/bridge.go starts it with
// `cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil`, and os/exec makes those
// /dev/null. Verified on this machine 2026-09-10: all three running helpers had
// `/proc/<pid>/fd/2 -> /dev/null`.
//
// This is the process that opens every local pane and launches its shell. With
// no sink, the pane launch is a black box: a spawn whose shell never
// authenticates produced a hello-timeout in the backend and not one word
// anywhere about what was actually started.
//
// ONE FILE FOR EVERY GENERATION, not one per. A helper install is
// content-addressed and several generations serve at once — three were running
// here — and a person asking "why did my pane fail" does not know which one
// served it. The generation is on every line instead, which is the same fact
// without the search.
//
// It lives under the helper's own directory and not in the app's profile,
// because a generation serves whichever backend asked for it: a dev stand and a
// shipped app reach the same daemon, and putting its log in either one's
// profile would make the other's absent.
const serveLogName = "helper.log"

// ServeLogPath is where a running `nocx-helper serve` writes. Exported shape
// kept next to the daemon rather than in internal/helper/endpoint, because it
// is a fact about the BINARY's behaviour and not about the endpoint protocol.
func ServeLogPath(home string) string {
	return filepath.Join(home, ".nocx", serveLogName)
}

// openServeLog builds the daemon's logger and returns the file to close with
// it. It never fails the daemon: a helper that cannot open its log still
// serves, and says so on the stderr it may or may not have — refusing to open a
// pane because a log file could not be created would be the diagnostic costing
// more than the thing it diagnoses.
func openServeLog(home string, generation string) (*slog.Logger, io.Closer) {
	opts := &slog.HandlerOptions{Level: nocxlog.DefaultLevel(), AddSource: true}
	stderrOnly := slog.New(slog.NewTextHandler(os.Stderr, opts)).With("generation", generation)

	path := ServeLogPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		stderrOnly.Warn("helper log file unavailable; logging to stderr only", "path", path, "err", err)
		return stderrOnly, nil
	}
	// #nosec G304 — the path is the account's home plus a fixed name.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		stderrOnly.Warn("helper log file unavailable; logging to stderr only", "path", path, "err", err)
		return stderrOnly, nil
	}
	// Stderr AND the file: the bridge's stderr is a real destination and the
	// daemon's is /dev/null, and one logger that is correct in both cases
	// beats two that each know where they are.
	lg := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, file), opts)).With("generation", generation)
	// The log names itself first, so a running daemon can say where its file
	// is by reading its own first line — the same promise the backend's makes.
	lg.Info("helper log file", "path", path)
	return lg, file
}
