package proc_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/proc"
)

// TestMain makes this binary the reaper of its own orphans before any test
// runs. Without it the escalation tests below assert a state that NOTHING in
// this repository produces.
//
// The shape they assert is a group whose leader is killed while a descendant
// is still alive. The descendant dies with the group, becomes a zombie of the
// leader, and is orphaned the moment we reap the leader — and an orphan
// zombie is released by the process it is REPARENTED to, which is not us and
// never was. On a desktop that is launchd or systemd and the release is
// immediate. Inside the pre-commit hook's container it is `go`, because the
// hook runs `exec setpriv … go test ./...` with nothing between setpriv and
// the Go tool: PID 1 in that container is a Go program, and a Go program
// reaps its own children and nothing else. The zombie therefore stayed a
// zombie for the whole run, `kill(pid, 0)` went on answering nil, and the
// wait for ESRCH ran out its ceiling — every time, on an idle machine.
//
// (It looked like a flake because the single-package helper the failure was
// re-run with interposes a `sh -c "go test …"`, and a shell blocked in wait()
// reaps whatever it is handed. Same image, same tests, different PID 1.)
//
// PR_SET_CHILD_SUBREAPER makes THIS process the one those orphans are
// reparented to, so waitGone can collect them itself. The wait then ends
// because of something the test did, which is the only kind of wait that
// cannot be starved.
func TestMain(m *testing.M) {
	if err := becomeOrphanReaper(); err != nil {
		fmt.Fprintf(os.Stderr, "proc: cannot become the reaper of this binary's orphans: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// waitCeiling bounds every wait below. It is not a duration anything is
// asserted against: each wait ends on an observable state that either this
// test or the code under test produces, and the ceiling exists only so a
// broken build fails instead of hanging.
const waitCeiling = 20 * time.Second

// waitFor polls pred until it holds or waitCeiling elapses.
func waitFor(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitCeiling)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("never observed: %s", what)
}

// gone reports whether pid names no process at all — not merely a dead one.
//
// The distinction is half of assertion 34. Signal 0 is delivered to zombies
// too: a child that has exited and not been collected still answers kill(pid,
// 0) with nil, and it is still a process occupying a slot in the table. So
// "gone" is the state where the pid answers ESRCH, which is reached only
// after the process has been REAPED.
func gone(pid int) bool {
	return syscall.Kill(pid, 0) != nil
}

// reapOrphan collects pid if it has been reparented to this process and has
// already exited.
//
// This is the test performing, itself, the collection it used to wait for
// somebody else to perform. Both non-answers are ordinary and both are
// ignored: 0 means "ours, still running" and ECHILD means "not ours" — it is
// still its own parent's child, or this platform has no subreaper and PID 1
// has it. The caller re-checks `gone` either way, so nothing here decides
// anything; it only makes the state the caller is waiting for reachable.
func reapOrphan(pid int) {
	var ws syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
}

// waitGone waits until pid names no process at all, reaping it first if it
// has become ours. The failure message is built when the wait fails, not
// before it starts, so it reports the state the pid is actually stuck in.
func waitGone(t *testing.T, role string, pid int) {
	t.Helper()
	deadline := time.Now().Add(waitCeiling)
	for time.Now().Before(deadline) {
		reapOrphan(pid)
		if gone(pid) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s (pid %d) never reached ESRCH: it is in state %q with ppid %q — a member of the killed group is still a process",
		role, pid, procField(pid, 0), procField(pid, 1))
}

// procField reads one field of a pid's /proc/<pid>/stat, counting from the
// single-letter run state: field 0 is that state ("R", "S", "Z"), field 1 the
// parent pid. It is for failure messages only, and answers "" when the pid is
// gone or there is no procfs.
func procField(pid, n int) string {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	i := strings.LastIndex(string(b), ")")
	if i < 0 || i+2 >= len(b) {
		return ""
	}
	f := strings.Fields(string(b)[i+2:])
	if n >= len(f) {
		return ""
	}
	return f[n]
}

// fixture is the process shape both escalation tests need: a direct child
// that ignores INT and TERM, and a GRANDCHILD of it that ignores them too.
//
// Both are deliberate. A cooperative child proves nothing about the case the
// escalation exists for — it passes against an implementation that never
// sends KILL — and a child with no descendant passes against one that kills
// the direct child alone and orphans the pipeline.
type fixture struct {
	// script is the shell program to run as the direct child.
	script string

	pidsFile  string // the direct child writes its own pid, then the grandchild's
	readyFile string // the grandchild writes here once its trap is installed
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{
		pidsFile:  filepath.Join(dir, "pids"),
		readyFile: filepath.Join(dir, "ready"),
	}

	// Both shells park by opening a FIFO nobody ever writes to, rather than
	// looping on `sleep`. The loop's `sleep` is a further process per shell
	// that the fixture creates and the test never learns the pid of; with
	// this binary now the reaper of its own orphans, those would become its
	// own uncollected zombies — a test asserting "nothing is left a zombie"
	// leaving two behind itself. Opening a FIFO for reading blocks in open(2)
	// until a writer appears and none ever does, so the fixture is exactly
	// the two processes it reports, and both are blocked in a state only KILL
	// ends, which is the case under test.
	blocker := filepath.Join(dir, "blocker")
	if err := syscall.Mkfifo(blocker, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	f.script = `
trap '' INT TERM
( trap '' INT TERM; echo ready > "` + f.readyFile + `"; read line < "` + blocker + `" ) &
echo "$$" >> "` + f.pidsFile + `"
echo "$!" >> "` + f.pidsFile + `"
read line < "` + blocker + `"
`
	return f
}

// awaitStarted returns the direct child's pid and its grandchild's, once both
// have been reported AND the grandchild has installed its trap.
//
// Waiting on the grandchild's own marker is what keeps the escalation the
// thing under test. `$!` is known to the parent shell the instant it forks,
// so a wait that took the pids as the start signal could send the whole
// escalation at a grandchild that had not reached `trap` yet — which INT
// would then kill, and the test would pass against an implementation that
// never sends KILL at all.
func (f fixture) awaitStarted(t *testing.T) (child, grandchild int) {
	t.Helper()
	var pids []int
	waitFor(t, "the fixture to report both pids and the grandchild to install its trap", func() bool {
		if _, err := os.Stat(f.readyFile); err != nil {
			return false
		}
		b, err := os.ReadFile(f.pidsFile) //nolint:gosec // a path this test built under t.TempDir()
		if err != nil {
			return false
		}
		pids = pids[:0]
		for _, field := range strings.Fields(string(b)) {
			if p, convErr := strconv.Atoi(field); convErr == nil {
				pids = append(pids, p)
			}
		}
		return len(pids) >= 2
	})
	return pids[0], pids[1]
}

// The whole point of the group form: a child that ignores TERM, and a
// GRANDCHILD of that child which also ignores TERM, are both gone after the
// escalation — and neither is left a zombie.
func TestKillGroup_KillsATermIgnoringDescendantAndReapsTheChild(t *testing.T) {
	f := newFixture(t)

	// sh -c: the shell is the direct child and the group leader; it forks a
	// background grandchild.
	cmd := exec.Command("sh", "-c", f.script) //nolint:gosec // the script is this test's own literal
	proc.InOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	child, grandchild := f.awaitStarted(t)
	if child != cmd.Process.Pid {
		t.Fatalf("the fixture reports direct child %d; we started %d", child, cmd.Process.Pid)
	}

	proc.KillGroup(cmd, done, time.Millisecond)

	// The direct child is reaped by the Wait above; without it the process
	// would linger as a zombie of THIS process for the life of the test
	// binary, which is the second half of assertion 34.
	waitFor(t, "the direct child to be reaped", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})

	// Gone, not merely dead: a zombie is still a process. The direct child is
	// reaped by the Wait above; the grandchild's parent was KILLed and never
	// got to reap it, so it is reparented — to this process, which TestMain
	// arranged for exactly so that waitGone can collect it rather than wait
	// on a stranger to.
	waitGone(t, "the direct child", child)
	waitGone(t, "the TERM-ignoring grandchild", grandchild)
}

// A child that finishes on its own before the escalation starts is never
// signalled: KillGroup returns on `done` rather than sending anything into a
// group id the kernel may since have handed to somebody else.
func TestKillGroup_ReturnsWithoutSignallingAFinishedChild(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	proc.InOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	done := make(chan struct{})
	close(done)
	proc.KillGroup(cmd, done, time.Second) // must not block on the grace
}

// A process group is what makes the escalation reach a descendant at all.
func TestInOwnGroup_MakesTheChildItsOwnGroupLeader(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exec sleep 30")
	proc.InOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Fatalf("child pgid = %d, want its own pid %d", pgid, cmd.Process.Pid)
	}
	if pgid == syscall.Getpgrp() {
		t.Fatalf("child shares our group %d; a group kill would reach us", pgid)
	}
}
