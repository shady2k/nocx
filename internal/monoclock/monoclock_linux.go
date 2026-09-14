//go:build linux

package monoclock

import "golang.org/x/sys/unix"

// nowRaw reads CLOCK_MONOTONIC: the kernel's own monotonic clock, immune to
// wall-clock adjustment (NTP, a user setting the date) and unaffected by
// leap seconds. It is not immune to a suspend/resume gap — that is what
// CLOCK_MONOTONIC_RAW's Darwin counterpart also does not promise either,
// and neither this package nor spec §7.2 needs it to: a commitBy deadline
// surviving a suspend is not a case the helper's local intents are asked to
// handle.
func nowRaw() Nanos {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		// CLOCK_MONOTONIC is unconditionally supported by every Linux kernel
		// this repository targets; an error here means the syscall itself
		// is unavailable, which is a fact about the machine no caller can
		// recover from by retrying. Panicking is the same choice
		// time.Now() itself makes on a monotonic-read failure.
		panic("monoclock: clock_gettime(CLOCK_MONOTONIC): " + err.Error())
	}
	return Nanos(ts.Nano())
}
