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

// inheritedOverrides are the variables a daemon must not inherit from
// whoever happened to open a window.
//
// The list is short on purpose: every entry is a variable that would make
// the daemon resolve something OTHER than what the launcher expects, and
// nothing is on it merely for tidiness.
//
//   - NOCX_WS_ADDR pins the WebSocket listen address (cmd/devharness reads
//     it, and app.WithWSAddr applies it). The launcher learns the address
//     from the socket the daemon reports on; a daemon that took its
//     address from an inherited variable would be listening somewhere the
//     launcher never chose — and, since the variable can name any
//     interface, potentially off loopback, which design §6 forbids
//     outright.
//   - NOCX_NO_SYSTEM_KEYSTORE decides whether the vault may reach the OS
//     keystore. Design §6 says it in as many words: for a process that
//     lives for days that stance must be a property of the build, not
//     something any process of the user can supply — and D10 makes the
//     stance declared rather than discovered. Until A1.2 moves it to the
//     build, clearing it here is what stops one window's environment
//     deciding the keystore policy for every window afterwards.
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
