//go:build !windows

package sandbox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// envProbe selects the probe mode of the child test binary on both the
// Linux (Landlock) and macOS (Seatbelt) enforcement suites: the probe runs
// INSIDE the enforced cage and reports a machine-checked verdict.
const envProbe = "NOCX_SANDBOX_PROBE"

// runProbe executes the behavior assertions inside the enforced cage. It
// exits 0 only when every assertion holds, printing OK/FAIL lines for the
// parent to surface on failure.
func runProbe() int {
	failures := 0
	fail := func(format string, args ...any) {
		failures++
		fmt.Printf("FAIL: "+format+"\n", args...)
	}
	ok := func(format string, args ...any) {
		fmt.Printf("OK: "+format+"\n", args...)
	}

	w := os.Getenv("NOCX_SB_WORKSPACE")
	sentinel := os.Getenv("NOCX_SB_SENTINEL")
	preHard := os.Getenv("NOCX_SB_PREHARD")
	home := os.Getenv("NOCX_SB_HOME")
	tmp := os.Getenv("NOCX_SB_TMP")
	if w == "" || sentinel == "" || preHard == "" || home == "" || tmp == "" {
		fmt.Printf("FAIL: probe missing fixture env\n")
		return 1
	}

	// Writable roots: create, truncate, rename.
	f := filepath.Join(w, "a.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		fail("create in workspace: %v", err)
	} else {
		ok("create in workspace")
	}
	if err := os.WriteFile(f, []byte("longer content"), 0o600); err != nil {
		fail("truncate-rewrite in workspace: %v", err)
	} else {
		ok("truncate-rewrite in workspace")
	}
	if err := os.Rename(f, filepath.Join(w, "b.txt")); err != nil {
		fail("rename in workspace: %v", err)
	} else {
		ok("rename in workspace")
	}
	if err := os.WriteFile(filepath.Join(home, "f"), []byte("h"), 0o600); err != nil {
		fail("create in runtime home: %v", err)
	} else {
		ok("create in runtime home")
	}
	if err := os.WriteFile(filepath.Join(tmp, "f"), []byte("t"), 0o600); err != nil {
		fail("create in runtime tmp: %v", err)
	} else {
		ok("create in runtime tmp")
	}

	// Read-only system root.
	if _, err := os.ReadFile("/etc/hostname"); err != nil {
		fail("read /etc/hostname: %v", err)
	} else {
		ok("read system root")
	}

	// A usable shell is a positive requirement, not an inferred side effect of
	// denial assertions. The pipeline exercises executable lookup and the
	// redirect exercises the finite writable /dev device allowlist.
	shell := os.Getenv("NOCX_SB_SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	pipeline := exec.Command(shell, "-c", "printf sandbox | cat") //nolint:gosec // shell path is supplied by the parent-side probe fixture
	if output, err := pipeline.Output(); err != nil || string(output) != "sandbox" {
		fail("shell pipeline: output=%q err=%v", output, err)
	} else {
		ok("shell pipeline")
	}
	redirect := exec.Command(shell, "-c", "printf sandbox >/dev/null") //nolint:gosec // shell path is supplied by the parent-side probe fixture
	if output, err := redirect.CombinedOutput(); err != nil {
		fail("shell redirect to /dev/null: output=%q err=%v", output, err)
	} else {
		ok("shell redirect to /dev/null")
	}

	// Sentinel outside all roots: unreadable and unwritable.
	if _, err := os.ReadFile(sentinel); err == nil { //nolint:gosec // probe asserts the cage denies
		fail("read of sentinel outside roots succeeded")
	} else {
		ok("sentinel unreadable")
	}
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err == nil {
		fail("write to sentinel outside roots succeeded")
	} else {
		ok("sentinel unwritable")
	}

	// Symlink escape: resolution is path-checked, the target is outside.
	link := filepath.Join(w, "escape")
	if err := os.Symlink(sentinel, link); err != nil {
		fail("create symlink in workspace: %v", err)
	}
	if _, err := os.ReadFile(link); err == nil { //nolint:gosec // probe asserts the cage denies
		fail("symlink escape read succeeded")
	} else {
		ok("symlink escape blocked")
	}

	// Hard-link creation to an outside file is denied at link(2).
	if err := os.Link(sentinel, filepath.Join(w, "hard")); err == nil {
		fail("hard link to sentinel created")
	} else {
		ok("hard-link creation to outside file denied")
	}

	// Renaming an outside file into the workspace needs REFER on the source
	// hierarchy, which is outside the roots.
	if err := os.Rename(sentinel, filepath.Join(w, "moved")); err == nil {
		fail("rename of sentinel into workspace succeeded")
	} else {
		ok("rename of sentinel blocked")
	}

	// Subprocesses inherit the domain. Capture output instead of redirecting:
	// a failed redirect must not masquerade as a denied sentinel read.
	sub := exec.Command(shell, "-c", "cat \"$1\"", "sandbox-probe", sentinel) //nolint:gosec // probe asserts the cage denies the escape
	if output, err := sub.CombinedOutput(); err == nil {
		fail("subprocess read the sentinel: output=%q", output)
	} else if strings.Contains(string(output), "top secret") {
		fail("subprocess exposed sentinel content: output=%q", output)
	} else {
		ok("subprocess blocked from sentinel")
	}

	// Pre-existing hard link inside a writable root: documented limitation,
	// reachable through the in-root path (hierarchy-based rules, not inode).
	if _, err := os.ReadFile(preHard); err != nil { //nolint:gosec // probe asserts the documented limitation
		fail("pre-existing hard link unreadable: %v", err)
	} else {
		ok("pre-existing hard link reachable (documented limitation)")
	}

	// Outbound TCP (loopback): network is outside the contract and must work.
	ln, lErr := net.Listen("tcp", "127.0.0.1:0")
	if lErr != nil {
		fail("tcp listen: %v", lErr)
	} else {
		ok("tcp listen (loopback)")
		addr := ln.Addr().String()
		conn, dErr := net.Dial("tcp", addr)
		if dErr != nil {
			fail("tcp connect: %v", dErr)
		} else {
			ok("tcp connect (loopback)")
			_ = conn.Close()
		}
		_ = ln.Close()
	}

	// Helper-internal env must never reach the shell.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, helperEnvPrefix) {
			fail("helper env leaked into shell env: %q", kv)
		}
	}

	if failures > 0 {
		fmt.Printf("PROBE RESULT: %d failures\n", failures)
		return 1
	}
	fmt.Printf("PROBE RESULT: ok\n")
	return 0
}
