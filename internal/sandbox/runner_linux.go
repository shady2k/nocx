//go:build linux

package sandbox

import (
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"syscall"

	landlock "github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

// RunRunner is the Linux-only entrypoint used by cmd/nocx-sandbox-runner.
// It never parses command-line arguments or consults environment variables.
func RunRunner() int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	plan, err := ReadRunnerPlan(RunnerFDPlan)
	if err != nil {
		return runnerFailure("invalid-plan")
	}
	if err := validateRunnerChannels(plan); err != nil {
		return runnerFailure("invalid-channels")
	}
	if err := validatePinnedObjects(plan); err != nil {
		return runnerFailure("object-mismatch")
	}
	if err := unix.Fchdir(plan.WorkspaceFD); err != nil {
		return runnerFailure("workspace-unavailable")
	}
	rules := make([]landlock.Rule, 0, len(plan.Policy.Roots))
	for i, root := range plan.Policy.Roots {
		path := fdPath(plan.RootFDs[i])
		switch root.Kind {
		case DirectoryRoot:
			if root.Access == ReadOnly {
				rules = append(rules, landlock.RODirs(path))
			} else {
				rules = append(rules, landlock.RWDirs(path).WithRefer())
			}
		case ArtifactRoot:
			rules = append(rules, landlock.ROFiles(path))
		case DeviceRoot:
			rule := landlock.RWFiles(path).WithIoctlDev()
			if root.Access == ReadOnly {
				rule = landlock.ROFiles(path)
			}
			rules = append(rules, rule)
		default:
			return runnerFailure("invalid-root-kind")
		}
	}
	// V9 handles pathname Unix resolution but no root is granted WithResolveUnix.
	// RestrictPaths intentionally excludes network and scoped permissions.
	if err := landlock.V9.RestrictPaths(rules...); err != nil {
		return runnerFailure("landlock-unsupported")
	}
	if err := sendRunnerPacket(RunnerReady{Version: RunnerReadyVersion, Status: "ready"}); err != nil {
		return runnerFailure("readiness-channel")
	}
	if err := prepareExecDescriptors(plan); err != nil {
		return runnerFailure("descriptor-cleanup")
	}
	// #nosec G204 -- helper-authoritative shell/arguments; private plan digest and pinned executable identity verified above.
	if err := syscall.Exec(plan.Policy.Shell, plan.Args, os.Environ()); err != nil {
		writeRunnerFailure("exec-failed")
		return 126
	}
	return 0
}

func validateRunnerChannels(plan RunnerPlan) error {
	typ, err := unix.GetsockoptInt(RunnerFDReady, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_SEQPACKET {
		return runnerErr("readiness-channel")
	}
	addr, err := unix.Getsockname(RunnerFDReady)
	if err != nil {
		return runnerErr("readiness-channel")
	}
	if _, ok := addr.(*unix.SockaddrUnix); !ok {
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
	return nil
}

func validatePinnedObjects(plan RunnerPlan) error {
	for i, root := range plan.Policy.Roots {
		fd := plan.RootFDs[i]
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_PATH == 0 {
			return runnerErr("root-not-path-fd")
		}
		if st.Dev != root.Identity.Device || st.Ino != root.Identity.Inode {
			return runnerErr("root-identity")
		}
		switch root.Kind {
		case DirectoryRoot:
			if st.Mode&unix.S_IFMT != unix.S_IFDIR {
				return runnerErr("root-type")
			}
		case ArtifactRoot:
			if st.Mode&unix.S_IFMT != unix.S_IFREG || root.Access != ReadOnly || (root.Provenance != TrustedArtifactRoot && root.Provenance != DependencyRoot) {
				return runnerErr("artifact-type")
			}
		case DeviceRoot:
			if st.Mode&unix.S_IFMT != unix.S_IFCHR && st.Mode&unix.S_IFMT != unix.S_IFBLK {
				return runnerErr("device-type")
			}
			if root.Access != ReadWrite || root.Provenance != WritableDeviceRoot {
				return runnerErr("device-access")
			}
		default:
			return runnerErr("root-kind")
		}
	}
	var cwd unix.Stat_t
	if err := unix.Fstat(plan.WorkspaceFD, &cwd); err != nil {
		return err
	}
	if cwd.Mode&unix.S_IFMT != unix.S_IFDIR {
		return runnerErr("workspace-type")
	}
	flags, err := unix.FcntlInt(uintptr(plan.WorkspaceFD), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_PATH == 0 {
		return runnerErr("workspace-not-path-fd")
	}
	bound := false
	for _, root := range plan.Policy.Roots {
		if root.Path == plan.Policy.WorkspaceRoot && root.Provenance == WorkspaceRoot && root.Kind == DirectoryRoot && root.Access == ReadWrite {
			if cwd.Dev != root.Identity.Device || cwd.Ino != root.Identity.Inode {
				return runnerErr("workspace-identity")
			}
			bound = true
		}
	}
	if !bound {
		return runnerErr("workspace-authority")
	}
	return nil
}

func prepareExecDescriptors(plan RunnerPlan) error {
	keep := map[int]bool{}
	for _, fd := range plan.KeepFDs {
		keep[fd] = true
	}
	for _, fd := range []int{3, 4} {
		if !keep[fd] {
			if err := unix.Close(fd); err != nil && err != unix.EBADF {
				return err
			}
		}
	}
	if err := unix.Close(RunnerFDReady); err != nil {
		return err
	}
	unix.CloseOnExec(RunnerFDError)
	// One bounded kernel operation closes every root, workspace and accidental
	// inherited descriptor. Failure is fatal rather than leaking a capability.
	if err := unix.CloseRange(RunnerFDRoots, ^uint(0), 0); err != nil {
		return err
	}
	return nil
}

func fdPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

func sendRunnerPacket(packet RunnerReady) error {
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
	if len(data) > 128 {
		return
	}
	_, _ = unix.Write(RunnerFDError, data)
}

func runnerFailure(code string) int {
	_ = sendRunnerErrorPacket(code)
	writeRunnerFailure(code)
	return 125
}

func sendRunnerErrorPacket(code string) error {
	data, _ := json.Marshal(RunnerStatus{Version: RunnerReadyVersion, Status: "error", Code: code})
	if len(data) > 256 {
		return runnerErr("failure-packet")
	}
	n, err := unix.Write(RunnerFDReady, data)
	if err != nil || n != len(data) {
		return runnerErr("failure-write")
	}
	return nil
}
