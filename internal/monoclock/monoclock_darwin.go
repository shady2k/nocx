//go:build darwin

package monoclock

import "golang.org/x/sys/unix"

// nowRaw reads CLOCK_MONOTONIC_RAW: Darwin's mach_continuous_time-based
// clock, immune to wall-clock adjustment the same way Linux's
// CLOCK_MONOTONIC is. CLOCK_MONOTONIC_RAW rather than plain
// CLOCK_MONOTONIC on this platform because Darwin's plain CLOCK_MONOTONIC
// is defined relative to mach_absolute_time and is itself subject to NTP
// frequency-skew adjustment (slewing) — the raw variant is the one Apple
// documents as unaffected by it, which is the property spec §7.2 asks the
// pair of clocks to share.
func nowRaw() Nanos {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts); err != nil {
		// See monoclock_linux.go's identical panic: a machine that cannot
		// answer this syscall at all is not one a retry fixes.
		panic("monoclock: clock_gettime(CLOCK_MONOTONIC_RAW): " + err.Error())
	}
	return Nanos(ts.Nano())
}
