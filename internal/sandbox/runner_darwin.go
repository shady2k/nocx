//go:build darwin

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const seatbeltShimArg = "--nocx-seatbelt-shim"

// RunRunner keeps the full native transition on one OS thread. The first stage
// compiles the private profile, then sandbox-exec re-enters this executable as
// a minimal shim. The shim proves policy effects before acknowledging readiness.
func RunRunner() int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if len(os.Args) > 1 && os.Args[1] == seatbeltShimArg {
		return runSeatbeltShim()
	}
	planCopy, err := unix.FcntlInt(RunnerFDPlan, unix.F_DUPFD_CLOEXEC, RunnerFDRoots)
	if err != nil {
		return runnerFailure("plan-copy")
	}
	plan, err := ReadRunnerPlan(RunnerFDPlan)
	if err != nil {
		return runnerFailure("invalid-plan")
	}
	if err := validateDarwinRunner(plan, true); err != nil {
		return runnerFailure("invalid-channels")
	}
	if _, err := unix.Seek(planCopy, 0, 0); err != nil {
		return runnerFailure("plan-copy")
	}
	if err := clearCloseOnExec(planCopy); err != nil {
		return runnerFailure("plan-copy")
	}
	profileText, err := CompileSeatbeltProfile(plan.Policy, plan.ObserverNonce)
	if err != nil {
		return runnerFailure("profile-invalid")
	}
	profile, err := privateSeatbeltProfile(plan.Policy.Runtime.Root, profileText)
	if err != nil {
		return runnerFailure("profile-create")
	}
	profileReader, profileWriter, err := os.Pipe()
	if err != nil {
		_ = profile.Close()
		return runnerFailure("profile-pipe")
	}
	request, err := json.Marshal(RunnerStatus{Version: RunnerReadyVersion, Status: "needs-profile"})
	if err != nil {
		return runnerFailure("profile-request")
	}
	control := unix.UnixRights(int(profile.Fd()), int(profileWriter.Fd()))
	sent, err := unix.SendmsgN(RunnerFDReady, request, control, nil, 0)
	_ = profile.Close()
	_ = profileWriter.Close()
	if err != nil || sent != len(request) {
		_ = profileReader.Close()
		return runnerFailure("profile-handoff")
	}
	profileFD := int(profileReader.Fd())
	if profileFD < RunnerFDRoots {
		// ReadRunnerPlan closes FD5; keep the pipe outside protocol slots.
		profileFD, err = unix.FcntlInt(profileReader.Fd(), unix.F_DUPFD_CLOEXEC, RunnerFDRoots)
		_ = profileReader.Close()
		if err != nil {
			return runnerFailure("profile-descriptor")
		}
	}
	if err := clearCloseOnExec(profileFD); err != nil {
		_ = unix.Close(profileFD)
		return runnerFailure("profile-descriptor")
	}
	if err := unix.Fchdir(plan.WorkspaceFD); err != nil {
		return runnerFailure("workspace-unavailable")
	}
	profilePath := "/dev/fd/" + strconv.Itoa(profileFD)
	args := []string{seatbeltExecutable, "-f", profilePath, plan.Policy.Runner, seatbeltShimArg, strconv.Itoa(planCopy), strconv.Itoa(profileFD)}
	if err := syscall.Exec(seatbeltExecutable, args, os.Environ()); err != nil {
		_ = unix.Close(profileFD)
		return runnerFailure("seatbelt-exec")
	}
	return 0
}

func CompileSeatbeltProfile(policy Policy, observerNonce string) (string, error) {
	if policy.Backend != MacOSSeatbelt || policy.BackendVersion != MacOSBaselineVersion || policy.Version != PolicyVersion || len(policy.Roots) == 0 || len(policy.Roots) > MaxEffectiveRoots || !canonicalRunnerPath(policy.Runtime.Root) || policy.Runtime.Root == "/" || !validObserverNonce(observerNonce) {
		return "", errors.New("unsupported Seatbelt policy")
	}
	for _, root := range policy.Roots {
		if !canonicalRunnerPath(root.Path) {
			return "", errors.New("invalid root path")
		}
		switch root.Kind {
		case DirectoryRoot:
			if root.Access != ReadOnly && root.Access != ReadWrite {
				return "", errors.New("invalid directory access")
			}
		case ArtifactRoot:
			if root.Access != ReadOnly {
				return "", errors.New("writable artifact")
			}
		case DeviceRoot:
			if root.Access != ReadWrite || root.Provenance != WritableDeviceRoot {
				return "", errors.New("invalid device root")
			}
		default:
			return "", errors.New("invalid root kind")
		}
	}
	var b strings.Builder
	b.Grow(512 + len(policy.Roots)*160)
	// The private nonce annotates Seatbelt denials for the helper's log stream.
	// It is launch metadata, deliberately excluded from Policy and its digest.
	b.WriteString("(version 1)\n(deny default (with message \"" + observerNonce + "\"))\n")
	for _, operation := range [...]string{"file-read*", "file-write*", "file-ioctl"} {
		if err := writeSeatbeltFilesystemBoundary(&b, operation, policy); err != nil {
			return "", err
		}
	}
	if err := writeSeatbeltUnixBoundary(&b, policy.Runtime.Root); err != nil {
		return "", err
	}
	if b.Len() > MaxRunnerPlanBytes {
		return "", errors.New("Seatbelt profile exceeds bound")
	}
	return b.String(), nil
}

func validObserverNonce(nonce string) bool {
	if len(nonce) != 32 {
		return false
	}
	for _, c := range nonce {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func writeSeatbeltFilesystemBoundary(b *strings.Builder, operation string, policy Policy) error {
	var allowed strings.Builder
	add := func(filter, path string) error {
		quoted, err := quoteSeatbeltString(path)
		if err != nil {
			return err
		}
		allowed.WriteString(" (")
		allowed.WriteString(filter)
		allowed.WriteByte(' ')
		allowed.WriteString(quoted)
		allowed.WriteByte(')')
		return nil
	}
	if operation != "file-ioctl" {
		if err := add("subpath", policy.Runtime.Root); err != nil {
			return err
		}
	}
	for _, root := range policy.Roots {
		if operation == "file-write*" && root.Access != ReadWrite || operation == "file-ioctl" && root.Kind != DeviceRoot {
			continue
		}
		filter := "literal"
		if root.Kind == DirectoryRoot {
			filter = "subpath"
		}
		if err := add(filter, root.Path); err != nil {
			return err
		}
	}
	if operation == "file-read*" {
		// Seatbelt checks directory reads during path traversal, including "/".
		// Exact ancestor literals permit traversal, not descendant file data.
		ancestors := make(map[string]struct{})
		collect := func(path string) {
			// macOS exposes these system ancestors through lexical symlinks.
			// Permit the link inode for traversal, never its whole target tree.
			if private, ok := strings.CutPrefix(path, "/private/"); ok {
				component, _, _ := strings.Cut(private, "/")
				switch component {
				case "tmp", "var", "etc":
					ancestors["/"+component] = struct{}{}
				}
			}
			for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
				if _, exists := ancestors[parent]; exists {
					return
				}
				ancestors[parent] = struct{}{}
				if parent == "/" {
					return
				}
			}
		}
		collect(policy.Runtime.Root)
		for _, root := range policy.Roots {
			collect(root.Path)
		}
		paths := make([]string, 0, len(ancestors))
		for path := range ancestors {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		for _, path := range paths {
			if err := add("literal", path); err != nil {
				return err
			}
		}
	}
	if operation == "file-ioctl" {
		// Preserve the explicitly inherited PTY, without granting host PTYs.
		var tty unix.Stat_t
		if unix.Fstat(0, &tty) == nil && tty.Mode&unix.S_IFMT == unix.S_IFCHR {
			if path, err := descriptorActual(0); err == nil {
				if err := add("literal", path); err != nil {
					return err
				}
			}
		}
	}
	b.WriteString("(deny ")
	b.WriteString(operation)
	if allowed.Len() != 0 {
		b.WriteString(" (require-not (require-any")
		b.WriteString(allowed.String())
		b.WriteString("))")
	}
	b.WriteString(")\n")
	return nil
}

func writeSeatbeltUnixBoundary(b *strings.Builder, runtimeRoot string) error {
	if !canonicalRunnerPath(runtimeRoot) {
		return errors.New("invalid runtime path")
	}
	quoted, err := quoteSeatbeltString(runtimeRoot)
	if err != nil {
		return err
	}
	b.WriteString("(deny network-outbound (require-all (remote unix-socket) (require-not (remote unix-socket (subpath " + quoted + ")))))\n")
	b.WriteString("(deny network-bind (require-all (local unix-socket) (require-not (local unix-socket (subpath " + quoted + ")))))\n")
	return nil
}

func quoteSeatbeltString(value string) (string, error) {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n', '\r', 0:
			return "", errors.New("invalid path character")
		case '\t':
			b.WriteString("\\x09;")
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, "\\x%02x;", r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

func validateDarwinRunner(plan RunnerPlan, verifyProbe bool) error {
	if plan.Policy.Backend != MacOSSeatbelt || plan.Policy.BackendVersion != MacOSBaselineVersion || len(plan.ProbePath) == 0 || len(plan.ProbePath) > 80 {
		return runnerErr("unsupported-backend")
	}
	typ, err := unix.GetsockoptInt(RunnerFDReady, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_DGRAM {
		return runnerErr("readiness-channel")
	}
	address, err := unix.Getsockname(RunnerFDReady)
	if err != nil {
		return runnerErr("readiness-channel")
	}
	if _, ok := address.(*unix.SockaddrUnix); !ok {
		return runnerErr("readiness-channel")
	}
	var st unix.Stat_t
	if err := unix.Fstat(RunnerFDError, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
		return runnerErr("error-channel")
	}
	for fd := 0; fd <= 2; fd++ {
		if err := unix.Fstat(fd, &st); err != nil {
			return runnerErr("stdio")
		}
	}
	for _, fd := range plan.KeepFDs {
		if err := unix.Fstat(fd, &st); err != nil {
			return runnerErr("keep-fd")
		}
	}
	if err := validatePinnedObjects(plan); err != nil {
		return err
	}
	if verifyProbe {
		probe, err := os.Lstat(plan.ProbePath)
		if err != nil || !probe.Mode().IsRegular() || probe.Mode().Perm()&0o077 != 0 || probe.Size() > 1 {
			return runnerErr("probe-unavailable")
		}
	}
	return nil
}

func validatePinnedObjects(plan RunnerPlan) error {
	for i, root := range plan.Policy.Roots {
		fd := plan.RootFDs[i]
		st, statErr := statPinnedDarwin(fd, root.Path, root.Kind)
		if statErr != nil {
			return runnerErr("root-fd")
		}
		if uint64(st.Dev) != root.Identity.Device || st.Ino != root.Identity.Inode {
			return runnerErr("root-identity")
		}
		if uint32(st.Mode&unix.S_IFMT) != mapRootType(root.Kind) {
			return runnerErr("root-type")
		}
		var actual string
		var err error
		if root.Kind == DeviceRoot {
			var parent string
			parent, err = descriptorActual(fd)
			actual = filepath.Join(parent, filepath.Base(root.Path))
		} else {
			actual, err = canonicalActual(root.Path)
		}
		pathIdentity, identityErr := identity(root.Path)
		if err != nil || identityErr != nil || actual != root.Path || pathIdentity != root.Identity {
			return runnerErr("root-path-identity")
		}
	}
	var cwd unix.Stat_t
	if err := unix.Fstat(plan.WorkspaceFD, &cwd); err != nil || cwd.Mode&unix.S_IFMT != unix.S_IFDIR {
		return runnerErr("workspace-fd")
	}
	if !hasWorkspaceIdentity(plan.Policy, cwd) {
		return runnerErr("workspace-identity")
	}
	actual, err := canonicalActual(plan.Policy.WorkspaceRoot)
	pathIdentity, identityErr := identity(plan.Policy.WorkspaceRoot)
	if err != nil || identityErr != nil || actual != plan.Policy.WorkspaceRoot || pathIdentity.Device != uint64(cwd.Dev) || pathIdentity.Inode != cwd.Ino {
		return runnerErr("workspace-path-identity")
	}
	return nil
}

func mapRootType(kind RootKind) uint32 {
	switch kind {
	case DirectoryRoot:
		return unix.S_IFDIR
	case ArtifactRoot:
		return unix.S_IFREG
	case DeviceRoot:
		return unix.S_IFCHR
	default:
		return 0
	}
}

func hasWorkspaceIdentity(policy Policy, st unix.Stat_t) bool {
	for _, root := range policy.Roots {
		if root.Path == policy.WorkspaceRoot && root.Provenance == WorkspaceRoot && root.Access == ReadWrite && root.Kind == DirectoryRoot {
			return uint64(st.Dev) == root.Identity.Device && st.Ino == root.Identity.Inode
		}
	}
	return false
}

func runSeatbeltShim() int {
	if len(os.Args) != 4 {
		return runnerFailure("shim-arguments")
	}
	planFD, err := strconv.Atoi(os.Args[2])
	if err != nil || planFD < 8 {
		return runnerFailure("shim-plan-fd")
	}
	profileFD, err := strconv.Atoi(os.Args[3])
	if err != nil || profileFD < 8 || profileFD == planFD {
		return runnerFailure("shim-profile-fd")
	}
	plan, err := ReadRunnerPlan(planFD)
	if err != nil || validateRunnerPlan(plan) != nil || validateDarwinRunner(plan, false) != nil {
		return runnerFailure("shim-plan")
	}
	if err := unix.Close(profileFD); err != nil {
		return runnerFailure("shim-profile-close")
	}
	if err := proveSeatbeltApplied(plan); err != nil {
		return runnerFailure("seatbelt-not-applied")
	}
	if err := unix.Fchdir(plan.WorkspaceFD); err != nil {
		return runnerFailure("workspace-unavailable")
	}
	if err := closeRunnerDescriptors(plan); err != nil {
		return runnerFailure("descriptor-cleanup")
	}
	if err := sendRunnerPacket(RunnerStatus{Version: RunnerReadyVersion, Status: "ready", Observer: ObserverUnavailable}); err != nil {
		return runnerFailure("readiness-channel")
	}
	if err := unix.Close(RunnerFDReady); err != nil {
		writeRunnerFailure("readiness-close")
		return 125
	}
	if err := syscall.Exec(plan.Policy.Shell, plan.Args, os.Environ()); err != nil {
		writeRunnerFailure("exec-failed")
		return 125
	}
	return 0
}

func proveSeatbeltApplied(plan RunnerPlan) error {
	fd, err := unix.Open(plan.ProbePath, unix.O_WRONLY|unix.O_APPEND|unix.O_CLOEXEC, 0)
	if err == nil {
		_ = unix.Close(fd)
		return errors.New("write unexpectedly permitted")
	}
	if err != unix.EACCES && err != unix.EPERM {
		return errors.New("write probe inconclusive")
	}
	f, err := os.CreateTemp(plan.Policy.Runtime.Root, ".seatbelt-write-")
	if err != nil {
		return errors.New("runtime write unavailable")
	}
	name := f.Name()
	if _, err = f.Write([]byte("ok")); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return errors.New("runtime write unavailable")
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(name)
		return errors.New("runtime write unavailable")
	}
	if err = os.Remove(name); err != nil {
		return errors.New("runtime cleanup unavailable")
	}
	if err = proveUnixSocketBoundary(plan.Policy.Runtime.Root, plan.ProbePath); err != nil {
		return err
	}
	probe, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return errors.New("outbound Unix probe unavailable")
	}
	defer func() { _ = unix.Close(probe) }()
	unix.CloseOnExec(probe)
	if err = unix.Connect(probe, &unix.SockaddrUnix{Name: plan.ProbeSocket}); err != unix.EACCES && err != unix.EPERM {
		return errors.New("host-created workspace Unix endpoint not denied")
	}
	return nil
}

func proveUnixSocketBoundary(runtimeRoot, outsidePath string) error {
	outside := outsidePath + ".sock"
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	unix.CloseOnExec(fd)
	err = unix.Bind(fd, &unix.SockaddrUnix{Name: outside})
	_ = unix.Close(fd)
	if err == nil {
		_ = unix.Unlink(outside)
		return errors.New("outside Unix bind unexpectedly permitted")
	}
	if err != unix.EACCES && err != unix.EPERM {
		return errors.New("outside Unix bind inconclusive")
	}
	runtimeSocket := runtimeRoot + "/.seatbelt-probe.sock"
	server, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	unix.CloseOnExec(server)
	defer func() { _ = unix.Close(server) }()
	if err = unix.Bind(server, &unix.SockaddrUnix{Name: runtimeSocket}); err != nil {
		return errors.New("runtime Unix bind unavailable")
	}
	defer func() { _ = unix.Unlink(runtimeSocket) }()
	if err = unix.Listen(server, 1); err != nil {
		return errors.New("runtime Unix listen unavailable")
	}
	client, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	unix.CloseOnExec(client)
	defer func() { _ = unix.Close(client) }()
	if err = unix.Connect(client, &unix.SockaddrUnix{Name: runtimeSocket}); err != nil {
		return errors.New("runtime Unix connect unavailable")
	}
	accepted, _, err := unix.Accept(server)
	if err != nil {
		return errors.New("runtime Unix accept unavailable")
	}
	_ = unix.Close(accepted)
	return nil
}

func closeRunnerDescriptors(plan RunnerPlan) error {
	keep := map[int]bool{}
	for _, fd := range plan.KeepFDs {
		keep[fd] = true
	}
	for _, fd := range []int{3, 4} {
		if keep[fd] {
			continue
		}
		if err := unix.Close(fd); err != nil && err != unix.EBADF {
			return err
		}
	}
	if err := setCloseOnExec(RunnerFDError); err != nil {
		return err
	}
	return closeDescriptorsFrom(RunnerFDRoots)
}

func closeDescriptorsFrom(first int) error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil || limit.Cur > 1<<20 {
		return runnerErr("descriptor-limit")
	}
	for fd := first; uint64(fd) < limit.Cur; fd++ {
		if err := unix.Close(fd); err != nil && err != unix.EBADF {
			return runnerErr("descriptor-close")
		}
	}
	return nil
}

func clearCloseOnExec(fd int) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return err
	}
	_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC)
	return err
}

func setCloseOnExec(fd int) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return err
	}
	_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC)
	return err
}

func sendRunnerPacket(packet RunnerStatus) error {
	data, err := json.Marshal(packet)
	if err != nil || len(data) > 256 {
		return runnerErr("readiness-packet")
	}
	n, err := unix.Write(RunnerFDReady, data)
	if err != nil || n != len(data) {
		return runnerErr("readiness-write")
	}
	return nil
}

func writeRunnerFailure(code string) {
	data, _ := json.Marshal(RunnerFailure{Version: RunnerReadyVersion, Code: code})
	if len(data) <= 128 {
		_, _ = unix.Write(RunnerFDError, data)
	}
}

func runnerFailure(code string) int {
	_, _ = unix.Write(RunnerFDReady, mustRunnerError(code))
	writeRunnerFailure(code)
	return 125
}

func mustRunnerError(code string) []byte {
	data, _ := json.Marshal(RunnerStatus{Version: RunnerReadyVersion, Status: "error", Code: code})
	if len(data) > 256 {
		return nil
	}
	return data
}
