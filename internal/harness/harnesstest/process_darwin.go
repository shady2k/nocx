//go:build darwin

package harnesstest

import "golang.org/x/sys/unix"

func processRunning(pid int) bool {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return err != unix.ESRCH && err != unix.ENOENT
	}
	return info != nil && info.Proc.P_stat != 5
}
