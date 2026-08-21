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
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// waitForeground waits until the pty reports a foreground process group at
// all — the shell's own after spawn, the execution's while it runs. The
// kernel answers only once the child has set its controlling terminal, so
// the assertion is on the observable state, never on a duration.
func waitForeground(t testing.TB, lp *LocalPty) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pgid, err := lp.ForegroundProcessGroup()
		if err == nil && pgid > 0 {
			return pgid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the pty never reported a foreground process group")
	return 0
}

// waitForFile polls until path exists — the observable a child process
// itself produces (a pid file, a trap's receipt marker) — and returns its
// trimmed content. The wait is on the observable, never on a duration.
func waitForFile(t testing.TB, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the file is the test's own temp file, written by the command under test
			return strings.TrimSpace(string(b))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the file %s never appeared", path)
	return ""
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pgid, err := lp.ForegroundProcessGroup()
		if err == nil && pgid > 0 && pgid != lp.Pid() {
			jobPGID = pgid
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if jobPGID == 0 {
		t.Fatal("the foreground group never left the shell — the job did not start")
	}

	// The signal must reach the execution: SIGINT ends it, and the kernel
	// returns the foreground to the shell.
	if err := lp.SignalForeground(syscall.SIGINT); err != nil {
		t.Fatalf("SignalForeground(SIGINT): %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(-jobPGID, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		pgid, err := lp.ForegroundProcessGroup()
		if err == nil && pgid == lp.Pid() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the execution's process group %d survived SIGINT (or the shell never resumed)", jobPGID)
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
	pidText := waitForFile(t, pidFile)
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
	if got := waitForFile(t, marker); got != "reached" {
		t.Fatalf("the child's TERM trap wrote %q, want the receipt marker", got)
	}
	// Observable two, the death: the trap's wait reaped the sleep, so the
	// pid it wrote is gone — zombie-free.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(childPID, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the execution's child %d was not reaped after the group signal — cancellation reached only the shell", childPID)
}

func TestLocalPty_SignalForegroundZeroChecksExistence(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	if _, err := lp.Write([]byte("sleep 30\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pgid, err := lp.ForegroundProcessGroup()
		if err == nil && pgid > 0 && pgid != lp.Pid() {
			// Signal 0 is the existence check: a live group answers nil.
			if serr := lp.SignalForeground(0); serr != nil {
				t.Fatalf("SignalForeground(0) on a live execution = %v, want nil", serr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the job never started")
}
