//go:build darwin

package wavepin

import (
	"time"

	"golang.org/x/sys/unix"
)

// Darwin and Linux each answer process identity through a different kernel
// interface. Separate files keep that choice in the build, rather than a
// runtime switch that would leave one platform's types uncheckable elsewhere.
func readProcess(pid int) (processSnapshot, error) {
	if pid <= 0 {
		return processSnapshot{}, unix.ESRCH
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return processSnapshot{}, err
	}
	if process.Proc.P_starttime.Sec <= 0 || process.Eproc.Ppid <= 0 {
		return processSnapshot{}, unix.ESRCH
	}
	return processSnapshot{
		Parent: int(process.Eproc.Ppid),
		StartTime: time.Unix(
			process.Proc.P_starttime.Sec,
			int64(process.Proc.P_starttime.Usec)*int64(time.Microsecond),
		),
	}, nil
}
