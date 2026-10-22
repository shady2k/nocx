//go:build darwin

package fishchannel

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func newWakePipe() (r, w int, err error) {
	var fds [2]int
	// Darwin has no pipe2. Exclude fork until both owned descriptors carry
	// CLOEXEC so concurrent helper spawns cannot inherit the wake pipe.
	syscall.ForkLock.RLock()
	if err := unix.Pipe(fds[:]); err != nil {
		syscall.ForkLock.RUnlock()
		return 0, 0, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	syscall.ForkLock.RUnlock()
	for _, fd := range fds {
		if err := unix.SetNonblock(fd, true); err != nil {
			_ = unix.Close(fds[0])
			_ = unix.Close(fds[1])
			return 0, 0, err
		}
	}
	return fds[0], fds[1], nil
}
