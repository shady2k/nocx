//go:build unix

package app

import (
	"os"
	"syscall"
)

// The two observations §6.2's loss events are distinguished by, on this
// platform. They are separate from the seam that uses them so a test can
// state a loss instead of arranging a process and a socket.

// socketPresent reports whether the control socket still names something.
// Lstat, not Stat: a symlink where our socket was is not our socket.
func socketPresent(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// processAlive reports whether the master process still exists. Signal 0 is
// the existence question and delivers nothing.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
