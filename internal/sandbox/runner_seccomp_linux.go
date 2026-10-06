//go:build linux

package sandbox

import (
	"encoding/json"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	bpfLoadWordAbsolute = 0x20
	bpfJumpEqual        = 0x15
	bpfReturn           = 0x06
)

type (
	sockFilter struct {
		Code   uint16
		JT, JF uint8
		K      uint32
	}
	sockFprog struct {
		Len    uint16
		Filter *sockFilter
	}
)

// installOpenNotificationFilter runs after Landlock has already been applied.
func installOpenNotificationFilter() (ObserverStatus, int, error) {
	arch, ok := seccompAuditArch()
	if !ok {
		return ObserverUnsupported, -1, runnerErr("seccomp-architecture")
	}
	filters := [...]sockFilter{
		{Code: bpfLoadWordAbsolute, K: 4},
		{Code: bpfJumpEqual, JT: 1, K: arch},
		{Code: bpfReturn, K: unix.SECCOMP_RET_ALLOW},
		{Code: bpfLoadWordAbsolute, K: 0},
		{Code: bpfJumpEqual, JF: 1, K: uint32(unix.SYS_OPENAT)},
		{Code: bpfReturn, K: unix.SECCOMP_RET_USER_NOTIF},
		{Code: bpfJumpEqual, JF: 1, K: uint32(unix.SYS_OPENAT2)},
		{Code: bpfReturn, K: unix.SECCOMP_RET_USER_NOTIF},
		{Code: bpfReturn, K: unix.SECCOMP_RET_ALLOW},
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return ObserverFailed, -1, err
	}
	program := sockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	// #nosec G103 -- fixed BPF program and backing array retained through the kernel syscall.
	fd, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_NEW_LISTENER, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filters)
	if errno != 0 {
		if errno == unix.ENOSYS || errno == unix.EOPNOTSUPP || errno == unix.EINVAL {
			return ObserverUnsupported, -1, errno
		}
		return ObserverFailed, -1, errno
	}
	return ObserverActive, int(fd), nil
}

func seccompAuditArch() (uint32, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return 0xc000003e, true
	case "arm64":
		return 0xc00000b7, true
	default:
		return 0, false
	}
}

func sendRunnerPacketWithFD(packet RunnerReady, fd int) error {
	data, err := json.Marshal(packet)
	if err != nil || len(data) > 256 || fd < 0 {
		return runnerErr("readiness-packet")
	}
	n, err := unix.SendmsgN(RunnerFDReady, data, unix.UnixRights(fd), nil, 0)
	if err != nil || n != len(data) {
		return runnerErr("readiness-listener")
	}
	return nil
}
