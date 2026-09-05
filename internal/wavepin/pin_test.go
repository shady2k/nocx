package wavepin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPinLiveProcessAndChild(t *testing.T) {
	pinner := SystemPinner{}
	root, err := pinner.Pin(os.Getpid())
	if err != nil {
		t.Fatalf("Pin(self): %v", err)
	}
	if root.PID != os.Getpid() {
		t.Fatalf("root PID = %d, want %d", root.PID, os.Getpid())
	}
	if root.StartTime.IsZero() {
		t.Fatal("Pin(self) returned a zero start time")
	}

	child := exec.Command("sleep", "30")
	startErr := child.Start()
	if startErr != nil {
		t.Fatalf("start child: %v", startErr)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	member, err := pinner.Member(child.Process.Pid, root)
	if err != nil {
		t.Fatalf("Member(child): %v", err)
	}
	if !member {
		t.Fatal("Member(child) = false, want true")
	}
}

func TestPinRejectsExitedProcess(t *testing.T) {
	pinner := SystemPinner{}
	process := exec.Command("sh", "-c", "exit 0") //nolint:gosec // test-only fixed command
	startErr := process.Start()
	if startErr != nil {
		t.Fatalf("start process: %v", startErr)
	}
	root, err := pinner.Pin(process.Process.Pid)
	if err != nil {
		t.Fatalf("Pin(process): %v", err)
	}
	waitErr := process.Wait()
	if waitErr != nil {
		t.Fatalf("wait process: %v", waitErr)
	}

	_, err = pinner.Member(root.PID, root)
	if !errors.Is(err, ErrGone) {
		t.Fatalf("Member(exited): error = %v, want ErrGone", err)
	}
}

func TestPinRejectsBrokenAncestry(t *testing.T) {
	pinner := SystemPinner{}
	root, err := pinner.Pin(os.Getpid())
	if err != nil {
		t.Fatalf("Pin(self): %v", err)
	}

	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	command := "sleep 30 & echo $! > \"$1\""
	orphanParent := exec.Command("sh", "-c", command, "sh", pidFile) //nolint:gosec // test-only fixed command
	runErr := orphanParent.Run()
	if runErr != nil {
		t.Fatalf("start orphan parent: %v", runErr)
	}
	pidBytes, readErr := os.ReadFile(pidFile) // #nosec G304 -- test-owned temporary path
	if readErr != nil {
		t.Fatalf("read orphan pid: %v", readErr)
	}
	orphanPID, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse orphan pid %q: %v", pidBytes, err)
	}
	t.Cleanup(func() {
		if process, findErr := os.FindProcess(orphanPID); findErr == nil {
			_ = process.Kill()
		}
	})

	member, err := pinner.Member(orphanPID, root)
	if member {
		t.Fatal("Member(orphan) = true, want false")
	}
	if !errors.Is(err, ErrChainBroken) {
		t.Fatalf("Member(orphan): error = %v, want ErrChainBroken", err)
	}
}
