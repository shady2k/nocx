package main

// THE LINUX UPDATE RESTART IS AN ORDERED HANDOVER (nocx-vh2hr.1.13).
//
// macOS relaunches with `open -n`, which Launch Services answers with a NEW
// instance even while the old one is still running. Linux has no such flag:
// the update restart used to `exec.Command(installPath)` the installed build
// and only then ask this window to quit, and on this platform the webview is
// loaded on ACTIVATION — so a window started while the old GTK application
// still holds the id can be handed that activation and never paint, while its
// Go side (services, coordinator) runs perfectly well. That is the shape the
// nightly's proof printed: a candidate process, a serving candidate
// coordinator, and an update journal nobody ever finalised, because the only
// caller of ReportHealthy is the renderer (nocx-vh2hr.1.8).
//
// So the window no longer starts the build. It starts a small detached waiter
// (setsid — not nohup: the child must leave this session, not merely ignore
// SIGHUP), hands it this process's pid and the installed path, and quits. The
// waiter waits for THIS PROCESS to be gone — pidfd where the kernel has one,
// the pid itself otherwise, never a timer — and then execs the installed
// build, so the new window starts with no other instance of its application
// id alive. The exec keeps the waiter's stdout/stderr, which are this
// window's, so the restarted build's log lands where the launcher's does
// (PR #683).
//
// Failure is said out loud in the window's own log and the journal is left
// alone: an update that could not be started is recovered by the ordinary
// reconcile on the next launch.

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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
// THE KERNEL'S OWN ANSWER FIRST, WHERE IT EXISTS. waitForProcessExitPidfd
// (relaunch_linux.go) is one descriptor that becomes readable only when the
// process it names has exited, so there is no polling window and no pid reuse
// to get wrong. The split is a build constraint because pidfd_open lives only
// in x/sys/unix's Linux half; every other platform — macOS, whose restart goes
// through `open -n` and never starts a waiter — answers supported=false and
// takes the poll below (relaunch_other.go).
//
// The poll is the same fact at a coarser grain — asking whether the pid still
// exists, never a sleep that assumes the answer.
func waitForProcessExit(pid int) error {
	if supported, err := waitForProcessExitPidfd(pid); supported {
		return err
	}
	return pollProcessExit(pid)
}

// pollProcessExit asks whether pid still exists, until it does not.
func pollProcessExit(pid int) error {
	for {
		if killErr := unix.Kill(pid, 0); errors.Is(killErr, unix.ESRCH) {
			return nil
		} else if killErr != nil && !errors.Is(killErr, unix.EPERM) {
			return fmt.Errorf("poll pid %d: %w", pid, killErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runRelaunchWaiter is the waiter: wait for the window that started it, then
// hand this process over to the installed build.
//
// execFn is syscall.Exec in production and a seam in the tests; the split is
// what lets a test assert BOTH halves — that nothing is started while the old
// window lives, and that something is started the moment it is gone.
func runRelaunchWaiter(pid int, installPath string, logger *slog.Logger, execFn func(string, []string, []string) error) error {
	at(logger).Info("relaunch: holding the handover until the window that started it exits",
		"pid", pid, "install", installPath)
	if err := waitForProcessExit(pid); err != nil {
		at(logger).Error("relaunch: the previous window could not be waited for",
			"pid", pid, "error", err)
		return fmt.Errorf("relaunch: wait for pid %d: %w", pid, err)
	}
	at(logger).Info("relaunch: the previous window is gone; starting the installed build",
		"install", installPath)
	if err := execFn(installPath, []string{installPath}, os.Environ()); err != nil {
		// The journal is deliberately left in place: the next launch's
		// reconcile owns the recovery, and a deleted journal here would
		// disarm it.
		at(logger).Error("relaunch: the installed build could not be started; the update journal is left for recovery",
			"install", installPath, "error", err)
		return fmt.Errorf("relaunch: start %s: %w", installPath, err)
	}
	return nil
}
