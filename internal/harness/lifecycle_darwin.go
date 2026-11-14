//go:build darwin

package harness

import (
	"errors"

	"golang.org/x/sys/unix"
)

func observeExit(pid int) error {
	fd, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	events := make([]unix.Kevent_t, 1)
	for {
		_, err = unix.Kevent(fd, []unix.Kevent_t{change}, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		// A process already exited before registration remains ours until Wait.
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return err
	}
}

func groupRunning(pgid int) (bool, error) {
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if int(entry.Eproc.Pgid) == pgid && entry.Proc.P_stat != 5 {
			return true, nil
		}
	}
	return false, nil
}
