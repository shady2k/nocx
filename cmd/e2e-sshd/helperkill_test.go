package main

// The far host's kill switch: the harness for the lifecycle scenario where the
// helper daemon dies while the SSH transport lives (design §5.10, scenario 4;
// nocx-oalo1.6.3).
//
// # What these tests are, and are not
//
// They are the harness's own tests. The scenarios that USE the switch —
// a panel's helper axis turning `helper-lost`, Refresh redialling, the mode
// set to raw beside a serving helper — belong to e2e/ssh-panels.spec.ts and are
// not written here. What is asserted here is what those scenarios will stand
// on: the gate kills the daemon this account installed, only that one, once per
// cue, and the transport that carried it keeps serving.
//
// # A fake daemon that is faithful about its own process line
//
// The match is on the PROCESS TABLE, so the fake has to look like the real
// thing there and nowhere else: this test binary, started through a symlink at
// `<home>/.nocx/helper/<generation>/nocx-helper` with the argument `serve`.
// argv[0] is the symlink's path (exec keeps the name it was given), which is
// exactly what `ps` shows for the installed daemon, and the binary holds in
// fake mode (fakeHelperDaemonEnv) instead of running this suite again.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// fakeHelperDaemonEnv puts this binary into fake-daemon mode. Set only on the
// children a test starts, never in the test process itself.
const fakeHelperDaemonEnv = "NOCX_E2E_SSHD_FAKE_HELPER_DAEMON"

// fakeExitWait bounds a wait for a SIGKILLed process to be reaped. It is a
// guard against a hung fixture, not a pace: the wait itself is on the process's
// exit, which is the event that matters.
const fakeExitWait = 10 * time.Second

func TestMain(m *testing.M) {
	if os.Getenv(fakeHelperDaemonEnv) != "" {
		// A far helper daemon stands here, and only a signal ends it.
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

// TestHelperDaemonPIDs_FindsOnlyTheInstalledDaemonForItsHome pins the rule the
// switch kills by: the command is `<home>/.nocx/helper/<generation>/nocx-helper
// serve`, and a bridge, another account's daemon, or the same binary invoked
// with arguments is not a daemon.
func TestHelperDaemonPIDs_FindsOnlyTheInstalledDaemonForItsHome(t *testing.T) {
	const home = "/home/e2e"
	table := strings.Join([]string{
		"      1 /sbin/init",
		"    123 " + home + "/.nocx/helper/0123abc/nocx-helper serve",
		"    124 " + home + "/.nocx/helper/0123abc/nocx-helper bridge 0123abc",
		"    125 /home/somebody-else/.nocx/helper/0123abc/nocx-helper serve",
		"    126 " + home + "/.nocx/helper/0123abc/nocx-helper serve --extra",
		"    127 " + home + "/.nocx/helper/0123abc/other-binary serve",
		"    128 " + home + "/.nocx/helper/0123abc/nocx-helper",
		"",
	}, "\n")

	got := helperDaemonPIDs(home, table)
	if want := []int{123}; !slices.Equal(got, want) {
		t.Fatalf("helperDaemonPIDs = %v, want %v", got, want)
	}
}

// TestHelperKillGate_KillsOncePerCueAndAnnouncesIt pins the cue protocol the
// spec waits on: a distinct gate content is one kill, the same content again is
// nothing, and every cue ends with an announced count even when the process
// table could not be read.
func TestHelperKillGate_KillsOncePerCueAndAnnouncesIt(t *testing.T) {
	home := t.TempDir()
	gatePath := filepath.Join(home, "cue")
	table := []byte("    7 " + home + "/.nocx/helper/g/nocx-helper serve\n")

	var printed []string
	var killed []int
	gate := &helperKillGate{
		path:  gatePath,
		home:  home,
		print: func(line string) { printed = append(printed, line) },
		list:  func() ([]byte, error) { return table, nil },
		kill:  func(pid int) error { killed = append(killed, pid); return nil },
	}

	// No gate file: nothing happens, and nothing is announced.
	gate.seed()
	gate.observe()
	if len(printed) != 0 || len(killed) != 0 {
		t.Fatalf("a gate that was never written did something: printed=%v killed=%v", printed, killed)
	}

	writeGate(t, gatePath, "cue-1")
	gate.observe()
	if want := []int{7}; !slices.Equal(killed, want) {
		t.Fatalf("killed = %v, want %v", killed, want)
	}
	if want := []string{"HELPERKILLED=7", "HELPERKILLDONE=1"}; !slices.Equal(printed, want) {
		t.Fatalf("printed = %v, want %v", printed, want)
	}

	// The same content is the same cue: the rule, not an accident of a poll
	// having run twice.
	gate.observe()
	if len(killed) != 1 {
		t.Fatalf("the same gate content killed again: %v", killed)
	}

	// A new content is a new cue, and it re-arms even if a spec writes it
	// while the previous cue is still the file's content.
	writeGate(t, gatePath, "cue-2")
	gate.observe()
	if want := []int{7, 7}; !slices.Equal(killed, want) {
		t.Fatalf("killed after the second cue = %v, want %v", killed, want)
	}

	// A far side that cannot read its process table says so rather than
	// reporting a kill it did not make.
	gate.list = func() ([]byte, error) { return nil, os.ErrPermission }
	writeGate(t, gatePath, "cue-3")
	gate.observe()
	if got := printed[len(printed)-2:]; !strings.HasPrefix(got[0], "HELPERKILLFAILED=") ||
		got[1] != "HELPERKILLDONE=0" {
		t.Fatalf("a failed process table printed %q, want a failure and a zero count", got)
	}

	// Removing the gate forgets the last content: writing it again is a cue.
	if err := os.Remove(gatePath); err != nil {
		t.Fatalf("remove gate: %v", err)
	}
	gate.list = func() ([]byte, error) { return table, nil }
	gate.observe()
	writeGate(t, gatePath, "cue-3")
	gate.observe()
	if len(killed) != 3 {
		t.Fatalf("a gate re-created with the same content did not kill: %v", killed)
	}
}

// TestHelperKillGate_KillsTheDaemonAndNotItsBridgeOrAnotherHomesDaemon is the
// process-table half against real processes: the rules above are only worth
// anything if `ps` really shows the daemon the way the match reads it.
func TestHelperKillGate_KillsTheDaemonAndNotItsBridgeOrAnotherHomesDaemon(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()

	daemon := startFakeDaemon(t, installFakeHelper(t, home), "serve")
	bridge := startFakeDaemon(t, installFakeHelper(t, home), "bridge", "generation")
	foreign := startFakeDaemon(t, installFakeHelper(t, other), "serve")

	var printed []string
	var printedMu sync.Mutex
	gate := &helperKillGate{
		path:  filepath.Join(t.TempDir(), "cue"),
		home:  home,
		print: func(line string) { printedMu.Lock(); printed = append(printed, line); printedMu.Unlock() },
		list:  processTable,
		kill:  killProcess,
	}
	gate.seed()
	writeGate(t, gate.path, "cue-1")
	gate.observe()

	daemon.waitExit(t)
	bridge.stillRunning(t)
	foreign.stillRunning(t)

	printedMu.Lock()
	defer printedMu.Unlock()
	if want := "HELPERKILLED=" + strconv.Itoa(daemon.pid); !slices.Contains(printed, want) {
		t.Fatalf("the far side announced %q, want %q", printed, want)
	}
	// One, and only one: a bridge is not a daemon and another home's daemon is
	// not this home's, so a rule that lost either comparison would say 2 or 3.
	if !slices.Contains(printed, "HELPERKILLDONE=1") {
		t.Fatalf("the far side announced %q, want HELPERKILLDONE=1", printed)
	}
}

// TestHelperKillGate_LeavesALiveSshTransportServing is the premise every
// scenario built on this switch stands on: killing the helper daemon is a
// process on the far side, and the SSH connection that carried it is still
// there afterwards, on the same client, running commands.
func TestHelperKillGate_LeavesALiveSshTransportServing(t *testing.T) {
	addr, hostKey, signer := startFixture(t)
	client := dial(t, addr, hostKey, signer)

	if got := runExec(t, client, "echo before"); got != "before\n" {
		t.Fatalf("the fixture did not run a command before the kill: %q", got)
	}

	home := t.TempDir()
	daemon := startFakeDaemon(t, installFakeHelper(t, home), "serve")
	var printed []string
	var mu sync.Mutex
	gate := &helperKillGate{
		path:  filepath.Join(t.TempDir(), "cue"),
		home:  home,
		print: func(line string) { mu.Lock(); printed = append(printed, line); mu.Unlock() },
		list:  processTable,
		kill:  killProcess,
	}
	gate.seed()
	writeGate(t, gate.path, "cue-1")
	gate.observe()
	daemon.waitExit(t)

	if got := runExec(t, client, "echo after"); got != "after\n" {
		t.Fatalf("the transport stopped serving after the helper daemon died: %q", got)
	}
}

// TestAccountHomes_OffersTheResolvedSpellingBesideTheGivenOne pins the macOS
// hazard the match exists against: a home reached through a symlink is the same
// account, and an install path made through SFTP's realpath carries the
// resolved spelling, so the table may hold either one.
func TestAccountHomes_OffersTheResolvedSpellingBesideTheGivenOne(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-home")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatalf("the real home: %v", err)
	}
	link := filepath.Join(dir, "linked-home")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("the linked home: %v", err)
	}

	got := accountHomes(link)
	if want := []string{link, real}; !slices.Equal(got, want) {
		t.Fatalf("accountHomes(%q) = %v, want %v", link, got, want)
	}
	if got := accountHomes(real); !slices.Equal(got, []string{real}) {
		t.Fatalf("a home needing no resolution answered %v, want itself alone", got)
	}
}

// ── the fake far host ───────────────────────────────────────────────────────

// fakeDaemon is one process started through the installed helper's path. Its
// exit is an observable the test waits on, never a duration.
type fakeDaemon struct {
	cmd  *exec.Cmd
	pid  int
	done chan struct{}
}

// installFakeHelper lays the helper's install path out under home and answers
// the executable's path: a symlink to this test binary, so a process started
// through it shows the installed name in the process table. A symlink and not
// a copy: the test binary is tens of megabytes and the kernel is content with
// the name.
func installFakeHelper(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".nocx", "helper", "test-generation")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("helper install dir: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("this test binary: %v", err)
	}
	path := filepath.Join(dir, "nocx-helper")
	if _, err := os.Lstat(path); err == nil {
		// The same home's daemon and its bridge: one install, two processes.
		return path
	}
	if err := os.Symlink(exe, path); err != nil {
		t.Fatalf("symlink the fake helper: %v", err)
	}
	return path
}

// startFakeDaemon starts the fake helper with the given subcommand. It holds
// until a signal ends it; nothing here waits for a while to pass.
func startFakeDaemon(t *testing.T, path string, args ...string) *fakeDaemon {
	t.Helper()
	//nolint:gosec // dev-only fixture: the path is the install path this test just created
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), fakeHelperDaemonEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the fake helper: %v", err)
	}
	d := &fakeDaemon{cmd: cmd, pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(d.done)
	}()
	t.Cleanup(func() { d.stop() })
	return d
}

// waitExit requires the process to have ended, and says so plainly when it has
// not: a SIGKILL that did not land is the defect, not a slow machine.
func (d *fakeDaemon) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-d.done:
	case <-time.After(fakeExitWait):
		t.Fatalf("pid %d is still running after the kill", d.pid)
	}
}

// stillRunning requires the process to be alive at this moment. It reads the
// kernel rather than the harness's memory of signalling.
func (d *fakeDaemon) stillRunning(t *testing.T) {
	t.Helper()
	select {
	case <-d.done:
		t.Fatalf("pid %d was killed but is not the helper daemon of this home", d.pid)
	default:
	}
	if err := syscall.Kill(d.pid, 0); err != nil {
		t.Fatalf("pid %d is gone: %v", d.pid, err)
	}
}

// stop ends the process if the test left it running, and waits for that, so a
// leaked fake daemon cannot outlive the run.
func (d *fakeDaemon) stop() {
	select {
	case <-d.done:
		return
	default:
	}
	_ = d.cmd.Process.Signal(syscall.SIGKILL)
	select {
	case <-d.done:
	case <-time.After(fakeExitWait):
	}
}

// writeGate writes one cue, creating the gate on its first use.
func writeGate(t *testing.T, path, content string) {
	t.Helper()
	// #nosec G306 — a fixture file in a directory this test created.
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the gate %s: %v", path, err)
	}
}

// runExec runs one command over an open SSH client and answers its stdout: the
// transport's own account of still serving.
func runExec(t *testing.T, client *gossh.Client, command string) string {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer func() { _ = session.Close() }()
	out, err := session.Output(command)
	if err != nil {
		t.Fatalf("run %q: %v", command, err)
	}
	return string(out)
}
