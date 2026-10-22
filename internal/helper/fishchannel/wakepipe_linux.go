//go:build linux

package fishchannel

import "golang.org/x/sys/unix"

func newWakePipe() (r, w int, err error) {
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		return 0, 0, err
	}
	return fds[0], fds[1], nil
}
