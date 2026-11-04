package procwatch_test

// WaitForExit is what the update handover waits on (nocx-vh2hr.1.13): the
// detached waiter must not start the installed build until the window that
// started it is gone. It lived in the root package behind a GOOS split, which
// gave package main a platform half it must not have; it lives here now,
// beside the other question this package asks the kernel about a pid.
//
// Nothing here waits on a duration for a POSITIVE answer: "it returns" is
// awaited on a channel with a generous bound, and "it has not returned" is
// the only negative, made meaningful by waiting for the goroutine to enter
// the call first.

import (
	"os/exec"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/procwatch"
)

// startSleeper starts a process that lives until the test kills it. Nothing
// in the product's path runs a shell here: this is a waiter test, not a
// pty test.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// waitInBackground calls WaitForExit and reports the result on a channel it
// closes only after signalling that the call is under way.
func waitInBackground(pid int) (entered <-chan struct{}, result <-chan error) {
	in := make(chan struct{})
	out := make(chan error, 1)
	go func() {
		close(in)
		out <- procwatch.WaitForExit(pid)
	}()
	return in, out
}

// TestWaitForExit_ReturnsWhenTheProcessIsGone: a process that exits is
// noticed, and noticed without a timer deciding it.
func TestWaitForExit_ReturnsWhenTheProcessIsGone(t *testing.T) {
	cmd := startSleeper(t)
	entered, result := waitInBackground(cmd.Process.Pid)
	<-entered

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the sleeper: %v", err)
	}
	// Reaped, which is what "gone" means to a poll: a zombie still answers
	// kill(pid, 0).
	_ = cmd.Wait()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("WaitForExit returned an error for a process that exited: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WaitForExit did not return after the process it waits for was gone")
	}
}

// TestWaitForExit_KeepsWaitingWhileTheProcessLives: a process that is still
// running is NOT noticed. This is the half that matters at the handover — a
// waiter that returned early would start the new window over the old one.
func TestWaitForExit_KeepsWaitingWhileTheProcessLives(t *testing.T) {
	cmd := startSleeper(t)
	entered, result := waitInBackground(cmd.Process.Pid)
	<-entered

	select {
	case err := <-result:
		t.Fatalf("WaitForExit returned while the process was still running (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestWaitForExit_ReturnsForAProcessThatIsAlreadyGone: the waiter is started
// by a window that may exit before it reaches the call.
func TestWaitForExit_ReturnsForAProcessThatIsAlreadyGone(t *testing.T) {
	cmd := startSleeper(t)
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the sleeper: %v", err)
	}
	_ = cmd.Wait()

	done := make(chan error, 1)
	go func() { done <- procwatch.WaitForExit(pid) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForExit returned an error for a pid that is gone: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WaitForExit blocked on a pid that no longer exists")
	}
}
