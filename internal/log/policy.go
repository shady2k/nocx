package log

// THE RULES EVERY LOG FILE IN THIS PRODUCT IS OPENED AND WRITTEN BY
// (nocx-vh2hr.1.12).
//
// The window process had no file at all: main.go built a slog handler on
// os.Stderr, so an app launched from Finder kept nothing, and the update
// failure behind "The update could not finish" reached no log a person could
// read. The backend had a file, but it opened it in its own constructor, with
// its own level switch beside it. Two copies of "which level, which flags,
// which permissions" is exactly the pair that drifts, and a second logger is
// where a redaction rule quietly stops applying.
//
// So the policy lives here once, and internal/app and the window both ask for
// it. AddSource is part of the policy rather than a preference: without it a
// record names the adapter that forwarded it (see log.go's file doc), and the
// line that mattered cannot be found again.
import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// LevelEnvVar turns a build's log level up without a rebuild. Read by the
// backend and by the window, so one variable names one answer.
const LevelEnvVar = "NOCX_LOG_LEVEL"

// ConfiguredLevel resolves a raw NOCX_LOG_LEVEL value against the build's
// default. It returns the level to use and the spelling it could not
// recognise (empty when the value was understood). An unrecognised value
// falls back to the build's default rather than failing: a mistyped level
// must never stop the app starting, and the caller says so in its first
// lines.
func ConfiguredLevel(raw string) (slog.Level, string) {
	level := DefaultLevel()
	switch name := strings.ToLower(strings.TrimSpace(raw)); name {
	case "":
		return level, ""
	case "debug":
		return slog.LevelDebug, ""
	case "info":
		// SET, not skipped. It was a bare case while the default was info, and
		// leaving it bare would make NOCX_LOG_LEVEL=info a no-op on the build
		// whose default is debug — the one build somebody would type it on
		// (nocx-4l2a5.2).
		return slog.LevelInfo, ""
	case "warn", "warning":
		return slog.LevelWarn, ""
	case "error":
		return slog.LevelError, ""
	default:
		return level, name
	}
}

// HandlerOptions is the one handler configuration: the resolved level, and a
// source on every record. Values marked Sensitive are rendered by the handler
// at write time (sensitive.go), so any handler built here carries the build
// tag's redaction rule whether it writes to stderr or to a file.
func HandlerOptions(level slog.Level) *slog.HandlerOptions {
	return &slog.HandlerOptions{Level: level, AddSource: true}
}

// OpenFile opens a log file the way every process in this product does:
// the directory is created 0700, the file is appended to and readable by its
// owner only. Append is deliberate — one writer per file is the rule (the
// backend owns nocx.log, the window owns nocx-window.log), and the window's
// several instances share the window's file exactly as the helper daemon's
// generations share helper.log (cmd/nocx-helper/servelog.go).
//
// There is no rotation or size bound anywhere in this product today — the
// backend appends and nothing truncates it. This helper does not invent a
// second policy; if one is added it is added here, for every writer.
func OpenFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// #nosec G304 — the path is the app data dir plus a fixed name, or a
	// test override; never external input.
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
