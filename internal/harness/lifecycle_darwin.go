//go:build darwin

package harness

import (
	"errors"
	"fmt"

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
		n, err := unix.Kevent(fd, []unix.Kevent_t{change}, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		// A process already exited before registration remains ours until Wait.
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		return validateExitEvent(pid, events[0])
	}
}

func validateExitEvent(pid int, event unix.Kevent_t) error {
	if event.Ident != uint64(pid) || event.Filter != unix.EVFILT_PROC {
		return fmt.Errorf("unexpected exit observation: %+v", event)
	}
	if event.Flags&unix.EV_ERROR != 0 {
		if event.Data == int64(unix.ESRCH) {
			// It exited before registration; Wait still owns the child identity.
			return nil
		}
		if event.Data == 0 {
			return errors.New("exit observation returned a receipt without exit")
		}
		return fmt.Errorf("exit observation registration: %w", unix.Errno(event.Data))
	}
	if event.Fflags&unix.NOTE_EXIT == 0 {
		return fmt.Errorf("unexpected exit observation: %+v", event)
	}
	return nil
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
