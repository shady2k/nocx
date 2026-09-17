//go:build darwin

package pty

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ptyNameLen is the buffer TIOCPTYGNAME fills: the ioctl's own parameter
// length, encoded in its request number the way every BSD ioctl carries its
// argument size (_IOC_PARM_LEN in <sys/ioccom.h>, and creack/pty's own
// pty_darwin.go computes the identical number from the identical macro).
// Written as the mask-and-shift rather than the literal 128 it evaluates to,
// so a reader can check it against the constant instead of trusting a magic
// number nobody derived in this file.
const ptyNameLen = (unix.TIOCPTYGNAME >> 16) & 0x1fff

// openMaster opens /dev/ptmx itself, grants and unlocks the slave and reads
// its name — the same four steps creack/pty's Darwin Open performs
// (pty_darwin.go:14-18) — and sets O_NONBLOCK on the RAW fd before the one
// os.NewFile call that ever wraps it.
//
// creack/pty's own Open calls syscall.Open (never os.OpenFile) and wraps the
// result with os.NewFile at once, still blocking: os/file_unix.go's newFile
// only treats a wrapped descriptor as pollable when the caller says it is
// ALREADY non-blocking (kind=kindNewFile, and pollable is true exactly when
// the nonBlocking argument is), and a plain syscall.Open never sets that
// flag. So the master creack/pty hands back is wrapped blocking and stays
// blocking for the life of the *os.File — SetReadDeadline on it returns
// ErrNoDeadline rather than nil — which is nocx-6q1uh.1 on this platform: a
// program that floods output and never reads stdin stalls its pane because
// the read loop cannot poll for readability at all, only block inside
// Read().
//
// So this file performs the open itself, sets O_NONBLOCK on the raw fd
// before wrapping, and os.NewFile then sees a descriptor already
// non-blocking — which, per newFile's own branching (see
// master_nonblock_linux.go's longer note on the same mechanism), it never
// reverts on a later .Fd() call.
func openMaster() (master *os.File, slaveName string, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("pty: open /dev/ptmx: %w", err)
	}
	closeOnErr := true
	defer func() {
		if closeOnErr {
			_ = unix.Close(fd)
		}
	}()

	name, err := ptyGrantedName(fd)
	if err != nil {
		return nil, "", err
	}
	// TIOCPTYGRANT and TIOCPTYUNLK carry no argument (creack/pty's grantpt
	// and unlockpt pass 0 through the same VOID-direction ioctls).
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return nil, "", fmt.Errorf("pty: TIOCPTYGRANT: %w", err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return nil, "", fmt.Errorf("pty: TIOCPTYUNLK: %w", err)
	}
	// THE LINE THIS FILE EXISTS FOR: set before anything ever calls .Fd() on
	// a wrapper around this descriptor.
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, "", fmt.Errorf("pty: set the master non-blocking before it is wrapped: %w", err)
	}

	closeOnErr = false
	return os.NewFile(uintptr(fd), "/dev/ptmx"), name, nil
}

// ptyGrantedName reads TIOCPTYGNAME off the raw fd — an OUT-direction ioctl
// with no unix.IoctlGet* helper for a fixed byte buffer, so this issues the
// syscall the same way unix.IoctlSetWinsize and its neighbours do: a pointer
// to a stack buffer, through the package's own exported Syscall rather than
// an unexported one this package cannot reach.
func ptyGrantedName(fd int) (string, error) {
	var buf [ptyNameLen]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0]))) //nolint:gosec,staticcheck // gosec: buf is a stack array sized to exactly what TIOCPTYGNAME's own encoded parameter length (ptyNameLen) says the ioctl writes, and it is alive for the whole syscall — the pointer never outlives the call or is reused afterward. staticcheck (SA1019): unix.SYS_IOCTL is deprecated in favour of libSystem wrappers, but x/sys/unix exposes none for a fixed-byte-buffer OUT ioctl on darwin — IoctlGetInt/Winsize/Termios cover only their own fixed types, and the package's ioctlPtr that backs them is unexported, so this is the only path this package can reach
	if errno != 0 {
		return "", fmt.Errorf("pty: TIOCPTYGNAME: %w", errno)
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i]), nil
		}
	}
	return "", fmt.Errorf("pty: TIOCPTYGNAME: name was not NUL-terminated")
}
