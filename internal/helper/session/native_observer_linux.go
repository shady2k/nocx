//go:build linux

package session

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/shady2k/nocx/internal/sandbox"
	"golang.org/x/sys/unix"
)

const (
	seccompGetNotifSizes = unix.SECCOMP_GET_NOTIF_SIZES
	seccompNotifRecv     = unix.SECCOMP_IOCTL_NOTIF_RECV
	seccompNotifSend     = unix.SECCOMP_IOCTL_NOTIF_SEND
	seccompContinue      = unix.SECCOMP_USER_NOTIF_FLAG_CONTINUE
	maxRemoteRead        = 4096
)

type (
	seccompNotifSizes struct {
		Notif    uint16
		Response uint16
		Data     uint16
	}
	seccompNotifData struct {
		Nr   int32
		Arch uint32
		IP   uint64
		Args [6]uint64
	}
	seccompNotif struct {
		ID    uint64
		PID   uint32
		Flags uint32
		Data  seccompNotifData
	}
	seccompNotifResp struct {
		ID    uint64
		Val   int64
		Error int32
		Flags uint32
	}
)

type linuxNativeObserver struct {
	file         *os.File
	processDone  <-chan struct{}
	sink         sandbox.DiagnosticSink
	fatal        func()
	closeOnce    sync.Once
	done         chan struct{}
	closing      atomic.Bool
	fatalOnce    sync.Once
	fatalPending atomic.Bool
	failed       atomic.Bool
}

func startNativeObserver(config nativeObserverConfig) nativeObserver {
	if config.Listener == nil {
		if config.Sink != nil {
			config.Sink.SetObserver(sandbox.ObserverUnavailable)
		}
		done := make(chan struct{})
		close(done)
		return &linuxNativeObserver{done: done}
	}
	o := &linuxNativeObserver{file: config.Listener, processDone: config.Done, sink: config.Sink, fatal: config.Fatal, done: make(chan struct{})}
	if o.sink != nil {
		o.sink.SetObserver(sandbox.ObserverActive)
	}
	go o.readLoop()
	return o
}

func (o *linuxNativeObserver) Close() {
	o.closeOnce.Do(func() {
		o.closing.Store(true)
		<-o.done
		if o.file != nil {
			_ = o.file.Close()
		}
		if o.sink != nil && !o.failed.Load() {
			o.sink.SetObserver(sandbox.ObserverUnavailable)
		}
	})
}

func (o *linuxNativeObserver) readLoop() {
	defer func() {
		close(o.done)
		if o.fatalPending.Swap(false) {
			o.fatalOnce.Do(func() {
				if o.fatal != nil {
					o.fatal()
				}
			})
		}
	}()
	fd := int(o.file.Fd())
	var sizes seccompNotifSizes
	// #nosec G103 -- fixed kernel UAPI struct; returned sizes are validated before notifications.
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, seccompGetNotifSizes, 0, uintptr(unsafe.Pointer(&sizes)))
	if errno != 0 || sizes.Notif != uint16(unsafe.Sizeof(seccompNotif{})) || sizes.Response != uint16(unsafe.Sizeof(seccompNotifResp{})) || sizes.Data != uint16(unsafe.Sizeof(seccompNotifData{})) {
		o.fail()
		return
	}
	for {
		if o.closing.Load() {
			return
		}
		select {
		case <-o.processDone:
			if o.sink != nil {
				o.sink.SetObserver(sandbox.ObserverUnavailable)
			}
			return
		default:
		}
		pollfds := [1]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}} //nolint:gosec // kernel file descriptors are signed C ints.
		_, err := unix.Poll(pollfds[:], 100)
		if err != nil {
			if o.closing.Load() || err == unix.EINTR {
				continue
			}
			o.fail()
			return
		}
		if pollfds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			if o.closing.Load() {
				return
			}
			if pollfds[0].Revents == unix.POLLHUP && o.processDone != nil {
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-o.processDone:
					timer.Stop()
					if o.sink != nil {
						o.sink.SetObserver(sandbox.ObserverUnavailable)
					}
					return
				case <-timer.C:
				}
			}
			o.fail()
			return
		}
		if pollfds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		var n seccompNotif
		// #nosec G103 -- notification layout was checked against GET_NOTIF_SIZES.
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), seccompNotifRecv, uintptr(unsafe.Pointer(&n)))
		if errno != 0 {
			if errno == unix.EINTR || errno == unix.EAGAIN || errno == unix.ENOENT {
				continue
			}
			if o.closing.Load() {
				return
			}
			o.fail()
			return
		}
		// Capture only bounded tracee metadata while this syscall is held.
		// Prediction, filesystem canonicalization and sink delivery happen
		// strictly after CONTINUE, including unknown metadata and overflow.
		observation := o.observation(n)
		resp := continueNotification(n.ID)
		// #nosec G103 -- fixed validated kernel response layout.
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, uintptr(fd), seccompNotifSend, uintptr(unsafe.Pointer(&resp)))
		if errno != 0 {
			if errno == unix.ENOENT {
				continue
			}
			if o.closing.Load() {
				return
			}

			o.fail()
			return
		}
		if o.sink != nil {
			o.sink.Observe(observation)
		}
	}
}

func continueNotification(id uint64) seccompNotifResp {
	return seccompNotifResp{ID: id, Flags: seccompContinue}
}

func (o *linuxNativeObserver) fail() {
	if o.closing.Load() {
		return
	}
	select {
	case <-o.processDone:
		return
	default:
	}
	o.failed.Store(true)
	if o.sink != nil {
		o.sink.SetObserver(sandbox.ObserverFailed)
	}
	o.fatalPending.Store(true)
}

func (o *linuxNativeObserver) observation(n seccompNotif) sandbox.DiagnosticObservation {
	name := "openat"
	pid := int(n.PID)
	if pid <= 0 {
		return unknownLinuxObservation(name)
	}
	if n.Data.Nr == int32(unix.SYS_OPENAT2) {
		name = "openat2"
	}
	arch, ok := seccompAuditArchForObserver()
	if !ok || n.Data.Arch != arch {
		return unknownLinuxObservation(name)
	}
	flags := n.Data.Args[2]
	if name == "openat2" {
		if n.Data.Args[3] < 24 {
			return unknownLinuxObservation(name)
		}
		var how, verify [24]byte
		got, ok := readRemote(pid, n.Data.Args[2], how[:])
		second, stable := readRemote(pid, n.Data.Args[2], verify[:])
		if !ok || !stable || got != len(how) || second != len(verify) || how != verify {
			return unknownLinuxObservation(name)
		}
		flags = binary.LittleEndian.Uint64(how[:8])
	}
	path, known := readRemoteString(pid, n.Data.Args[1])
	if !known || path == "" {
		return unknownLinuxObservation(name)
	}
	if !strings.HasPrefix(path, "/") {
		base, ok := o.readBase(pid, int64(int32(uint32(n.Data.Args[0])))) //nolint:gosec // dirfd is the signed low 32 bits of a syscall C int.
		if !ok {
			return unknownLinuxObservation(name)
		}
		if base == "/" {
			path = base + path
		} else {
			path = base + "/" + path
		}
	}
	if filepath.Clean(path) != path {
		return unknownLinuxObservation(name)
	}
	if len(path) == 0 || len(path) > maxRemoteRead {
		return unknownLinuxObservation(name)
	}
	exe, _ := boundedProcLinkStable(pid, "exe")
	access := sandbox.DiagnosticRead
	if flags&(unix.O_WRONLY|unix.O_RDWR|unix.O_CREAT|unix.O_TRUNC|unix.O_APPEND) != 0 {
		access = sandbox.DiagnosticWrite
	}
	if flags&unix.O_PATH != 0 {
		access = sandbox.DiagnosticUnknown
	}
	return sandbox.DiagnosticObservation{Executable: exe, Path: path, Operation: name, Access: access, PathKnown: true, Source: sandbox.DiagnosticLinuxSeccomp, Precision: sandbox.PrecisionAttempted}
}

func unknownLinuxObservation(op string) sandbox.DiagnosticObservation {
	return sandbox.DiagnosticObservation{Operation: op, Access: sandbox.DiagnosticUnknown, PathKnown: false, Source: sandbox.DiagnosticLinuxSeccomp, Precision: sandbox.PrecisionAttempted}
}

func seccompAuditArchForObserver() (uint32, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return 0xc000003e, true
	case "arm64":
		return 0xc00000b7, true
	default:
		return 0, false
	}
}

func (o *linuxNativeObserver) readBase(pid int, dirfd int64) (string, bool) {
	if dirfd == -100 {
		return boundedProcLinkStable(pid, "cwd")
	}
	if dirfd < 0 || dirfd > 1<<20 {
		return "", false
	}
	return boundedProcLinkStable(pid, "fd/"+strconv.FormatInt(dirfd, 10))
}

func readRemote(pid int, addr uint64, dst []byte) (int, bool) {
	if pid <= 0 || addr == 0 || len(dst) == 0 || len(dst) > maxRemoteRead {
		return 0, false
	}
	var localIov [1]unix.Iovec
	var remoteIov [1]unix.RemoteIovec
	localIov[0] = unix.Iovec{Base: &dst[0], Len: uint64(len(dst))}
	remoteIov[0] = unix.RemoteIovec{Base: uintptr(addr), Len: len(dst)}
	n, err := unix.ProcessVMReadv(pid, localIov[:], remoteIov[:], 0)
	return n, err == nil
}

func readRemoteString(pid int, addr uint64) (string, bool) {
	var buf, verify [maxRemoteRead]byte
	n, ok := readRemote(pid, addr, buf[:])
	if !ok || n == 0 {
		return "", false
	}
	nul := bytes.IndexByte(buf[:n], 0)
	if nul < 0 {
		return "", false
	}
	second, stable := readRemote(pid, addr, verify[:nul+1])
	if !stable || second != nul+1 || !bytes.Equal(buf[:nul+1], verify[:second]) {
		return "", false
	}
	return string(buf[:nul]), true
}

func boundedProcLinkStable(pid int, name string) (string, bool) {
	first, ok := readProcLinkOnce(pid, name)
	if !ok {
		return "", false
	}
	second, ok := readProcLinkOnce(pid, name)
	return first, ok && first == second
}

func readProcLinkOnce(pid int, name string) (string, bool) {
	if pid <= 0 || len(name) > 32 {
		return "", false
	}
	procPath := "/proc/" + strconv.Itoa(pid) + "/" + name
	var buf [maxRemoteRead]byte
	n, err := unix.Readlink(procPath, buf[:])
	if err != nil || n == 0 || n >= maxRemoteRead {
		return "", false
	}
	value := string(buf[:n])
	if strings.HasSuffix(value, " (deleted)") {
		return "", false
	}
	return value, true
}
