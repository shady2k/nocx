package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
)

// Spawning the daemon: the half of the lifecycle that cannot live in
// nocx-server, because the thing that must decide whether a daemon is
// already running is the thing that would otherwise raise a second one
// (cmd/nocx-server/main.go says the same from the other side).

// Spawned is the handle on a daemon this launcher started. It is not a
// process the launcher owns — setsid has already put it in a session of its
// own — so the handle carries only what the readiness wait needs: something
// to name in a failure, and a way to hear that it died before it answered.
type Spawned struct {
	// PID is the child's process id, for the message a person reads.
	PID int
	// Command is what was executed, likewise.
	Command string
	// Exit closes with the wait status when the process ends. A daemon
	// that exits before its socket appears is the difference between "it
	// is still starting" and "it refused to start", and without this the
	// launcher could only report the timeout and never the reason. It is
	// nil for a spawner that has nothing to wait on.
	Exit <-chan error
}

// Spawner raises a coordinator. An interface because the launcher's own
// decisions — lock, spawn, wait, refuse — must be testable without putting
// a process on the machine, and because a spawn that fails (a missing
// binary, a permission refusal) is a path a launcher has to report rather
// than hang on (AD-8).
type Spawner interface {
	Spawn(ctx context.Context) (Spawned, error)
}

// ExecSpawnerConfig is what the composition root knows and this package
// must not guess: where the binary is.
type ExecSpawnerConfig struct {
	// Path is the absolute path to the nocx-server executable. Where it
	// lives differs per platform — inside the bundle on macOS, a versioned
	// copy under ~/.local/share on Linux (design §4) — and that is the
	// caller's knowledge, not this package's.
	Path string
	// Environ supplies the launcher's environment. nil means os.Environ,
	// which is what production wants; a test supplies its own.
	Environ func() []string
	// Logger records the spawn. It never receives a token, because the
	// launcher has none at this point — the daemon has not minted one yet.
	Logger *slog.Logger
}

// ExecSpawner starts nocx-server detached: its own session, stdio on
// /dev/null, a cleaned environment and no arguments at all.
type ExecSpawner struct {
	cfg ExecSpawnerConfig
}

// NewExecSpawner returns the real spawner. It validates nothing here: the
// path is checked by the exec itself, whose error already names it, and a
// spawner constructed at startup must not fail before anybody has asked it
// to do anything.
func NewExecSpawner(cfg ExecSpawnerConfig) *ExecSpawner {
	return &ExecSpawner{cfg: cfg}
}

// Spawn starts the daemon and returns as soon as the fork succeeded. It
// does NOT wait for readiness — that is the launcher's, which is the only
// thing that knows what "ready" means (a socket that answers a hello).
//
// The three properties this function exists for, and what each is worth:
//
//   - setsid, so the daemon leads a session of its own. Without it the
//     daemon shares the window's process group and a terminal signal —
//     or the shell that started `wails dev` — takes it down with the
//     window, which is the entire point of moving the backend out.
//   - stdio on /dev/null. A detached daemon inheriting the window's pipes
//     keeps them open (a reader on the other end never sees EOF) and can
//     block forever on a write nobody drains. nocx-server writes its
//     diagnostics to a log file and its stdout carries nothing by design.
//   - no arguments. The token must never reach argv, where `ps` shows it
//     to every process on the machine (design §6). nocx-server parses no
//     flags at all; passing none is what keeps that true from this side.
func (s *ExecSpawner) Spawn(ctx context.Context) (Spawned, error) {
	if s.cfg.Path == "" {
		return Spawned{}, errors.New("coordinator: no nocx-server path to spawn")
	}
	logger := s.cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// exec.Command, not CommandContext: cancelling the launcher's context
	// must not kill a daemon that is already up and serving other windows.
	// The context bounds the launcher's WAIT, not the daemon's life.
	//nolint:gosec // the path comes from the composition root, never from a caller or the wire
	cmd := exec.Command(s.cfg.Path)
	cmd.Env = SpawnEnvironment(s.environ())
	// A nil Stdin/Stdout/Stderr is /dev/null for os/exec, which is exactly
	// what is wanted and is stated here because "nil" does not say it.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	// A working directory of its own, because a daemon may not inherit its
	// launcher's: the window's cwd belongs to the window, and this process
	// is started precisely to outlive it. Under an AppImage that is not a
	// nicety — AppRun chdirs into the FUSE mount before exec, the mount is
	// unmounted when the window exits, and an inherited cwd would leave the
	// daemon (and everything it spawns without a directory of its own)
	// standing in a directory that no longer exists. CI run 33320751321 saw
	// it from the far end: a session shell reporting '/work/squashfs-root'
	// instead of $HOME.
	cmd.Dir = SpawnDirectory(cmd.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return Spawned{}, fmt.Errorf("coordinator: starting %s: %w", s.cfg.Path, err)
	}

	// The child is still ours until it is reaped, setsid or not, so
	// something must Wait or it becomes a zombie for the life of the
	// window. The wait doubles as the "it died before it answered" signal
	// the readiness loop reads.
	exit := make(chan error, 1)
	go func() {
		exit <- cmd.Wait()
		close(exit)
	}()

	logger.Info("coordinator: spawned nocx-server", "pid", cmd.Process.Pid, "path", s.cfg.Path)
	return Spawned{PID: cmd.Process.Pid, Command: s.cfg.Path, Exit: exit}, nil
}

func (s *ExecSpawner) environ() []string {
	if s.cfg.Environ != nil {
		return s.cfg.Environ()
	}
	return os.Environ()
}

// spawnFallbackDir is where a daemon stands when it has no home to stand
// in. The root directory is the one path on a POSIX machine that is always
// present and is never a mount that somebody unmounts underneath us, which
// is the same property $HOME is chosen for below.
const spawnFallbackDir = "/"

// SpawnDirectory answers "where does the daemon stand", from the very
// environment the daemon will run with — so the directory it stands in and
// the $HOME it resolves are one fact rather than two that can disagree.
//
// # Why the home directory rather than the profile data directory
//
// Both outlive the window, which is the property actually required, and
// internal/storage already owns the data directory (AppDirName). The home
// directory wins on two counts. It is the directory the daemon is already
// guaranteed to resolve — it is in the environment being handed over, and
// nothing here has to import storage or agree with it about a path — and it
// exists before nocx does. The profile directory is one the app creates:
// on a first run it may not exist yet at the moment of the spawn, and a
// cmd.Dir that is absent makes Start fail. Trading a leaked directory for a
// coordinator that will not start on a fresh machine is not a fix.
//
// # And why absence is a fallback rather than an error
//
// A spawn must not begin failing on a machine where it works today, so an
// unresolvable, absent or non-directory HOME falls through to
// [spawnFallbackDir] instead of refusing. The last HOME in the environment
// is the one taken, because that is the one the child's own libc will read.
//
// Exported for the same reason [SpawnEnvironment] is: the choice is the
// point, and a test that had to raise a process to read it would be testing
// exec rather than the decision.
func SpawnDirectory(environ []string) string {
	home := ""
	for _, entry := range environ {
		if name, value, ok := strings.Cut(entry, "="); ok && name == "HOME" {
			home = value
		}
	}
	if home == "" {
		return spawnFallbackDir
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return spawnFallbackDir
	}
	return home
}

// inheritedOverrides are the variables a daemon must not inherit from
// whoever happened to open a window.
//
// The list is short on purpose: every entry is a variable that would make
// the daemon resolve something OTHER than what the launcher expects, and
// nothing is on it merely for tidiness.
//
// NEITHER IS READ BY ANY BINARY THIS REPO BUILDS ANY MORE, and the strip
// stays regardless. That is a deliberate exception to "no dead code", and the
// reason is that this is a security control rather than a feature: §6 forbids
// a class — "a daemon that inherited NOCX_WS_ADDR or a profile override
// resolves something other than what the launcher expects" — and the class
// outlives its current members. A reader is the thing that comes and goes; a
// variable a developer's shell exported before the cutover does not.
//
//   - NOCX_WS_ADDR pinned the WebSocket listen address. Its readers are gone
//     with cmd/devharness (design D11) and the listener is now loopback with
//     an OS-chosen port and no override at all (transport/ws.go). While it
//     was read, a daemon that took its address from an inherited variable
//     would have been listening somewhere the launcher never chose — and,
//     since the variable can name any interface, potentially off loopback,
//     which design §6 forbids outright.
//   - NOCX_NO_SYSTEM_KEYSTORE stated the keystore stance. D10 moved that to
//     a build property (internal/app/keystore_build_*.go) and the last
//     environment reader went with devharness, so today an inherited copy
//     would change nothing. The rule it serves is the one that matters: the
//     stance is never something any process of the user can supply, and the
//     cheapest way to keep that true is for the daemon never to see it.
//
// Deliberately NOT on the list, because the reasoning runs the other way:
//
//   - NOCX_TEST_APPDIR (storage.TestAppDirEnv) names the profile root.
//     The launcher watched a socket inside THAT profile, so the daemon
//     has to resolve the same one; stripping it would send the daemon to
//     the developer's real profile while the launcher waited on a socket
//     in a temporary one — nocx-ti8w, from the other direction.
//   - HOME, XDG_*, PATH and everything else the daemon needs to be a
//     normal process of this user.
//   - NOCX_LOG_LEVEL: a launcher started with debug logging should raise
//     a daemon that logs the same way, or the interesting half of a
//     startup is missing from the record.
var inheritedOverrides = []string{
	"NOCX_WS_ADDR",
	"NOCX_NO_SYSTEM_KEYSTORE",
}

// SpawnEnvironment returns environ with the overrides above removed.
//
// Exported so the decision is testable as a decision rather than through a
// process: the list is the point, and a test that had to spawn something to
// read it would be testing exec.
func SpawnEnvironment(environ []string) []string {
	cleaned := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if slices.Contains(inheritedOverrides, name) {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}
