package pty

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/sandbox"
)

type LocalPty struct {
	log    log.Logger
	cmd    *exec.Cmd
	file   *os.File
	mu     sync.Mutex
	done   chan struct{}
	closed bool
	// prepared is set only for sandboxed sessions: it owns the enforced
	// process, the readiness handshake, and the runtime-tree cleanup.
	prepared *sandbox.PreparedCommand
}

// sandboxReadyTimeout bounds the post-start enforcement handshake (design
// spec §8.3): a helper that never reports readiness fails closed.
const sandboxReadyTimeout = 30 * time.Second

// localeVars are checked in POSIX precedence order; any one of them present
// means the environment already states a locale.
var localeVars = []string{"LC_ALL=", "LC_CTYPE=", "LANG="}

// launcherSessionVars identify the SESSION that launched nocx, not the user's
// environment. A terminal hands out shells; it must not hand out its
// launcher's identity with them. When nocx is started from inside a coding
// agent — which is exactly how it gets developed — every shell it spawns
// inherited that agent's session markers, and a `claude` run in a tab saw
// CLAUDE_CODE_CHILD_SESSION and silently disabled transcript saving.
//
// Deliberately a precise list rather than a CLAUDE* wildcard: stripping
// something like an API key would break the very tool we are trying to fix.
// It grows as other launchers are found.
//
// NO_COLOR= and TERM= belong to the same class of leak: coding agents run
// nocx's dev harness with TERM=dumb / NO_COLOR=1 in their tool environment,
// and every spawned shell then tells its TUIs "no colors here" — claude
// renders black-and-white. A terminal emulator declares color capability
// itself (TERM=xterm-256color + COLORTERM=truecolor are appended below);
// the launcher's opinion must not leak into the PTY.
var launcherSessionVars = []string{
	"CLAUDECODE=",
	"CLAUDE_CODE_ENTRYPOINT=",
	"CLAUDE_CODE_EXECPATH=",
	"CLAUDE_CODE_SESSION_ID=",
	"CLAUDE_CODE_CHILD_SESSION=",
	"CLAUDE_PID=",
	"CLAUDE_EFFORT=",
	"NO_COLOR=",
	"TERM=",
}

func scrubLauncherSession(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, prefix := range launcherSessionVars {
			if strings.HasPrefix(kv, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// withUTF8Locale guarantees the child shell knows it is on a UTF-8 terminal.
// A GUI app launched from Finder or the Dock inherits none of the shell's
// environment, so without this the shell has no locale, and any Python/Rich
// TUI downstream encodes its output with errors="replace" — turning every
// non-ASCII glyph into a literal '?'. That failure is invisible when launched
// from a terminal, where LANG is inherited, and it masquerades as a font bug.
// Only fills a gap: an inherited locale, UTF-8 or not, is left alone.
func withUTF8Locale(env []string) []string {
	for _, kv := range env {
		for _, prefix := range localeVars {
			if strings.HasPrefix(kv, prefix) {
				return env
			}
		}
	}
	return append(env, "LANG=en_US.UTF-8")
}

// resolveCwd picks where the shell starts. A GUI app launched from Finder or
// the Dock inherits "/" as its working directory, which is useless as a
// starting point and useless as a tab name, so an unset Cwd falls back to the
// user's home the way Terminal.app and iTerm do.
func resolveCwd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func NewLocal(logger log.Logger, cfg Config, opts ...Option) (*LocalPty, error) {
	for _, opt := range opts {
		opt(&cfg)
	}

	// Prefer bash for shell integration (OSC 133 markers).  Fall back through
	// common paths; on stripped-down containers none may exist, so keep /bin/sh
	// as the last resort.
	shell := os.Getenv("SHELL")
	if shell == "" {
		for _, candidate := range []string{
			"/run/current-system/sw/bin/bash", // NixOS
			"/bin/bash",
			"/usr/bin/bash",
			"/usr/local/bin/bash",
		} {
			if _, err := os.Stat(candidate); err == nil {
				shell = candidate
				break
			}
		}
	}
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, "-i") //nolint:gosec // shell is from detected path
	cmd.Dir = resolveCwd(cfg.Cwd)
	env := withUTF8Locale(append(
		scrubLauncherSession(os.Environ()),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	))
	env = append(env, cfg.Env...)
	cmd.Env = env

	// A sandbox request prepares the ordinary command through the injected
	// Service: the command is wrapped (helper re-exec / sandbox-exec), the
	// runtime tree is created, and enforcement must be confirmed before this
	// session may exist (design spec §7.3). Fail-closed: any preparation or
	// readiness error closes the PTY, tears down the helper and runtime tree,
	// and returns the typed error — no session is registered.
	var prepared *sandbox.PreparedCommand
	if cfg.Sandbox != nil {
		if cfg.sandboxService == nil {
			return nil, sandbox.NewSetupErrorf("sandbox request without a sandbox service")
		}
		pc, perr := cfg.sandboxService.Prepare(context.Background(), *cfg.Sandbox, sandbox.CommandSpec{
			Path: shell,
			Args: []string{"-i"},
			Dir:  cmd.Dir,
			Env:  env,
		})
		if perr != nil {
			return nil, perr
		}
		cmd = pc.Cmd
		prepared = pc
	}

	f, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: cfg.Cols,
		Rows: cfg.Rows,
		X:    cfg.XPixel,
		Y:    cfg.YPixel,
	})
	if err != nil {
		if prepared != nil {
			prepared.Close()
		}
		return nil, err
	}

	if prepared != nil {
		readyCtx, cancel := context.WithTimeout(context.Background(), sandboxReadyTimeout)
		readyErr := prepared.WaitReady(readyCtx)
		cancel()
		if readyErr != nil {
			_ = f.Close()
			prepared.Close()
			return nil, readyErr
		}
	}

	lp := &LocalPty{
		log:      logger,
		cmd:      cmd,
		file:     f,
		done:     make(chan struct{}),
		prepared: prepared,
	}

	// Exactly one waiter owns the process. Sandboxed sessions wait for the
	// child to exit before PreparedCommand.Close releases its runtime tree;
	// Close itself remains the kill-and-cleanup path for startup failures.
	go func() {
		_ = cmd.Wait()
		if prepared != nil {
			prepared.Close()
		}
		close(lp.done)
	}()

	return lp, nil
}

// SandboxInfo returns the immutable sandbox metadata for a sandboxed
// session, or nil for an ordinary one. It implements pty.SandboxInfoProvider.
func (lp *LocalPty) SandboxInfo() *sandbox.SessionInfo {
	if lp.prepared == nil || lp.prepared.Policy == nil {
		return nil
	}
	return &sandbox.SessionInfo{
		Backend:       lp.prepared.Backend,
		Workspace:     lp.prepared.Policy.Workspace,
		WritableRoots: lp.prepared.Policy.WritableRoots,
	}
}

func (lp *LocalPty) Read(p []byte) (int, error) {
	return lp.file.Read(p)
}

func (lp *LocalPty) Write(p []byte) (int, error) {
	return lp.file.Write(p)
}

func (lp *LocalPty) Close() error {
	lp.mu.Lock()
	defer lp.mu.Unlock()

	if lp.closed {
		return nil
	}
	lp.closed = true

	if lp.cmd.Process != nil {
		_ = lp.cmd.Process.Signal(syscall.SIGTERM)
	}
	return lp.file.Close()
}

func (lp *LocalPty) Resize(_ context.Context, cols, rows, xpixel, ypixel uint16) error {
	return pty.Setsize(lp.file, &pty.Winsize{
		Cols: cols,
		Rows: rows,
		X:    xpixel,
		Y:    ypixel,
	})
}

func (lp *LocalPty) Done() <-chan struct{} {
	return lp.done
}
