package main

import "golang.org/x/sys/unix"

// setRawMode disables local echo and canonical (line-buffered) input on fd,
// the same "stty raw -echo" worker_orchestration_task_test.go's own fake
// claude shells out to — done here with a direct termios ioctl instead,
// because this program is the pane's own process rather than a wrapper
// around one, and golang.org/x/term is not already a dependency of this
// module. It reports the ORIGINAL state so the caller can restore it, though
// nothing in this program's own lifetime needs that beside tidiness: the
// pane ends with the process.
func setRawMode(fd int) (*unix.Termios, error) {
	orig, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	raw := *orig
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &raw); err != nil {
		return nil, err
	}
	return orig, nil
}

// restoreMode puts fd back to the termios state setRawMode read before
// changing it.
func restoreMode(fd int, orig *unix.Termios) error {
	return unix.IoctlSetTermios(fd, ioctlSetTermios, orig)
}
