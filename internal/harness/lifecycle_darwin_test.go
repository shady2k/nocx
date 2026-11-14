//go:build darwin

package harness

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinExitEventValidation(t *testing.T) {
	const pid = 123
	valid := unix.Kevent_t{Ident: pid, Filter: unix.EVFILT_PROC, Fflags: unix.NOTE_EXIT}
	if err := validateExitEvent(pid, valid); err != nil {
		t.Fatal(err)
	}
	for name, event := range map[string]unix.Kevent_t{
		"wrong pid":           {Ident: pid + 1, Filter: unix.EVFILT_PROC, Fflags: unix.NOTE_EXIT},
		"wrong filter":        {Ident: pid, Filter: unix.EVFILT_READ, Fflags: unix.NOTE_EXIT},
		"not exit":            {Ident: pid, Filter: unix.EVFILT_PROC},
		"registration failed": {Ident: pid, Filter: unix.EVFILT_PROC, Flags: unix.EV_ERROR, Data: int64(unix.EBADF)},
		"wrong error pid":     {Ident: pid + 1, Filter: unix.EVFILT_PROC, Flags: unix.EV_ERROR, Data: int64(unix.ESRCH)},
		"wrong error filter":  {Ident: pid, Filter: unix.EVFILT_READ, Flags: unix.EV_ERROR, Data: int64(unix.ESRCH)},
		"success receipt":     {Ident: pid, Filter: unix.EVFILT_PROC, Flags: unix.EV_ERROR},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateExitEvent(pid, event); err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
	if err := validateExitEvent(pid, unix.Kevent_t{Ident: pid, Filter: unix.EVFILT_PROC, Flags: unix.EV_ERROR, Data: int64(unix.ESRCH)}); err != nil {
		t.Fatal(err)
	}
	if err := validateExitEvent(pid, unix.Kevent_t{Ident: pid, Filter: unix.EVFILT_PROC, Flags: unix.EV_ERROR, Data: int64(unix.EBADF)}); !errors.Is(err, unix.EBADF) {
		t.Fatalf("error = %v", err)
	}
}
