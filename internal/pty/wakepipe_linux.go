//go:build linux

package pty

import "golang.org/x/sys/unix"

// newWakePipe opens the self-pipe WaitReadable polls alongside the readiness
// dup (nocx-6q1uh.18): unix.Pipe2 sets O_NONBLOCK and O_CLOEXEC on both ends
// in the one syscall Linux offers for it. See wakepipe_darwin.go for the
// platform with no pipe2(2) at all.
func newWakePipe() (r, w int, err error) {
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		return 0, 0, err
	}
	return fds[0], fds[1], nil
}
