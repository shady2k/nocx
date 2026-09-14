//go:build linux

package pty

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// openMaster opens /dev/ptmx, unlocks the slave and derives its name — the
// same three steps creack/pty's Linux Open performs (pty_linux.go) — and sets
// O_NONBLOCK on the RAW fd before the one os.NewFile call that ever wraps it.
//
// # Why this package cannot use creack/pty's Open and flip a flag afterwards
//
// creack/pty's Linux Open calls os.OpenFile("/dev/ptmx", os.O_RDWR, 0), and
// Go's own os.OpenFile puts a freshly opened character device into
// non-blocking mode as a side effect of being pollable
// (internal/poll.FD.Init, called from os/file_unix.go's newFile with
// kind=kindOpenFile) — so FOR A MOMENT the master already is what this bead
// wants. Then creack/pty calls ptsname and unlockpt, and both reach the fd
// through (*os.File).Fd(): "if f.nonblock { f.pfd.SetBlocking() }... because
// historically we have always returned a descriptor opened in blocking
// mode" (os/file_unix.go). A file Go itself put into non-blocking mode has
// f.nonblock set, so the very first ioctl after the open SILENTLY reverts
// the master to blocking, and it never becomes non-blocking again — nothing
// downstream calls SetNonblock a second time. That is nocx-6q1uh.1's root
// cause on Linux as much as on Darwin: a program that floods output and
// never reads stdin can still stall its pane through this master.
//
// So every ioctl below is issued against the RAW fd, before anything ever
// wraps it in an *os.File, and O_NONBLOCK is set on that same raw fd
// afterwards. os.NewFile then sees a descriptor ALREADY non-blocking
// (unix.HasNonblockFlag reads true off F_GETFL) and, per newFile's own
// branching, never marks f.nonblock true for it — so future .Fd() calls
// (Setsize among them) leave it alone. See master_nonblock_darwin.go for the
// platform this was first measured on.
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

	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return nil, "", fmt.Errorf("pty: TIOCGPTN: %w", err)
	}
	// TIOCSPTLCK takes a pointer to an int; zero clears the lock, the same
	// value creack/pty's own unlockpt passes.
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return nil, "", fmt.Errorf("pty: TIOCSPTLCK (unlock): %w", err)
	}
	// THE LINE THIS FILE EXISTS FOR: set before anything ever calls .Fd() on
	// a wrapper around this descriptor, so no wrapper ever believes it put
	// this fd into non-blocking mode and no wrapper ever reverts it.
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, "", fmt.Errorf("pty: set the master non-blocking before it is wrapped: %w", err)
	}

	closeOnErr = false
	return os.NewFile(uintptr(fd), "/dev/ptmx"), "/dev/pts/" + strconv.Itoa(n), nil
}
