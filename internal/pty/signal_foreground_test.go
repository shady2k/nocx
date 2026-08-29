package pty

// The foreground-process-group seam (ADR-0020 decision 2): cancellation
// escalates INT → TERM → KILL against the EXECUTION's process group — the
// foreground job's group, created by the interactive shell's job control —
// so it reaches the command's children, never only the shell. The pty owns
// the master, so it can ask the kernel which group is foreground
// (TIOCGPGRP) without ever reading the byte stream (AD-6).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
	"golang.org/x/sys/unix"
)

// waitForOutput waits until the pty has produced `want`. The pty is read on a
// goroutine of its own because Read blocks: a test that read inline would
// hang past its own bound rather than fail with what it did see. The bound is
// a failure detector, never the thing being waited on — the condition is the
// marker's arrival.
func waitForOutput(t testing.TB, lp *LocalPty, want string) {
	t.Helper()
	var mu sync.Mutex
	var seen strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := lp.Read(buf)
			if n > 0 {
				mu.Lock()
				seen.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { <-done })
	waittest.WaitForTimeoutDetail(t, fmt.Sprintf("the pty to produce %q", want), 20*time.Second,
		func() string {
			mu.Lock()
			defer mu.Unlock()
			return seen.String()
		},
		func() bool {
			mu.Lock()
			defer mu.Unlock()
			return strings.Contains(seen.String(), want)
		})
}

// waitForeground waits until the pty reports a foreground process group at
// all — the shell's own after spawn, the execution's while it runs. The
// kernel answers only once the child has set its controlling terminal, so
// the assertion is on the observable state, never on a duration.
func waitForeground(t testing.TB, lp *LocalPty) int {
	t.Helper()
	var pgid int
	waittest.WaitFor(t, "the pty to report a foreground process group", func() bool {
		got, err := lp.ForegroundProcessGroup()
		if err == nil && got > 0 {
			pgid = got
			return true
		}
		return false
	})
	return pgid
}

func TestLocalPty_SignalForegroundAtPromptIsNoop(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	// At the prompt the foreground group is the shell's own: the guard
	// refuses to signal it, because the shell is not part of the execution
	// the cancellation is aimed at.
	pgid := waitForeground(t, lp)
	if pgid != lp.Pid() {
		t.Fatalf("foreground pgid = %d, want the shell's own pid %d", pgid, lp.Pid())
	}
	if err := lp.SignalForeground(syscall.SIGINT); !errors.Is(err, ErrNoForeground) {
		t.Fatalf("SignalForeground at the prompt = %v, want ErrNoForeground", err)
	}
}

func TestLocalPty_SignalForegroundReachesTheExecution(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	// Run a long foreground job and wait for the observable transition: the
	// foreground group leaves the shell's own (the job is running).
	if _, err := lp.Write([]byte("sleep 30\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var jobPGID int
	waittest.WaitFor(t, "the foreground group to leave the shell", func() bool {
		pgid, err := lp.ForegroundProcessGroup()
		if err == nil && pgid > 0 && pgid != lp.Pid() {
			jobPGID = pgid
			return true
		}
		return false
	})

	// The signal must reach the execution: SIGINT ends it, and the kernel
	// returns the foreground to the shell.
	if err := lp.SignalForeground(syscall.SIGINT); err != nil {
		t.Fatalf("SignalForeground(SIGINT): %v", err)
	}
	waittest.WaitForDetail(t, "the execution group to end or foreground to return",
		func() string {
			return fmt.Sprintf("the execution's process group %d survived SIGINT (or the shell never resumed)", jobPGID)
		},
		func() bool {
			if err := unix.Kill(-jobPGID, 0); errors.Is(err, unix.ESRCH) {
				return true
			}
			pgid, err := lp.ForegroundProcessGroup()
			return err == nil && pgid == lp.Pid()
		})
}

func TestLocalPty_SignalForegroundReachesAChildNotOnlyTheShell(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	// The execution spawns its own child and reveals it: sh -c writes the
	// CHILD's pid (the backgrounded sleep, $!) and waits. The escalation
	// must kill the child too — a signal that reached only the shell would
	// leave the sleep alive. The pid file is the child's identity, written
	// by the command itself, never guessed.
	//
	// The child's receipt is a TERM trap that writes a marker file, and
	// the trap's body WAITS — reaping the backgrounded sleep. That shape
	// is the observable, learned from the container: /bin/sh is dash there
	// and bash here, and the two differ in whether the parent reaps its
	// background child before dying. On dash the sleep dies from the group
	// signal but is never reaped — a zombie, and kill(0) keeps succeeding
	// forever, so asserting the death with a kill(0) poll fails in the
	// container exactly where the signal worked. The marker is the child's
	// OWN receipt that the signal reached it; the trap's wait reaps the
	// sleep, so its death is observable as ESRCH without polling a zombie.
	dir := t.TempDir()
	marker := dir + "/reached.marker"
	pidFile := dir + "/child.pid"
	cmd := "sh -c 'trap \"echo reached > " + marker + "; wait\" TERM; sleep 30 & echo $! > " + pidFile + "; wait'\n"
	if _, err := lp.Write([]byte(cmd)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Wait for the observable: the command's own child pid appears.
	var pidText string
	waittest.WaitForDetail(t, "the child pid file",
		func() string { return fmt.Sprintf("the file %s never appeared", pidFile) },
		func() bool {
			b, err := os.ReadFile(pidFile) //nolint:gosec // the path is this test's own temp file
			if err != nil {
				return false
			}
			pidText = strings.TrimSpace(string(b))
			return true
		})
	var childPID int
	if _, err := fmt.Sscanf(pidText, "%d", &childPID); err != nil || childPID <= 0 {
		t.Fatalf("the command never wrote a child pid (file holds %q)", pidText)
	}
	// The child must be alive before the signal (it is the thing being
	// bounded that does not cooperate).
	if err := unix.Kill(childPID, 0); err != nil {
		t.Fatalf("child %d not running before the signal: %v", childPID, err)
	}

	if err := lp.SignalForeground(syscall.SIGTERM); err != nil {
		t.Fatalf("SignalForeground(SIGTERM): %v", err)
	}

	// Observable one, the receipt: the child's own trap ran — the signal
	// reached a process that is not the shell.
	var markerText string
	waittest.WaitForDetail(t, "the child's TERM receipt marker",
		func() string { return fmt.Sprintf("the file %s never appeared", marker) },
		func() bool {
			b, err := os.ReadFile(marker) //nolint:gosec // the path is this test's own temp file
			if err != nil {
				return false
			}
			markerText = strings.TrimSpace(string(b))
			return true
		})
	if got := markerText; got != "reached" {
		t.Fatalf("the child's TERM trap wrote %q, want the receipt marker", got)
	}
	// Observable two, the death: the trap's wait reaped the sleep, so the
	// pid it wrote is gone — zombie-free.
	waittest.WaitForDetail(t, "the execution's child to be reaped",
		func() string {
			return fmt.Sprintf("the execution's child %d was not reaped after the group signal — cancellation reached only the shell", childPID)
		},
		func() bool {
			return errors.Is(unix.Kill(childPID, 0), unix.ESRCH)
		})
}

func TestLocalPty_SignalForegroundZeroChecksExistence(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	if _, err := lp.Write([]byte("sleep 30\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waittest.WaitFor(t, "the execution foreground group to start", func() bool {
		pgid, err := lp.ForegroundProcessGroup()
		return err == nil && pgid > 0 && pgid != lp.Pid()
	})
	// Signal 0 is the existence check: a live group answers nil.
	if err := lp.SignalForeground(0); err != nil {
		t.Fatalf("SignalForeground(0) on a live execution = %v, want nil", err)
	}
}

// TestLocalPty_ProtectedShellGroupIsItsOwnAnswer separates the two facts
// ErrNoForeground used to carry as one (nocx-7l4ex.10).
//
// "The foreground group is the launcher shell's own" and "there is no group
// left to signal" are different states of the world, and the caller that
// decides whether to fall back to the terminal's own interrupt byte has to
// tell them apart: the first means a program may well be running INSIDE the
// protected group, the second means nothing is running at all. They stayed
// indistinguishable while both answered ErrNoForeground, so the specific
// error WRAPS the general one — every existing caller reading "nothing to
// cancel" keeps reading it, and the one caller that needs the distinction
// can ask for it.
func TestLocalPty_ProtectedShellGroupIsItsOwnAnswer(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	pgid := waitForeground(t, lp)
	if pgid != lp.Pid() {
		t.Fatalf("foreground pgid = %d, want the shell's own pid %d", pgid, lp.Pid())
	}
	err := lp.SignalForeground(syscall.SIGINT)
	if !errors.Is(err, ErrProtectedForeground) {
		t.Fatalf("SignalForeground at the prompt = %v, want ErrProtectedForeground", err)
	}
	if !errors.Is(err, ErrNoForeground) {
		t.Fatalf("ErrProtectedForeground must still satisfy ErrNoForeground; got %v", err)
	}
}

// TestLocalPty_ProtectedGroupHoldsARunningProgram is the incident's topology
// at the seam that decides about it (nocx-7l4ex.11).
//
// `set +m` turns job control off, and bash then runs a foreground command in
// its OWN process group instead of creating one for it — the same shape
// ADR-0024 names ("`set +m` puts shell and command in one group"), and the
// shape the owner's `top` was in. So the kernel's answer is identical to the
// answer it gives at an idle prompt, while a program is very much running:
// this is exactly why the protected answer cannot be read as "nothing is
// running", and why nothing here may infer otherwise from the byte stream.
func TestLocalPty_ProtectedGroupHoldsARunningProgram(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	waitForeground(t, lp)
	dir := t.TempDir()
	// The marker is a file's CONTENT, echoed back by the running program —
	// so observing it means that program has completed its execve and is the
	// one holding the terminal, and it cannot be satisfied by the shell's
	// echo of the command line (the line names only the path).
	marker := filepath.Join(dir, "shared-group-ready")
	if err := os.WriteFile(marker, []byte("SHARED-GROUP-READY\n"), 0o600); err != nil {
		t.Fatalf("write the readiness marker: %v", err)
	}
	if _, err := lp.Write([]byte("set +m\ntail -f '" + marker + "'\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForOutput(t, lp, "SHARED-GROUP-READY")

	pgid, err := lp.ForegroundProcessGroup()
	if err != nil {
		t.Fatalf("ForegroundProcessGroup: %v", err)
	}
	if pgid != lp.Pid() {
		t.Fatalf("foreground pgid = %d, want the shell's own pid %d — job control was not off", pgid, lp.Pid())
	}
	if err := lp.SignalForeground(syscall.SIGINT); !errors.Is(err, ErrProtectedForeground) {
		t.Fatalf("SignalForeground over a shared group = %v, want ErrProtectedForeground", err)
	}
}
