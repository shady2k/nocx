package main

// THE WINDOW'S OWN LOG HAS A FILE (nocx-vh2hr.1.12).
//
// The window process runs the in-app update: CheckForUpdate, ApplyUpdate,
// RestartWindow and ReportHealthy are its bindings, and the updater behind
// them logs through this process. Until this file existed that log reached
// os.Stderr only. An app started from Finder has no stderr anyone keeps, so
// the owner's failed update left one red toast — "The update could not
// finish" — and no line, anywhere, saying which step refused or why
// (nocx-vh2hr.1.6). The backend's nocx.log is no substitute: it is opened by
// nocx-server, a different process, and holds no update line at all.
//
// ONE OWNER PER FILE. The backend owns nocx.log; this process owns
// nocx-window.log beside it. They are deliberately not merged: internal/app
// opens its file once for the one coordinator, and two processes appending to
// it would make the backend's own level decision and its first line
// ("backend log file") ambiguous. Window instances — Linux can run two, macOS
// can with `open -n` — DO share the window file; that is the arrangement
// cmd/nocx-helper already declares for helper.log, where several generations
// serve at once. O_APPEND with one Write per record keeps the lines whole, and
// one file is what a person can actually read.
//
// The rules are internal/log's, shared with the backend: ConfiguredLevel,
// HandlerOptions (AddSource, and Sensitive's build-tag redaction at write
// time) and OpenFile (0700 directory, 0600 append). The product has no
// rotation or size bound on any log today; this file does not invent a
// second policy for itself.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// windowLogName is the window's file. A sibling of the backend's nocx.log,
// never the same file: see the file doc above.
const windowLogName = "nocx-window.log"

// windowLogPath is where a window with this data directory writes.
func windowLogPath(dataDir string) string { return filepath.Join(dataDir, windowLogName) }

// at is the context-carrying view of a process logger.
//
// log.From is the product's one path for a log line (internal/log/context.go),
// and the ratchet that enforces it cannot see a raw *slog.Logger call as
// carrying anything. For the lines this file and the window's update bindings
// add, the logger is not yet in a context, so bind it to one first.
func at(lg *slog.Logger) log.Logger {
	return log.From(log.WithLogger(context.Background(), log.NewSlogAdapter(lg)))
}

// openWindowLog resolves the profile and opens the window's file. It never
// fails the window: a log that cannot be opened is said on stderr and the
// app starts, because a diagnostic must not cost more than the thing it
// diagnoses. The returned file is nil when only stderr is in use.
func openWindowLog() (*slog.Logger, *os.File) {
	paths, err := storage.NewAppPaths()
	if err != nil {
		level, _ := log.ConfiguredLevel(os.Getenv(log.LevelEnvVar))
		lg := slog.New(slog.NewTextHandler(os.Stderr, log.HandlerOptions(level)))
		at(lg).Warn("window log file unavailable; logging to stderr only", "error", err)
		return lg, nil
	}
	logger, file, _ := newWindowLogger(paths.DataDir())
	return logger, file
}

// newWindowLogger builds the logger for one data directory. It is the seam a
// test drives without a running Wails app.
func newWindowLogger(dataDir string) (*slog.Logger, *os.File, string) {
	level, badLevel := log.ConfiguredLevel(os.Getenv(log.LevelEnvVar))
	opts := log.HandlerOptions(level)
	path := windowLogPath(dataDir)
	stderrOnly := slog.New(slog.NewTextHandler(os.Stderr, opts))
	file, err := log.OpenFile(path)
	if err != nil {
		at(stderrOnly).Warn("window log file unavailable; logging to stderr only",
			"path", path, "error", err)
		return stderrOnly, nil, ""
	}
	// Keep stderr AND the file, for the same reason the backend does: the
	// proof captures stderr, and a person's own launch keeps the file.
	logger := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, file), opts))
	// The log names itself first, so a running window can say where its file
	// is by reading its own first line — the promise the backend's and the
	// helper's logs already make.
	at(logger).Info("window log file", "path", path)
	if badLevel != "" {
		at(logger).Warn("unrecognised log level; using the build's default",
			"default", level.String(), "var", log.LevelEnvVar, "value", badLevel,
			"known", "debug, info, warn, error")
	}
	return logger, file, path
}

// installProcessLogger makes lg the process's logger for both routes a line
// can take — slog.Default (what NewSlogAdapter(nil) and any bare slog call
// reaches) and log.From's root (what context-less updater lines reach). The
// returned function puts the previous pair back, so a test can own the
// process for one case without leaking its logger into the next.
func installProcessLogger(lg *slog.Logger) func() {
	previousRoot := log.Root()
	previousDefault := slog.Default()
	log.SetRoot(log.NewSlogAdapter(lg))
	slog.SetDefault(lg)
	return func() {
		log.SetRoot(previousRoot)
		slog.SetDefault(previousDefault)
	}
}
