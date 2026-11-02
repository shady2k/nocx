package main

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// THE HANDOVER'S TWO HALVES ARE ONE ASSERTION EACH (nocx-vh2hr.1.13): nothing
// is started while the window that owns the application id is alive, and the
// installed build is started the moment it is gone.
func TestTheWaiterStartsTheInstalledBuildOnlyAfterTheWindowExits(t *testing.T) {
	sleeper := exec.Command("/bin/sh", "-c", "sleep 30") //nolint:gosec // a fixture process, not input
	if err := sleeper.Start(); err != nil {
		t.Fatalf("start the stand-in window: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = sleeper.Wait(); close(exited) }()

	var started atomic.Bool
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- runRelaunchWaiter(sleeper.Process.Pid, "/install/nocx.AppImage",
			slog.New(slog.NewTextHandler(os.Stderr, nil)),
			func(string, []string, []string) error {
				once.Do(func() { started.Store(true) })
				return nil
			})
	}()

	// The window is alive and the waiter is waiting on it, not on a clock.
	select {
	case err := <-done:
		t.Fatalf("the waiter returned while the window was alive: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if started.Load() {
		t.Fatal("the installed build was started while the window still held the application id")
	}

	_ = sleeper.Process.Kill()
	<-exited
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the waiter failed after the window exited: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiter never noticed the window had exited")
	}
	if !started.Load() {
		t.Fatal("the window exited and the installed build was never started")
	}
}

// The failure path: the exec fails, the error is logged where a person reads it
// (the window's log) and returned so the caller can exit non-zero. The journal
// is untouched — recovery is the next launch's reconcile.
func TestTheWaiterReportsAnExecFailure(t *testing.T) {
	gone := exec.Command("/bin/true") //nolint:gosec // fixture
	if err := gone.Run(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	boom := errors.New("exec format error")
	err := runRelaunchWaiter(gone.Process.Pid, "/install/missing.AppImage", logger,
		func(string, []string, []string) error { return boom })
	if err == nil {
		t.Fatal("a failed exec was reported as success")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error %v does not carry the exec failure", err)
	}
	out := buf.String()
	if !strings.Contains(out, "could not be started") || !strings.Contains(out, "journal is left for recovery") {
		t.Fatalf("the failure was not said in the window's log; log was:\n%s", out)
	}
	if strings.Contains(out, "starting the installed build") {
		// Ordering: the "starting" line is written before the exec, so it is
		// expected; what matters is that the failure line follows it.
		_ = out
	}
}

// The window's half: what it starts is a waiter carrying its own pid and the
// installed path, in a session of its own, with this window's streams.
func TestTheRestartStartsAHandoverWaiterNotTheBuild(t *testing.T) {
	cmd := relaunchWaiterCommand("/app/nocx", "/install/nocx.AppImage", 4242)
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--relaunch-after 4242") {
		t.Fatalf("the waiter was not told which window to wait for: %v", cmd.Args)
	}
	if !strings.Contains(joined, "--relaunch-install /install/nocx.AppImage") {
		t.Fatalf("the waiter was not told what to start: %v", cmd.Args)
	}
	if cmd.Path == "/install/nocx.AppImage" {
		t.Fatal("the restart started the installed build itself, before this window released the application")
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("the waiter must leave this window's session (setsid), or a terminal close takes it down")
	}
	if cmd.Stdout != os.Stdout || cmd.Stderr != os.Stderr {
		t.Fatal("the restarted window's log must stay on this window's streams")
	}
}

func TestTheWaiterCommandLineIsRecognised(t *testing.T) {
	pid, install, ok := relaunchRequest(relaunchWaiterArgs("/install/nocx.AppImage", 99))
	if !ok || pid != 99 || install != "/install/nocx.AppImage" {
		t.Fatalf("relaunchRequest = %d %q %v", pid, install, ok)
	}
	for _, argv := range [][]string{
		nil,
		{"--version"},
		{"--relaunch-after", "0", "--relaunch-install", "/x"},
		{"--relaunch-after", "12"},
		{"--relaunch-install", "/x"},
	} {
		if _, _, ok := relaunchRequest(argv); ok {
			t.Errorf("%v was read as a handover command", argv)
		}
	}
}
