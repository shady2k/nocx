//go:build linux || darwin

package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
	"golang.org/x/sys/unix"
)

var (
	errNativeLaunch       = errors.New("sandbox: native launch failed")
	errNativeClosePending = errors.New("sandbox: native candidate termination unconfirmed")
)

// nativeLaunch owns the private startup descriptors and any temporary deny probe.
type nativeLaunch struct {
	prepared      *sandbox.Prepared
	files         []*os.File
	ready         *net.UnixConn
	errorReader   *os.File
	probePath     string
	probeListener *net.UnixListener
	deadline      time.Time
	observer      sandbox.ObserverStatus
	observerNonce string
	listener      *os.File
}

func prepareNativeLaunch(prepared *sandbox.Prepared, cfg *pty.Config) (_ *nativeLaunch, err error) {
	launch := &nativeLaunch{prepared: prepared, deadline: time.Now().Add(30 * time.Second)}
	defer func() {
		if err != nil {
			launch.Close()
		}
	}()
	if len(cfg.ExtraFiles) > 2 || len(prepared.Roots) != len(prepared.Policy.Roots) || prepared.Workspace == nil {
		return nil, errNativeLaunch
	}
	plan := sandbox.RunnerPlan{Version: sandbox.RunnerPlanVersion, Policy: prepared.Policy, Digest: prepared.Digest, Args: append([]string{prepared.Policy.Shell}, cfg.Args...), RootFDs: make([]int, len(prepared.Roots)), WorkspaceFD: sandbox.RunnerFDRoots + len(prepared.Roots)}
	if prepared.Policy.Backend == sandbox.MacOSSeatbelt {
		launch.probePath, err = createSeatbeltProbe(prepared)
		if err != nil {
			return nil, errNativeLaunch
		}
		plan.ProbePath = launch.probePath
		var nonce [8]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, errNativeLaunch
		}
		plan.ProbeSocket = filepath.Join(prepared.Policy.WorkspaceRoot, ".nxsb-"+hex.EncodeToString(nonce[:])+".sock")
		var observerNonce [16]byte
		if _, err = rand.Read(observerNonce[:]); err != nil {
			return nil, errNativeLaunch
		}
		plan.ObserverNonce = hex.EncodeToString(observerNonce[:])
		launch.observerNonce = plan.ObserverNonce
		launch.probeListener, err = net.ListenUnix("unix", &net.UnixAddr{Name: plan.ProbeSocket, Net: "unix"})
		if err != nil {
			return nil, errNativeLaunch
		}
	}
	extra := append([]*os.File(nil), cfg.ExtraFiles...)
	for index := range extra {
		plan.KeepFDs = append(plan.KeepFDs, index+3)
	}
	for len(extra) < 2 {
		placeholder, openErr := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if openErr != nil {
			return nil, errNativeLaunch
		}
		launch.files = append(launch.files, placeholder)
		extra = append(extra, placeholder)
	}
	for index := range plan.RootFDs {
		plan.RootFDs[index] = sandbox.RunnerFDRoots + index
	}
	encoded, encodeErr := sandbox.EncodeRunnerPlan(plan)
	if encodeErr != nil {
		return nil, encodeErr
	}
	policy, createErr := os.CreateTemp(prepared.Runtime.Root, ".launch-plan-")
	if createErr != nil {
		return nil, errNativeLaunch
	}
	launch.files = append(launch.files, policy)
	if unlinkErr := os.Remove(policy.Name()); unlinkErr != nil {
		return nil, errNativeLaunch
	}
	if _, writeErr := policy.Write(encoded); writeErr != nil {
		return nil, errNativeLaunch
	}
	if _, seekErr := policy.Seek(0, io.SeekStart); seekErr != nil {
		return nil, errNativeLaunch
	}
	syscall.ForkLock.RLock()
	socketType := unix.SOCK_SEQPACKET
	if runtime.GOOS == "darwin" {
		socketType = unix.SOCK_DGRAM
	}
	sockets, socketErr := unix.Socketpair(unix.AF_UNIX, socketType, 0)
	if socketErr == nil {
		unix.CloseOnExec(sockets[0])
		unix.CloseOnExec(sockets[1])
	}
	syscall.ForkLock.RUnlock()
	if socketErr != nil {
		return nil, errNativeLaunch
	}
	parent := os.NewFile(uintptr(sockets[0]), "sandbox-ready-parent")
	child := os.NewFile(uintptr(sockets[1]), "sandbox-ready-child")
	launch.files = append(launch.files, child)
	connection, connectionErr := net.FileConn(parent)
	_ = parent.Close()
	if connectionErr != nil {
		return nil, errNativeLaunch
	}
	var ok bool
	launch.ready, ok = connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errNativeLaunch
	}
	reader, writer, pipeErr := os.Pipe()
	if pipeErr != nil {
		return nil, errNativeLaunch
	}
	launch.errorReader = reader
	launch.files = append(launch.files, writer)
	extra = append(extra, policy, child, writer)
	extra = append(extra, prepared.Roots...)
	extra = append(extra, prepared.Workspace)
	cfg.Command = prepared.Policy.Runner
	cfg.Args = nil
	cfg.Cwd = prepared.Policy.WorkspaceRoot
	cfg.Env = prepared.Environment(cfg.Env)
	cfg.ExtraFiles = extra
	return launch, nil
}

func createSeatbeltProbe(prepared *sandbox.Prepared) (string, error) {
	for _, base := range []string{os.TempDir(), "/private/tmp", "/private/var/tmp"} {
		file, err := os.CreateTemp(base, ".nocx-seatbelt-deny-")
		if err != nil {
			continue
		}
		name := file.Name()
		path, pathErr := filepath.EvalSymlinks(name)
		closeErr := file.Close()
		if pathErr != nil || closeErr != nil {
			_ = os.Remove(name)
			continue
		}
		path = filepath.Clean(path)
		if len(path) > 80 {
			_ = os.Remove(path)
			continue
		}
		covered := false
		for _, root := range prepared.Policy.Roots {
			if root.Access == sandbox.ReadWrite && pathWithin(root.Path, path) {
				covered = true
				break
			}
		}
		if !covered {
			return path, nil
		}
		_ = os.Remove(path)
	}
	return "", errNativeLaunch
}

func pathWithin(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, strings.TrimRight(parent, string(os.PathSeparator))+string(os.PathSeparator))
}

func (launch *nativeLaunch) closeChildCopies() {
	for _, file := range launch.files {
		_ = file.Close()
	}
	launch.files = nil
}

func (launch *nativeLaunch) Close() {
	launch.closeChildCopies()
	if launch.listener != nil {
		_ = launch.listener.Close()
		launch.listener = nil
	}
	if launch.probeListener != nil {
		_ = launch.probeListener.Close()
		launch.probeListener = nil
	}
	if launch.ready != nil {
		_ = launch.ready.Close()
		launch.ready = nil
	}
	if launch.errorReader != nil {
		_ = launch.errorReader.Close()
		launch.errorReader = nil
	}
	if launch.probePath != "" {
		_ = os.Remove(launch.probePath)
		launch.probePath = ""
	}
}

func (launch *nativeLaunch) waitReady(proc localPTY) error {
	launch.closeChildCopies()
	if err := launch.ready.SetReadDeadline(launch.deadline); err != nil {
		return errNativeLaunch
	}
	ready, descriptors, err := receiveNativeStatus(launch.ready)
	if err != nil {
		return errNativeLaunch
	}
	if runtime.GOOS == "darwin" {
		if ready.Status != "needs-profile" || len(descriptors) != 2 {
			closeNativeDescriptors(descriptors)
			return errNativeLaunch
		}
		if relayErr := relaySeatbeltProfile(descriptors, launch.deadline); relayErr != nil {
			return errNativeLaunch
		}
		ready, descriptors, err = receiveNativeStatus(launch.ready)
	}
	if err != nil || ready.Status != "ready" {
		closeNativeDescriptors(descriptors)
		return errNativeLaunch
	}
	switch ready.Observer {
	case sandbox.ObserverActive, sandbox.ObserverUnavailable, sandbox.ObserverUnsupported, sandbox.ObserverFailed:
	default:
		closeNativeDescriptors(descriptors)
		return errNativeLaunch
	}
	if launch.prepared.Policy.Backend == sandbox.LinuxLandlock && ready.Observer == sandbox.ObserverActive {
		if len(descriptors) != 1 {
			closeNativeDescriptors(descriptors)
			return errNativeLaunch
		}
		launch.listener = os.NewFile(uintptr(descriptors[0]), "native-observer")
	} else if len(descriptors) != 0 {
		closeNativeDescriptors(descriptors)
		return errNativeLaunch
	}
	launch.observer = ready.Observer
	if err = launch.errorReader.SetReadDeadline(launch.deadline); err != nil {
		return errNativeLaunch
	}
	failure, err := io.ReadAll(io.LimitReader(launch.errorReader, 1025))
	if err != nil || len(failure) != 0 {
		return errNativeLaunch
	}
	select {
	case <-proc.Done():
		return errNativeLaunch
	default:
	}
	if launch.probePath != "" {
		if err := os.Remove(launch.probePath); err != nil && !os.IsNotExist(err) {
			return errNativeLaunch
		}
		launch.probePath = ""
	}
	return launch.prepared.Close()
}

// Received descriptors become CLOEXEC while holding ForkLock. Holding that
// lock only for a nonblocking recvmsg avoids leaking a private profile into a
// concurrent ordinary shell without stalling unrelated launches on netpoll.
func receiveNativeStatus(connection *net.UnixConn) (sandbox.RunnerStatus, []int, error) {
	var status sandbox.RunnerStatus
	raw, err := connection.SyscallConn()
	if err != nil {
		return status, nil, errNativeLaunch
	}
	var packet [1024]byte
	var ancillary [256]byte
	var n, control, flags int
	var descriptors []int
	var receiveErr error
	err = raw.Read(func(fd uintptr) bool {
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		n, control, flags, _, receiveErr = unix.Recvmsg(int(fd), packet[:], ancillary[:], 0)
		if receiveErr == unix.EAGAIN || receiveErr == unix.EWOULDBLOCK || receiveErr == unix.EINTR {
			return false
		}
		if receiveErr != nil {
			return true
		}
		messages, parseErr := unix.ParseSocketControlMessage(ancillary[:control])
		if parseErr != nil {
			receiveErr = parseErr
			return true
		}
		for _, message := range messages {
			rights, rightsErr := unix.ParseUnixRights(&message)
			if rightsErr != nil {
				receiveErr = rightsErr
				continue
			}
			descriptors = append(descriptors, rights...)
			for _, descriptor := range rights {
				if _, flagErr := unix.FcntlInt(uintptr(descriptor), unix.F_SETFD, unix.FD_CLOEXEC); flagErr != nil {
					receiveErr = flagErr
				}
			}
		}
		if receiveErr != nil {
			closeNativeDescriptors(descriptors)
			descriptors = nil
		}
		return true
	})
	if err != nil || receiveErr != nil || n <= 0 || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		closeNativeDescriptors(descriptors)
		return status, nil, errNativeLaunch
	}
	decoder := json.NewDecoder(bytes.NewReader(packet[:n]))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&status) != nil || status.Version != sandbox.RunnerReadyVersion || status.Code != "" || decoder.Decode(&trailing) != io.EOF {
		closeNativeDescriptors(descriptors)
		return status, nil, errNativeLaunch
	}
	return status, descriptors, nil
}

func closeNativeDescriptors(descriptors []int) {
	for _, descriptor := range descriptors {
		_ = unix.Close(descriptor)
	}
}

// sandbox-exec accepts an anonymous pipe but resolves an unlinked regular
// profile's old basename rather than consuming its inherited file descriptor.
// Relay the bounded source through the existing private startup channel. There
// is no readable profile pathname or additional long-lived producer process.
func relaySeatbeltProfile(descriptors []int, deadline time.Time) error {
	defer func() { closeNativeDescriptors(descriptors) }()
	if len(descriptors) != 2 {
		return errNativeLaunch
	}
	var source, target unix.Stat_t
	if unix.Fstat(descriptors[0], &source) != nil || unix.Fstat(descriptors[1], &target) != nil ||
		source.Mode&unix.S_IFMT != unix.S_IFREG || source.Mode&0o777 != 0o600 ||
		int64(source.Uid) != int64(os.Getuid()) || source.Size <= 0 || source.Size > sandbox.MaxRunnerPlanBytes ||
		target.Mode&unix.S_IFMT != unix.S_IFIFO {
		return errNativeLaunch
	}
	if _, err := unix.Seek(descriptors[0], 0, io.SeekStart); err != nil {
		return errNativeLaunch
	}
	if err := unix.SetNonblock(descriptors[1], true); err != nil {
		return errNativeLaunch
	}
	reader := os.NewFile(uintptr(descriptors[0]), "sandbox-profile-source")
	writer := os.NewFile(uintptr(descriptors[1]), "sandbox-profile-pipe")
	// os.File owns these handles from here, including when a deadline fails.
	descriptors = nil
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	if err := writer.SetWriteDeadline(deadline); err != nil {
		return errNativeLaunch
	}
	if _, err := io.CopyN(writer, reader, source.Size); err != nil {
		return errNativeLaunch
	}
	return nil
}

// abortNative observes actual process death before removing its runtime. A
// process that cannot yet be reaped retains that tree; the preparation ticket
// remains terminal, and the helper refuses further native launches meanwhile.
func abortNative(proc localPTY, prepared *sandbox.Prepared) error {
	_ = proc.SignalProcessGroup(proc.Pid(), syscall.SIGKILL)
	_ = proc.Close()
	select {
	case <-proc.Done():
		return prepared.Cleanup()
	case <-time.After(5 * time.Second):
		return errNativeClosePending
	}
}
