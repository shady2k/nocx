package sandbox

import (
	"fmt"
	"os"
	"strconv"
)

// seatbeltHelperArg is the internal argv marker the macOS shim checks for
// (design spec §7.2): sandbox-exec execs the nocx executable as the shim,
// which writes the readiness byte and unix.Execs the real shell.
const seatbeltHelperArg = "__sandbox-seatbelt-exec"

// seatbeltShimExit is the process exit code the shim uses when it cannot
// report readiness or exec the shell (mirrors the Linux helper's 126).
const seatbeltShimExit = 126

// execFunc replaces the current process image with the sandboxed shell.
type execFunc func(argv0 string, argv []string, envv []string) error

// parseSeatbeltShimArgv parses the fixed internal shim argv (design spec
// §7.2): [exe, __sandbox-seatbelt-exec, <status-fd>, <shell>, <shell-args…>].
// ok=false means the process was not re-exec'd as the shim. When ok=true and
// errCode is nonzero, the marker matched but the invocation is malformed — the
// caller must exit with errCode rather than running the app.
func parseSeatbeltShimArgv(args []string) (statusFD int, shell string, shellArgs []string, ok bool, errCode int) {
	if len(args) < 2 || args[1] != seatbeltHelperArg {
		return 0, "", nil, false, 0
	}
	if len(args) < 4 || args[3] == "" {
		return 0, "", nil, true, seatbeltShimExit
	}
	fd, err := strconv.Atoi(args[2])
	if err != nil || fd < 3 {
		return 0, "", nil, true, seatbeltShimExit
	}
	return fd, args[3], args[4:], true, 0
}

// seatbeltShimMain is the platform-neutral macOS post-profile shim body. It
// writes the ready byte to the inherited status pipe and then replaces itself
// with the real shell. exec is unix.Exec in production (wired by the darwin
// MaybeHelper); tests inject a recorder. A profile rejected by sandbox-exec
// never reaches here and EOFs the pipe instead, so readiness is acknowledged
// only after Seatbelt applied.
func seatbeltShimMain(statusFD int, shell string, shellArgs []string, exec execFunc) int {
	statusFile := os.NewFile(uintptr(statusFD), "status")
	if _, err := statusFile.Write([]byte{0}); err != nil {
		_ = statusFile.Close()
		return seatbeltShimExit
	}
	_ = statusFile.Close()

	args := append([]string{shell}, shellArgs...)
	if err := exec(shell, args, os.Environ()); err != nil {
		// Path-free: user paths never enter status or stderr diagnostics.
		fmt.Fprintln(os.Stderr, "sandbox seatbelt shim: exec failed")
		return seatbeltShimExit
	}
	return seatbeltShimExit // unreachable
}
