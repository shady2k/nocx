package proc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/proc"
)

// manualClock is the injected time source. Nothing here waits on a
// duration: the deadline fires when the test says so, after it has OBSERVED
// the fixture's processes exist.
type manualClock struct {
	mu     sync.Mutex
	timers []*manualTimer
}

type manualTimer struct {
	c       *manualClock
	fn      func()
	stopped bool
}

func (t *manualTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (c *manualClock) AfterFunc(_ time.Duration, fn func()) proc.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{c: c, fn: fn}
	c.timers = append(c.timers, t)
	return t
}

// fire runs every scheduled call that has not been stopped.
func (c *manualClock) fire() {
	c.mu.Lock()
	due := make([]*manualTimer, 0, len(c.timers))
	for _, t := range c.timers {
		if !t.stopped {
			t.stopped = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.fn()
	}
}

func (c *manualClock) scheduled() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// Assertion 34. The deadline terminates the WHOLE process group — including
// a member that ignores TERM and requires KILL — and every member is reaped:
// afterwards no enumeration process of that group survives and none is left
// a zombie.
//
// The fixture ignores TERM on purpose, in both the direct child and a
// grandchild. A cooperative child would pass against an implementation that
// never sends KILL, and against one that kills only the direct child and
// orphans the pipeline — the two defects this exists to close.
func TestSupervisor_DeadlineKillsATermIgnoringGroupAndReapsIt(t *testing.T) {
	f := newFixture(t)
	clock := &manualClock{}
	sup := proc.Supervisor{Clock: clock, Grace: time.Millisecond}

	type outcome struct {
		out proc.Output
		err error
	}
	res := make(chan outcome, 1)
	go func() {
		out, err := sup.Run(context.Background(), proc.Job{
			Argv:     []string{"sh", "-c", f.script},
			Deadline: 5 * time.Second,
			MaxBytes: 1 << 20,
		})
		res <- outcome{out, err}
	}()

	child, grandchild := f.awaitStarted(t)
	waitFor(t, "the deadline to be armed on the injected clock", func() bool { return clock.scheduled() > 0 })

	clock.fire()

	got := <-res
	if !errors.Is(got.err, proc.ErrDeadline) {
		t.Fatalf("err = %v, want ErrDeadline", got.err)
	}
	if got.out.Complete {
		t.Fatalf("a run the deadline cut reported Complete; nothing may be published from it")
	}

	// GONE, not merely dead — which is the second half of assertion 34.
	// `gone` is ESRCH, the state a pid reaches only after it has been
	// reaped: by the Supervisor's own Wait for the direct child, and by
	// waitGone for the grandchild, whose parent we KILLed before it could
	// reap anything and which TestMain therefore made this process's own
	// orphan to collect. A zombie answers kill(pid, 0) and is still a
	// process, so a check that only asked "did it die" would pass over
	// exactly the leak this asserts against.
	waitGone(t, "the direct child", child)
	waitGone(t, "the TERM-ignoring grandchild", grandchild)
}

// A run that finishes inside its deadline publishes its output and is
// Complete — the paired "and on a normal machine it succeeds" for the
// failure case above.
func TestSupervisor_CompletesInsideTheDeadline(t *testing.T) {
	sup := proc.Supervisor{Clock: proc.RealClock{}, Grace: time.Millisecond}
	out, err := sup.Run(context.Background(), proc.Job{
		Argv:     []string{"sh", "-c", "printf 'alpha\nbeta\n'"},
		Deadline: 30 * time.Second,
		MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !out.Complete {
		t.Fatalf("Complete = false for a run that finished on its own")
	}
	if string(out.Stdout) != "alpha\nbeta\n" {
		t.Fatalf("stdout = %q", out.Stdout)
	}
}

// A non-zero exit is a failure of the job, not of the supervisor: the
// output is not Complete and the error names the status.
func TestSupervisor_NonZeroExitIsNotComplete(t *testing.T) {
	sup := proc.Supervisor{Clock: proc.RealClock{}, Grace: time.Millisecond}
	out, err := sup.Run(context.Background(), proc.Job{
		Argv:     []string{"sh", "-c", "printf partial; exit 3"},
		Deadline: 30 * time.Second,
		MaxBytes: 1 << 20,
	})
	if err == nil {
		t.Fatalf("a job exiting 3 returned no error")
	}
	if out.Complete {
		t.Fatalf("a job exiting 3 reported Complete")
	}
}

// Output is bounded. Past the bound the run is cut and reports incomplete
// rather than returning a truncated answer that reads like a whole one.
func TestSupervisor_OutputBoundCutsTheRun(t *testing.T) {
	sup := proc.Supervisor{Clock: proc.RealClock{}, Grace: time.Millisecond}
	out, err := sup.Run(context.Background(), proc.Job{
		Argv:     []string{"sh", "-c", "while :; do printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'; done"},
		Deadline: 60 * time.Second,
		MaxBytes: 4096,
	})
	if !errors.Is(err, proc.ErrOutputBound) {
		t.Fatalf("err = %v, want ErrOutputBound", err)
	}
	if out.Complete {
		t.Fatalf("a run cut at the output bound reported Complete")
	}
	if len(out.Stdout) > 4096 {
		t.Fatalf("kept %d bytes past the 4096 bound", len(out.Stdout))
	}
}

// The caller's context is the other way a run stops, and it stops the group
// the same way.
func TestSupervisor_ContextCancelStopsTheGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	pidsFile := filepath.Join(dir, "pids")
	// Parked on a FIFO nobody writes to rather than looping on `sleep`, for
	// the reason newFixture gives: the loop forks a process per iteration
	// that this test never learns the pid of, and this binary is now the
	// reaper those orphans land on.
	blocker := filepath.Join(dir, "blocker")
	if err := syscall.Mkfifo(blocker, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	script := `echo "$$" >> "` + pidsFile + `"; read line < "` + blocker + `"`

	sup := proc.Supervisor{Clock: proc.RealClock{}, Grace: time.Millisecond}
	done := make(chan error, 1)
	go func() {
		_, err := sup.Run(ctx, proc.Job{Argv: []string{"sh", "-c", script}, Deadline: time.Hour, MaxBytes: 1 << 20})
		done <- err
	}()
	var pid int
	waitFor(t, "the fixture to start", func() bool {
		b, err := os.ReadFile(pidsFile) //nolint:gosec // a path this test built under t.TempDir()
		if err != nil {
			return false
		}
		f := strings.Fields(string(b))
		if len(f) == 0 {
			return false
		}
		pid, _ = strconv.Atoi(f[0])
		return pid > 0
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	waitGone(t, "the cancelled child", pid)
}
