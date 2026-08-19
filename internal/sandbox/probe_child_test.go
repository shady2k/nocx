//go:build !windows

package sandbox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// envProbe selects the probe mode of the child test binary on both the
// Linux (Landlock) and macOS (Seatbelt) enforcement suites: the probe runs
// INSIDE the enforced cage and reports a machine-checked verdict.
const envProbe = "NOCX_SANDBOX_PROBE"

const (
	envProbeProjectedReadOnly = "NOCX_SB_PROJ_RO_REL"
	envProbeProjectedWritable = "NOCX_SB_PROJ_RW_REL"
	envProbeProjectedNestedRW = "NOCX_SB_PROJ_NESTED_RW_REL"
	projectedReadOnlyRelative = ".config"
	projectedWritableRelative = ".local/state/tool"
	projectedNestedRWRelative = ".config/tool/state"
)

type projectionProbeFixture struct {
	base           string
	workspace      string
	runtimeRoot    string
	home           string
	tmp            string
	hostHome       string
	sentinel       string
	readOnlyRoot   string
	writableRoot   string
	nestedWritable string
	preHard        string
}

func newProjectionProbeFixture(t *testing.T) projectionProbeFixture {
	t.Helper()
	base := t.TempDir()
	fixture := projectionProbeFixture{
		base:         base,
		workspace:    filepath.Join(base, "workspace"),
		runtimeRoot:  filepath.Join(base, "runtime"),
		hostHome:     filepath.Join(base, "host-home"),
		sentinel:     filepath.Join(base, "sentinel-secret.txt"),
		readOnlyRoot: filepath.Join(base, "host-home", ".config"),
		writableRoot: filepath.Join(base, "host-home", ".local", "state", "tool"),
	}
	fixture.home = filepath.Join(fixture.runtimeRoot, "home")
	fixture.tmp = filepath.Join(fixture.runtimeRoot, "tmp")
	fixture.nestedWritable = filepath.Join(fixture.readOnlyRoot, "tool", "state")
	for _, dir := range []string{
		fixture.workspace,
		fixture.runtimeRoot,
		fixture.home,
		fixture.tmp,
		fixture.hostHome,
		fixture.readOnlyRoot,
		fixture.writableRoot,
		fixture.nestedWritable,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir projection probe fixture: %v", err)
		}
	}
	if err := os.WriteFile(fixture.sentinel, []byte("top secret"), 0o600); err != nil {
		t.Fatalf("write projection probe sentinel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.readOnlyRoot, "keep.txt"), []byte("read-only"), 0o600); err != nil {
		t.Fatalf("write projection probe read-only file: %v", err)
	}
	fixture.preHard = filepath.Join(fixture.workspace, "pre-hard-link")
	if err := os.Link(fixture.sentinel, fixture.preHard); err != nil {
		t.Fatalf("pre-link projection probe sentinel: %v", err)
	}
	return fixture
}

func (f projectionProbeFixture) environment(shell string) []string {
	return append(os.Environ(),
		envProbe+"=1",
		"NOCX_SB_SHELL="+shell,
		"NOCX_SB_WORKSPACE="+f.workspace,
		"NOCX_SB_SENTINEL="+f.sentinel,
		"NOCX_SB_PREHARD="+f.preHard,
		"NOCX_SB_HOME="+f.home,
		"NOCX_SB_TMP="+f.tmp,
		"NOCX_SB_RO_ROOT="+f.readOnlyRoot,
		"NOCX_SB_RW_ROOT="+f.writableRoot,
		envProbeProjectedReadOnly+"="+projectedReadOnlyRelative,
		envProbeProjectedWritable+"="+projectedWritableRelative,
		envProbeProjectedNestedRW+"="+projectedNestedRWRelative,
		"HOME="+f.hostHome,
	)
}

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
	home := os.Getenv("HOME")
	tmp := os.TempDir()
	roRoot := os.Getenv("NOCX_SB_RO_ROOT")
	rwRoot := os.Getenv("NOCX_SB_RW_ROOT")
	projectedReadOnlyRelative := os.Getenv(envProbeProjectedReadOnly)
	projectedWritableRelative := os.Getenv(envProbeProjectedWritable)
	projectedNestedRWRelative := os.Getenv(envProbeProjectedNestedRW)
	if w == "" || sentinel == "" || preHard == "" || home == "" || tmp == "" || roRoot == "" || rwRoot == "" || projectedReadOnlyRelative == "" || projectedWritableRelative == "" || projectedNestedRWRelative == "" {
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
	if _, err := os.ReadFile("/etc/hosts"); err != nil {
		fail("read /etc/hosts: %v", err)
	} else {
		ok("read system root")
	}

	// User read-only root: reading an existing file succeeds; creating,
	// modifying, renaming, and removing there are all denied.
	roFile := filepath.Join(roRoot, "keep.txt")
	if data, err := os.ReadFile(roFile); err != nil || string(data) != "read-only" { //nolint:gosec // probe fixture path is supplied by the trusted parent test
		fail("read in read-only root: err=%v data=%q", err, data)
	} else {
		ok("read in read-only root")
	}
	if err := os.WriteFile(filepath.Join(roRoot, "new.txt"), []byte("x"), 0o600); err == nil {
		fail("create in read-only root succeeded")
	} else {
		ok("create in read-only root denied")
	}
	if err := os.WriteFile(roFile, []byte("mutated"), 0o600); err == nil {
		fail("modify in read-only root succeeded")
	} else {
		ok("modify in read-only root denied")
	}
	if err := os.Rename(roFile, filepath.Join(roRoot, "renamed.txt")); err == nil {
		fail("rename in read-only root succeeded")
	} else {
		ok("rename in read-only root denied")
	}
	if err := os.Remove(roFile); err == nil {
		fail("remove in read-only root succeeded")
	} else {
		ok("remove in read-only root denied")
	}

	// User writable root: creating and reading back both succeed.
	rwFile := filepath.Join(rwRoot, "f.txt")
	if err := os.WriteFile(rwFile, []byte("writable"), 0o600); err != nil {
		fail("create in writable root: %v", err)
	} else {
		ok("create in writable root")
	}
	if data, err := os.ReadFile(rwFile); err != nil || string(data) != "writable" { //nolint:gosec // probe fixture path is supplied by the trusted parent test
		fail("read in writable root: err=%v data=%q", err, data)
	} else {
		ok("read in writable root")
	}
	projectedReadOnly := filepath.Join(home, filepath.FromSlash(projectedReadOnlyRelative))
	projectedWritable := filepath.Join(home, filepath.FromSlash(projectedWritableRelative))
	projectedNestedRW := filepath.Join(home, filepath.FromSlash(projectedNestedRWRelative))
	if os.Getenv("HOME") != home || os.Getenv("XDG_CONFIG_HOME") != projectedReadOnly || filepath.Join(os.Getenv("XDG_STATE_HOME"), "tool") != projectedWritable {
		fail("sandbox HOME/XDG environment does not name the projection forest")
	} else {
		ok("sandbox HOME/XDG environment names the projection forest")
	}
	projectedROFile := filepath.Join(projectedReadOnly, "keep.txt")
	if data, err := os.ReadFile(projectedROFile); err != nil || string(data) != "read-only" { //nolint:gosec // trusted synthetic fixture
		fail("read through projected read-only root: err=%v data=%q", err, data)
	} else {
		ok("read through projected read-only root")
	}
	if err := os.WriteFile(filepath.Join(projectedReadOnly, "projected-new.txt"), []byte("x"), 0o600); err == nil {
		fail("create through projected read-only root succeeded")
	} else {
		ok("create through projected read-only root denied")
	}
	if err := os.WriteFile(projectedROFile, []byte("mutated"), 0o600); err == nil {
		fail("truncate through projected read-only root succeeded")
	} else {
		ok("truncate through projected read-only root denied")
	}
	if err := os.Remove(projectedROFile); err == nil {
		fail("remove through projected read-only root succeeded")
	} else {
		ok("remove through projected read-only root denied")
	}
	projectedRWFile := filepath.Join(projectedWritable, "projected.txt")
	if err := os.WriteFile(projectedRWFile, []byte("created"), 0o600); err != nil {
		fail("create through projected writable root: %v", err)
	} else if err := os.WriteFile(projectedRWFile, []byte("updated"), 0o600); err != nil {
		fail("update through projected writable root: %v", err)
	} else if data, err := os.ReadFile(projectedRWFile); err != nil || string(data) != "updated" { //nolint:gosec // trusted synthetic fixture
		fail("read through projected writable root: err=%v data=%q", err, data)
	} else {
		ok("create and update through projected writable root")
	}
	if err := os.WriteFile(filepath.Join(projectedNestedRW, "nested.txt"), []byte("nested writable"), 0o600); err != nil {
		fail("write through projected RW child below RO ancestor: %v", err)
	} else {
		ok("projected RW child below RO ancestor stays writable")
	}

	replaceProjection := func(target string) error {
		if err := os.Remove(projectedWritable); err != nil {
			return err
		}
		return os.Symlink(target, projectedWritable)
	}
	if err := replaceProjection(sentinel); err != nil {
		fail("retarget projection outside grants: %v", err)
	} else if _, err := os.ReadFile(projectedWritable); err == nil { //nolint:gosec // probe asserts native target denial
		fail("retargeted projection read outside grants succeeded")
	} else {
		ok("retargeted projection outside grants denied")
	}
	if err := replaceProjection(roRoot); err != nil {
		fail("retarget projection to granted read-only root: %v", err)
	} else if err := os.WriteFile(filepath.Join(projectedWritable, "keep.txt"), []byte("mutated"), 0o600); err == nil {
		fail("retargeted projection widened granted read-only root")
	} else {
		ok("retargeted projection keeps granted read-only class")
	}
	if err := replaceProjection(rwRoot); err != nil {
		fail("retarget projection to granted writable root: %v", err)
	} else if err := os.WriteFile(filepath.Join(projectedWritable, "retargeted.txt"), []byte("writable"), 0o600); err != nil {
		fail("retargeted projection lost granted writable class: %v", err)
	} else {
		ok("retargeted projection receives only granted writable class")
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
