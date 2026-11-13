//go:build linux

package monoclock

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// readClocks answers Linux's pair: CLOCK_MONOTONIC stops while the system is
// suspended, and CLOCK_BOOTTIME includes the time it spent suspended
// (clock_gettime(2): "CLOCK_BOOTTIME ... identical to CLOCK_MONOTONIC, except
// it also includes any time that the system is suspended"). The difference
// between their deltas is the suspend, exactly.
func readClocks() Readings {
	var awake, continuous unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &awake); err != nil {
		panic(fmt.Sprintf("monoclock: clock_gettime(CLOCK_MONOTONIC): %v", err))
	}
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &continuous); err != nil {
		panic(fmt.Sprintf("monoclock: clock_gettime(CLOCK_BOOTTIME): %v", err))
	}
	return Readings{Awake: Nanos(awake.Nano()), Continuous: Nanos(continuous.Nano())}
}
