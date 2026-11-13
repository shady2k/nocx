//go:build darwin

package monoclock

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// readClocks answers Darwin's pair. CLOCK_UPTIME_RAW is mach_absolute_time:
// Apple documents it as "like CLOCK_MONOTONIC_RAW, but does not increment while
// the system is asleep". CLOCK_MONOTONIC_RAW is the mach_continuous_time
// domain — the clock this package's Now reads, which does increment while the
// system is asleep. The difference between their deltas is the suspend,
// exactly.
//
// CLOCK_UPTIME_RAW and not plain CLOCK_MONOTONIC: Apple's clock_gettime(3) says
// CLOCK_MONOTONIC *does* continue while the system is asleep (it is the
// continuous domain too), so it would answer a zero gap on every machine.
func readClocks() Readings {
	var awake, continuous unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_UPTIME_RAW, &awake); err != nil {
		panic(fmt.Sprintf("monoclock: clock_gettime(CLOCK_UPTIME_RAW): %v", err))
	}
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &continuous); err != nil {
		panic(fmt.Sprintf("monoclock: clock_gettime(CLOCK_MONOTONIC_RAW): %v", err))
	}
	return Readings{Awake: Nanos(awake.Nano()), Continuous: Nanos(continuous.Nano())}
}
