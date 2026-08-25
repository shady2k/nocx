// Package reveal owns the OS file-manager reveal (files.reveal,
// nocx-ngf3u). One behaviour, three platform answers, behind an
// interface chosen at the composition root — the same shape
// internal/contentkey uses for the per-OS key-identity question.
//
// macOS: `open -R <path>` reveals the file in Finder.
// Linux: `xdg-open <dir>` opens the containing directory in whatever
//
//	file manager the desktop registers. There is no cross-desktop
//	"reveal this file" standard, so opening the parent directory is
//	the best the platform offers. When xdg-open is absent the revealer
//	refuses by name — never a silent success.
//
// Other platforms: the constructor returns nil, the transport answers
//
//	-32601, and the menu item does not render.
package reveal

import (
	"errors"
	"os/exec"
)

// ErrRevealUnavailable is returned when this platform has no file-manager
// reveal at all (the unsupported-platform case). It is distinct from a
// platform that has one but cannot reach it on this machine.
var ErrRevealUnavailable = errors.New("file reveal is not available on this platform")

// ErrNoFileManager is returned when the platform's file-manager opener
// (xdg-open on Linux) is not on PATH. It names the situation rather than
// returning a generic "not found" that a surface would render as a
// broken diagnostic.
var ErrNoFileManager = errors.New("no file manager found (xdg-open is not installed)")

// commandRunner runs an external command and returns its combined output
// and error. Injected so tests exercise the seam without a real binary.
type commandRunner func(name string, args ...string) ([]byte, error)

// realRunner runs the command via os/exec.
func realRunner() commandRunner {
	return func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
}

// Revealer shows a path in the OS file manager. It satisfies
// transport.FilesRevealer without importing transport — the
// composition root passes it to WithFilesRevealer.
type Revealer struct {
	run commandRunner
}

// New builds the platform-specific revealer, or returns nil with
// ErrRevealUnavailable on a platform nocx does not ship a reveal for.
// The constructor is the composition root's single decision point —
// the same shape contentkey uses.
func New() (*Revealer, error) {
	return newRevealer()
}

// wrapRunErr annotates a command-runner failure with the command name
// and its combined output, so the surface that renders it says which
// program failed and what it printed. The output is trimmed of
// trailing whitespace for readability.
func wrapRunErr(name string, out []byte, err error) error {
	msg := name + ": " + err.Error()
	if len(out) > 0 {
		msg += ": " + string(out)
	}
	return errors.New(msg)
}
