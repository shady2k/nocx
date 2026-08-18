//go:build linux

package sandbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	seccompSetModeFilter         = 1
	seccompFilterFlagNewListener = 1 << 3
	seccompRetAllow              = 0x7fff0000
	seccompRetKillProcess        = 0x80000000
	seccompRetUserNotif          = 0x7fc00000
	seccompUserNotifFlagContinue = 1
	seccompIoctlNotifRecv        = 0xc0502100
	seccompIoctlNotifIDValid     = 0x40082102
	seccompIoctlNotifSend        = 0xc0182101
	seccompPathBytes             = 16 * 1024
)

type seccompData struct {
	Nr                 int32
	Arch               uint32
	InstructionPointer uint64
	Args               [6]uint64
}

type seccompNotif struct {
	ID    uint64
	PID   uint32
	Flags uint32
	Data  seccompData
}

type seccompNotifResp struct {
	ID    uint64
	Val   int64
	Error int32
	Flags uint32
}

type remoteIovec struct {
	Base uintptr
	Len  uintptr
}

type linuxAccessMonitor struct {
	fd        int
	session   *AccessSession
	shell     string
	policy    *Policy
	once      sync.Once
	fdOnce    sync.Once
	onFailure func(error)
	closing   atomic.Bool
	done      chan struct{}
}

func installAccessNotifyFilter() (int, error) {
	arch, ok := linuxAuditArch()
	if !ok {
		return -1, fmt.Errorf("unsupported architecture %s", runtime.GOARCH)
	}
	syscalls := []uint32{uint32(unix.SYS_OPENAT)}
	if unix.SYS_OPENAT2 > 0 {
		syscalls = append(syscalls, uint32(unix.SYS_OPENAT2))
	}
	filters := make([]unix.SockFilter, 0, 6+len(syscalls)*2)
	filters = append(filters,
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, Jf: 0, K: arch},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetKillProcess},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	)
	for _, number := range syscalls {
		filters = append(filters,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: number},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetUserNotif},
		)
	}
	filters = append(filters, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetAllow})
	if len(filters) > int(^uint16(0)) {
		return -1, errors.New("seccomp filter exceeds kernel length field")
	}
	filterCount := uint16(len(filters)) // #nosec G115 -- the immediately preceding bound proves this fits.
	program := unix.SockFprog{Len: filterCount, Filter: &filters[0]}
	// #nosec G103 -- seccomp(2) requires a pointer to the kernel ABI struct; filters remain live through the call.
	fd, _, errno := unix.Syscall(unix.SYS_SECCOMP, seccompSetModeFilter, seccompFilterFlagNewListener, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filters)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func linuxAuditArch() (uint32, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return 0xc000003e, true
	case "arm64":
		return 0xc00000b7, true
	default:
		return 0, false
	}
}

func sendAccessListener(socketFD, listenerFD int) error {
	if socketFD < 3 {
		return errors.New("invalid monitor socket")
	}
	payload := []byte{0}
	var rights []byte
	if listenerFD >= 0 {
		payload[0] = 1
		rights = unix.UnixRights(listenerFD)
	}
	return unix.Sendmsg(socketFD, payload, rights, nil, 0)
}

func receiveAccessListener(socket *os.File) (int, error) {
	if socket == nil {
		return -1, errors.New("monitor socket unavailable")
	}
	payload := make([]byte, 1)
	control := make([]byte, unix.CmsgSpace(4))
	n, controlN, _, _, err := unix.Recvmsg(int(socket.Fd()), payload, control, 0)
	if err != nil {
		return -1, err
	}
	if n != 1 || payload[0] == 0 {
		return -1, nil
	}
	messages, err := unix.ParseSocketControlMessage(control[:controlN])
	if err != nil {
		return -1, err
	}
	for _, message := range messages {
		fds, parseErr := unix.ParseUnixRights(&message)
		if parseErr == nil && len(fds) == 1 {
			return fds[0], nil
		}
	}
	return -1, errors.New("listener descriptor missing")
}

func newLinuxAccessMonitor(fd int, session *AccessSession, shell string, policy *Policy, onFailure func(error)) *linuxAccessMonitor {
	return &linuxAccessMonitor{fd: fd, session: session, shell: shell, policy: policy, onFailure: onFailure, done: make(chan struct{})}
}

func (m *linuxAccessMonitor) Start() {
	go m.run()
}

func (m *linuxAccessMonitor) Close() {
	m.once.Do(func() {
		m.closing.Store(true)
		m.closeListener()
		<-m.done
	})
}

func (m *linuxAccessMonitor) run() {
	defer m.closeListener()
	defer close(m.done)
	for {
		var notification seccompNotif
		// #nosec G103 -- ioctl(2) writes the documented seccomp notification ABI struct.
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(m.fd), seccompIoctlNotifRecv, uintptr(unsafe.Pointer(&notification)))
		if errno != 0 {
			if errno == unix.EINTR {
				continue
			}
			m.fail(errno)
			return
		}
		m.observe(notification)
		response := seccompNotifResp{ID: notification.ID, Flags: seccompUserNotifFlagContinue}
		for {
			// #nosec G103 -- ioctl(2) reads the documented seccomp response ABI struct.
			_, _, errno = unix.Syscall(unix.SYS_IOCTL, uintptr(m.fd), seccompIoctlNotifSend, uintptr(unsafe.Pointer(&response)))
			if errno == unix.EINTR {
				continue
			}
			if errno != 0 && errno != unix.ENOENT {
				m.fail(errno)
				return
			}
			break
		}
	}
}

func (m *linuxAccessMonitor) fail(err error) {
	if !m.closing.Load() && m.onFailure != nil {
		m.onFailure(err)
	}
}

func (m *linuxAccessMonitor) closeListener() {
	m.fdOnce.Do(func() {
		_ = unix.Close(m.fd)
	})
}

func (m *linuxAccessMonitor) observe(notification seccompNotif) {
	if !accessNotificationValid(m.fd, notification.ID) {
		m.session.noteLost()
		return
	}
	pathPointer, flags, ok := accessPathArgs(notification)
	if !ok || pathPointer == 0 {
		m.session.noteLost()
		return
	}
	path, err := readTraceeString(int(notification.PID), pathPointer, seccompPathBytes)
	if err != nil || path == "" || strings.IndexByte(path, 0) >= 0 {
		m.session.noteLost()
		return
	}
	absolute, err := resolveTraceePath(int(notification.PID), notification.Data.Args[0], path)
	if err != nil || len(absolute) > accessPathMaxBytes {
		m.session.noteLost()
		return
	}
	access := AccessReadOnly
	if flags&uint64(unix.O_WRONLY|unix.O_RDWR|unix.O_CREAT|unix.O_TRUNC|unix.O_APPEND) != 0 {
		access = AccessReadWrite
	}
	if flags&uint64(unix.O_PATH) != 0 || policyAllowsAccess(m.policy, observedPolicyPath(absolute), access) {
		return
	}
	executable, _ := os.Readlink(filepath.Join("/proc", strconv.Itoa(int(notification.PID)), "exe"))
	if !accessNotificationValid(m.fd, notification.ID) {
		m.session.noteLost()
		return
	}
	m.session.Record(AccessObservation{
		Shell: m.shell, Executable: executable, Path: absolute,
		Access: access, Operation: syscallOperation(notification.Data.Nr), Source: AccessSourceLinuxSeccomp, At: time.Now().UTC(),
	})
}

func accessPathArgs(notification seccompNotif) (uint64, uint64, bool) {
	switch notification.Data.Nr {
	case int32(unix.SYS_OPENAT):
		return notification.Data.Args[1], notification.Data.Args[2], true
	case int32(unix.SYS_OPENAT2):
		var raw [8]byte
		if err := readTraceeBytes(int(notification.PID), notification.Data.Args[2], raw[:]); err != nil {
			return 0, 0, false
		}
		return notification.Data.Args[1], binary.LittleEndian.Uint64(raw[:]), true
	default:
		return 0, 0, false
	}
}

func syscallOperation(number int32) string {
	if number == int32(unix.SYS_OPENAT2) {
		return "openat2"
	}
	return "openat"
}

func accessNotificationValid(fd int, id uint64) bool {
	// #nosec G103 -- ioctl(2) reads the documented seccomp notification ID value.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), seccompIoctlNotifIDValid, uintptr(unsafe.Pointer(&id)))
	return errno == 0
}

func readTraceeString(pid int, address uint64, limit int) (string, error) {
	buffer := make([]byte, limit)
	if err := readTraceeBytes(pid, address, buffer); err != nil {
		return "", err
	}
	if n := bytesIndexByte(buffer, 0); n >= 0 {
		return string(buffer[:n]), nil
	}
	return "", errors.New("unterminated path")
}

func bytesIndexByte(buffer []byte, target byte) int {
	for n, value := range buffer {
		if value == target {
			return n
		}
	}
	return -1
}

func readTraceeBytes(pid int, address uint64, buffer []byte) error {
	if len(buffer) == 0 {
		return nil
	}
	local := unix.Iovec{Base: &buffer[0], Len: uint64(len(buffer))}
	remote := remoteIovec{Base: uintptr(address), Len: uintptr(len(buffer))}
	// #nosec G103 -- process_vm_readv(2) requires pointers to local and remote kernel ABI iovecs.
	n, _, errno := unix.Syscall6(unix.SYS_PROCESS_VM_READV, uintptr(pid), uintptr(unsafe.Pointer(&local)), 1, uintptr(unsafe.Pointer(&remote)), 1, 0)
	runtime.KeepAlive(buffer)
	if errno != 0 {
		return errno
	}
	if n == 0 {
		return errors.New("empty tracee read")
	}
	return nil
}

func resolveTraceePath(pid int, dirfd uint64, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	baseLink := filepath.Join("/proc", strconv.Itoa(pid), "cwd")
	const (
		atFDCWD32 = uint64(0xffffff9c)
		atFDCWD64 = ^uint64(99)
		maxFD     = uint64(1<<31 - 1)
	)

	if dirfd != atFDCWD32 && dirfd != atFDCWD64 {
		if dirfd > maxFD {
			return "", errors.New("relative path descriptor is out of range")
		}
		baseLink = filepath.Join("/proc", strconv.Itoa(pid), "fd", strconv.FormatUint(dirfd, 10))
	}
	base, err := os.Readlink(baseLink)
	if err != nil || !filepath.IsAbs(base) {
		return "", errors.New("relative path base unavailable")
	}
	return filepath.Clean(filepath.Join(base, path)), nil
}

func observedPolicyPath(path string) string {
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(canonical)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return path
	}
	return filepath.Clean(filepath.Join(parent, filepath.Base(path)))
}

func policyAllowsAccess(policy *Policy, path string, access AccessClass) bool {
	if policy == nil {
		return true
	}
	if access == AccessReadWrite {
		for _, root := range append(append([]string(nil), policy.WritableRoots...), policy.WritableDirs...) {
			if pathWithin(root, path) {
				return true
			}
		}
		for _, file := range policy.WritableFiles {
			if path == file {
				return true
			}
		}
		return false
	}
	for _, root := range append(append(append([]string(nil), policy.ReadOnlyRoots...), policy.WritableRoots...), policy.WritableDirs...) {
		if pathWithin(root, path) {
			return true
		}
	}
	for _, file := range append(append([]string(nil), policy.ReadOnlyFiles...), policy.WritableFiles...) {
		if path == file {
			return true
		}
	}
	return false
}
