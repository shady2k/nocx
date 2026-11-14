//go:build linux

package harness

import (
	"os/exec"
	"testing"

	"github.com/shady2k/nocx/internal/proc"
)

func TestGroupRunningExcludesZombie(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	proc.InOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()
	if err := observeExit(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	// WNOWAIT left a real zombie in this group. It is terminated, even though
	// kill(-pgid,0) still reports its process group exists.
	running, err := groupRunning(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Fatal("zombie was counted as running")
	}
	if err := awaitGroupGone(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
}
