//go:build linux

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// waitForProcessExitPidfd waits for pid to exit through the kernel's pidfd.
//
// supported is false when this kernel has no pidfd_open, or refuses one for a
// process this one may not signal; waitForProcessExit then polls the pid
// instead. A pidfd is one descriptor that becomes readable only when the
// process it names has exited, so there is no polling window and no pid reuse
// to get wrong.
func waitForProcessExitPidfd(pid int) (supported bool, err error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return true, nil // already gone
		}
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
			return false, nil
		}
		return true, fmt.Errorf("pidfd_open %d: %w", pid, err)
	}
	defer func() { _ = unix.Close(fd) }()
	// A pidfd is a small non-negative int; the cast is the syscall's own type,
	// not a narrowing that can lose a real descriptor.
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}} //nolint:gosec // pidfd fits int32
	for {
		_, pollErr := unix.Poll(fds, -1)
		if errors.Is(pollErr, unix.EINTR) {
			continue
		}
		return true, pollErr
	}
}
