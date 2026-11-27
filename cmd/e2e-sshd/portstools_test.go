package main

// The -ports-tools switch, asserted where it decides something: the binaries it
// writes, the environment it shapes, and the record it leaves (portstools.go).
//
// The two modes are the two halves of the ports contract this leaf closes
// (nocx-oalo1.13.4, design §5.10 scenario 8): a host whose tool reports one
// listener, and a host with no tool at all. What the ladder does with each is
// internal/portsprobe's own subject; what must hold HERE is that the fixture
// really produces those two hosts — a fake that `ss` would not recognise, or an
// "absent" tool that exits 1, would make an e2e run assert about a host nobody
// described.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/portsprobe"
)

// callsFile is the file every fixture tool appends to: the switch's record,
// which the e2e specs read too (<dir>/calls, beside PORTSTOOLS=). It is a
// helper HERE rather than in portstools.go because nothing in production reads
// it — the file is the far side's account, addressed by its readers.
func callsFile(dir string) string {
	return filepath.Join(dir, "calls")
}

// callsText reads the record; empty when nothing ran, and when the file does
// not exist, which is the same statement.
func callsText(dir string) string {
	body, err := os.ReadFile(callsFile(dir)) //nolint:gosec // the fixture's own record.
	if err != nil {
		return ""
	}
	return string(body)
}

// withPortsToolsDir installs one mode for the duration of a test and puts the
// package's switch back afterwards: portsToolsDir is process state that
// sessionEnv reads, and a test that left it set would shape every later one.
func withPortsToolsDir(t *testing.T, mode string) string {
	t.Helper()
	dir, err := installPortsTools(mode)
	if err != nil {
		t.Fatalf("installPortsTools(%q): %v", mode, err)
	}
	previous := portsToolsDir
	portsToolsDir = dir
	t.Cleanup(func() {
		portsToolsDir = previous
		_ = os.RemoveAll(dir)
	})
	return dir
}

// runTool runs one fixture binary with the fixture directory FIRST on PATH, the
// way the session (and therefore the far helper) resolves it.
func runTool(t *testing.T, dir, name string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	//nolint:gosec // the fixture's own directory, and a name the test chose.
	cmd := exec.Command(filepath.Join(dir, name), args...)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return out.String(), errOut.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("run %s: %v", name, err)
	}
	return out.String(), errOut.String(), 0
}

// The serving half: one fixed listener, in the dialect `ss -H -lntp` prints, so
// a spec can name the row a person sees and the parser agrees with it.
func TestPortsTools_FakeSSReportsOneFixedListenerTheLadderCanParse(t *testing.T) {
	dir := withPortsToolsDir(t, "fake")

	stdout, stderr, code := runTool(t, dir, "ss", "-H", "-lntp")
	if code != 0 {
		t.Fatalf("fake ss exited %d, want 0 (stderr %q): a rung that fails is a host with no tool", code, stderr)
	}

	listeners, ok := portsprobe.ParseSS([]byte(stdout))
	if !ok {
		t.Fatalf("the ladder's own parser refused the fake's output %q", stdout)
	}
	if len(listeners) != 1 {
		t.Fatalf("parsed %d listener(s), want exactly 1: %+v", len(listeners), listeners)
	}
	got := listeners[0]
	if got.Port != 6768 || got.Address != "0.0.0.0" {
		t.Errorf("listener = %s:%d, want 0.0.0.0:6768", got.Address, got.Port)
	}
	// `-p`'s own shape: a KNOWN owner, which is the panel's "name (pid N)" row.
	if got.Process.Evidence != portsprobe.EvidenceKnown || got.Process.Name != "node" || got.Process.PID != 4242 {
		t.Errorf("process = %+v, want known node pid 4242", got.Process)
	}

	// The record: the invocation is the fixture's own account, and it is what a
	// spec reads when it asserts that no ladder ran.
	if strings.TrimSpace(callsText(dir)) == "" {
		t.Fatalf("the fake ss left no record in %s", callsFile(dir))
	}
	if calls := callsText(dir); !strings.Contains(calls, "ss -H -lntp") {
		t.Errorf("calls = %q, want the rung's own argv", calls)
	}
}

// The absent half: every tool the ladder can reach exists and refuses the way a
// tool that is not installed does — 127, with "not found" on stderr. That pair
// is the classification's own rule, so this is the fixture asserting the
// contract rather than a spelling of it.
func TestPortsTools_AbsentExitsAsAMissingToolForEveryRung(t *testing.T) {
	dir := withPortsToolsDir(t, "absent")

	for _, name := range []string{"ss", "netstat", "lsof", "sockstat"} {
		stdout, stderr, code := runTool(t, dir, name, "-l")
		if code != 127 {
			t.Errorf("%s exited %d, want 127 — the status a missing tool produces", name, code)
		}
		if !strings.Contains(stderr, "not found") {
			t.Errorf("%s stderr = %q, want it to say \"not found\": the ladder reads both signals", name, stderr)
		}
		if stdout != "" {
			t.Errorf("%s printed %q on stdout; a tool that is not there prints nothing", name, stdout)
		}
	}

	calls := callsText(dir)
	for _, name := range []string{"ss", "netstat", "lsof", "sockstat"} {
		if !strings.Contains(calls, name+" ") {
			t.Errorf("calls = %q, want an entry for %s", calls, name)
		}
	}
}

// The environment is where the switch actually acts: the fixture directory
// FIRST, and the rest of the session's PATH intact behind it.
func TestPortsTools_SessionPathFindsTheFixtureFirstAndKeepsTheRest(t *testing.T) {
	dir := withPortsToolsDir(t, "fake")

	var path string
	var seen int
	for _, kv := range sessionEnv("bash") {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("PATH appears %d times, want exactly 1", seen)
	}
	if !strings.HasPrefix(path, dir+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q, want it to start with %q", path, dir)
	}
	// And the shell still resolves its own tools: the real PATH is preserved
	// behind the fixture, not replaced by it.
	if want := os.Getenv("PATH"); !strings.HasSuffix(path, want) {
		t.Errorf("PATH = %q, want the session's own %q behind the fixture", path, want)
	}
}

// Switching it off changes nothing, which is what keeps every other spec's
// environment the one sshd would hand on.
func TestPortsTools_OffLeavesTheSessionEnvironmentAlone(t *testing.T) {
	previous := portsToolsDir
	portsToolsDir = ""
	t.Cleanup(func() { portsToolsDir = previous })

	// The inherited PATH is still carried — exactly once, and unchanged. The
	// switch prepends; it never rewrites what a caller's environment already
	// said.
	want := os.Getenv("PATH")
	var path string
	var seen int
	for _, kv := range sessionEnv("bash") {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("PATH appears %d times, want exactly 1", seen)
	}
	if path != want {
		t.Errorf("PATH = %q, want the session's own %q", path, want)
	}
	if portsToolsPathPrefix() != "" {
		t.Error("the off switch still prefixes a directory onto PATH")
	}
}

// A typo is a startup failure, not a run that quietly changes nothing.
func TestPortsTools_AnUnknownModeIsRefusedByName(t *testing.T) {
	for _, mode := range []string{"fak", "absent ", "Fake"} {
		if _, err := installPortsTools(mode); err == nil {
			t.Errorf("installPortsTools(%q) accepted a mode that is not in the set", mode)
		} else if !strings.Contains(err.Error(), "-ports-tools") {
			t.Errorf("error %q does not name the switch", err)
		}
	}
}
