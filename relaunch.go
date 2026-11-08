package main

// THE UPDATE RESTART IS AN ORDERED HANDOVER, ON BOTH PLATFORMS
// (nocx-vh2hr.1.13, nocx-vh2hr.1.24).
//
// The window never starts the build. It starts a small detached waiter
// (setsid — not nohup: the child must leave this session, not merely ignore
// SIGHUP), hands it this process's pid and the installed path, and quits. The
// waiter waits for THIS PROCESS to be gone — pidfd where the kernel has one,
// the pid itself otherwise, never a timer — and only then starts the installed
// build. The waiter's stdout/stderr are this window's, so the restarted
// build's log lands where the launcher's does (PR #683).
//
// LINUX: the waiter EXECS the installed AppImage. It used to be the window
// itself that ran `exec.Command(installPath)` before asking this window to
// quit, and on this platform the webview is loaded on ACTIVATION — so a
// window started while the old GTK application still holds the id can be
// handed that activation and never paint, while its Go side (services,
// coordinator) runs perfectly well. That is the shape the nightly's proof
// printed: a candidate process, a serving candidate coordinator, and an update
// journal nobody ever finalised, because the only caller of ReportHealthy is
// the renderer (nocx-vh2hr.1.8).
//
// macOS: the window used to run `/usr/bin/open -n <bundle>` ITSELF, while this
// window was still alive. `-n` is what makes Launch Services answer with a NEW
// instance, and the Dock gave that instance a tile of its own beside the one
// the running window already had; when the old window went away, two tiles
// stayed (nocx-vh2hr.1.24, the owner's own Mac). The update is now handed over
// like Linux's: once this process is gone the waiter opens the installed
// bundle through Launch Services WITHOUT `-n`, so the one instance it starts is
// tied to the bundle's own Dock tile — which is what the owner gets from a
// pinned icon with nocx not running.
//
// The program the waiter execs is the installed build on Linux and
// `/usr/bin/open` on macOS; that choice is [relaunchHandover], and it takes the
// GOOS as a parameter so a test can state the platform it is not on
// (internal/update/serverbin's rule).
//
// Failure is said out loud in the window's own log and the journal is left
// alone: an update that could not be started is recovered by the ordinary
// reconcile on the next launch.

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/shady2k/nocx/internal/procwatch"
)

const (
	relaunchAfterFlag   = "--relaunch-after"
	relaunchInstallFlag = "--relaunch-install"
)

// relaunchWaiterArgs is the command line the waiter is started with.
func relaunchWaiterArgs(installPath string, pid int) []string {
	return []string{relaunchAfterFlag, strconv.Itoa(pid), relaunchInstallFlag, installPath}
}

// relaunchRequest reports a waiter command line, if argv is one. It is read
// before the window logger, the diagnostics and the version flag: the waiter is
// not a window and must not open one.
func relaunchRequest(argv []string) (pid int, installPath string, ok bool) {
	var havePid, haveInstall bool
	for i := 0; i+1 < len(argv); i++ {
		switch argv[i] {
		case relaunchAfterFlag:
			n, err := strconv.Atoi(argv[i+1])
			if err != nil || n <= 0 {
				return 0, "", false
			}
			pid, havePid = n, true
		case relaunchInstallFlag:
			installPath, haveInstall = argv[i+1], true
		}
	}
	return pid, installPath, havePid && haveInstall && installPath != ""
}

// relaunchWaiterCommand starts the waiter that will launch the installed
// build once this process is gone.
//
// SETSID, NOT NOHUP: the waiter must leave this window's session so that a
// terminal closing, or this process group being signalled, cannot take it with
// them — and so that the build it execs begins in a session of its own.
// Output stays this window's (PR #683): the restarted window is the only
// account of what happens next, and the proof reads that stream.
func relaunchWaiterCommand(self, installPath string, pid int) *exec.Cmd {
	cmd := exec.Command(self, relaunchWaiterArgs(installPath, pid)...) //nolint:gosec // self is os.Executable, installPath came from the updater
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// waitForProcessExit blocks until pid is gone.
//
// The waiting is procwatch's, not this package's: "is this pid gone yet?" is a
// kernel observation about a process, and procwatch is the package that asks
// those. It answers with pidfd_open on Linux and with a poll elsewhere, which
// is where a platform split belongs — a GOOS in the root package would move
// every one of its tests into the OS jobs and leave the `!unix` halves of this
// program compiled by nothing (nocx-vh2hr.1.13).
func waitForProcessExit(pid int) error {
	return procwatch.WaitForExit(pid)
}

// relaunchHandover answers what the waiter hands this window's place to: the
// program it execs and that program's command line.
//
// ONE LAUNCHER PER PLATFORM, and each is wrong for the other. On Linux the
// installed AppImage IS the program — the update's target is the file that
// runs. On macOS it is not: the bundle has to be started through Launch
// Services, or the window it opens belongs to no application and the Dock
// cannot tie it to the bundle's tile. `open` without `-n` is that launch, and
// the waiter only reaches here once the old window is gone, so there is no
// second instance for `-n` to force. The window itself never names a
// launcher: it starts this waiter, which is what keeps "nothing starts before
// the old window exits" true on both platforms.
//
// goos is passed rather than read for the same reason [quitSeam.keyBindings]
// takes it: a rule that differs per platform is a rule a test has to be able
// to state for the platform it is not on.
func relaunchHandover(goos, installPath string) (string, []string) {
	if goos == "darwin" {
		return "/usr/bin/open", []string{"open", installPath}
	}
	return installPath, []string{installPath}
}

// runRelaunchWaiter is the waiter: wait for the window that started it, then
// hand this process over to the installed build.
//
// execFn is syscall.Exec in production and a seam in the tests; the split is
// what lets a test assert BOTH halves — that nothing is started while the old
// window lives, and that something is started the moment it is gone — and, per
// [relaunchHandover], that the something is `/usr/bin/open` on macOS and the
// installed build on Linux.
func runRelaunchWaiter(goos string, pid int, installPath string, logger *slog.Logger, execFn func(string, []string, []string) error) error {
	at(logger).Info("relaunch: holding the handover until the window that started it exits",
		"pid", pid, "install", installPath)
	if err := waitForProcessExit(pid); err != nil {
		at(logger).Error("relaunch: the previous window could not be waited for",
			"pid", pid, "error", err)
		return fmt.Errorf("relaunch: wait for pid %d: %w", pid, err)
	}
	at(logger).Info("relaunch: the previous window is gone; starting the installed build",
		"install", installPath)
	program, argv := relaunchHandover(goos, installPath)
	if err := execFn(program, argv, os.Environ()); err != nil {
		// The journal is deliberately left in place: the next launch's
		// reconcile owns the recovery, and a deleted journal here would
		// disarm it.
		at(logger).Error("relaunch: the installed build could not be started; the update journal is left for recovery",
			"install", installPath, "error", err)
		return fmt.Errorf("relaunch: start %s: %w", installPath, err)
	}
	return nil
}
