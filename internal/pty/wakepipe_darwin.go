//go:build darwin

package pty

import "golang.org/x/sys/unix"

// newWakePipe is wakepipe_linux.go's twin for the platform with no pipe2(2)
// syscall: Darwin's unix.Pipe gives an ordinary blocking, exec-inheritable
// pair, so O_NONBLOCK and FD_CLOEXEC are set on each end separately
// afterwards (nocx-6q1uh.18).
func newWakePipe() (r, w int, err error) {
	var fds [2]int
	if err := unix.Pipe(fds[:]); err != nil {
		return 0, 0, err
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
		return 0, 0, err
	}
	if err := unix.SetNonblock(fds[1], true); err != nil {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
		return 0, 0, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	return fds[0], fds[1], nil
}
