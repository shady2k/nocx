package monoclock_test

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/monoclock"
)

// TestMonotonicNowIsSharedAcrossProcesses is the acceptance criterion named
// in the plan (nocx-6q1uh.5, Task 5): a `commitBy` deadline is computed by
// the coordinator process and checked by the helper process, two different
// processes on the same machine, so the property this package must have is
// not merely "increases within one process" (time.Now() has that too) but
// "one machine-wide clock, readable from anywhere on it and ordered the way
// wall time is not". This spawns a real child process — this same test
// binary, re-executed — and brackets its one reading between two readings
// taken in the parent immediately before and after the child ran.
func TestMonotonicNowIsSharedAcrossProcesses(t *testing.T) {
	if os.Getenv("MONOCLOCK_HELPER_PROCESS") == "1" {
		runHelperProcess()
		return
	}

	before := monoclock.Now()

	cmd := exec.Command(os.Args[0], "-test.run=TestMonotonicNowIsSharedAcrossProcesses") //nolint:gosec // os.Args[0] is this test binary, re-executed; the stdlib's own exec_test.go uses the identical pattern
	cmd.Env = append(os.Environ(), "MONOCLOCK_HELPER_PROCESS=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child process: %v (output: %s)", err, out)
	}

	after := monoclock.Now()

	child, parseErr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if parseErr != nil {
		t.Fatalf("parse child output %q: %v", out, parseErr)
	}
	childNanos := monoclock.Nanos(child)

	if childNanos < before {
		t.Errorf("child reading %d precedes the parent's own reading %d taken before the child ran", childNanos, before)
	}
	if childNanos > after {
		t.Errorf("child reading %d follows the parent's own reading %d taken after the child exited", childNanos, after)
	}
}

// runHelperProcess is the child half of
// TestMonotonicNowIsSharedAcrossProcesses: it prints exactly one
// monoclock.Now() reading to stdout and exits. It is not itself a test —
// re-executing under -test.run means the *testing* package still runs the
// named test, and this function intercepts before any assertion so the
// child's exit code and stdout are exactly the one line the parent parses.
func runHelperProcess() {
	// The write's own error has nowhere useful to go: this process is about
	// to os.Exit unconditionally, and the parent's failure mode for a
	// truncated or missing line is already covered — Output's own error, or
	// a ParseInt failure on whatever did arrive.
	_, _ = os.Stdout.WriteString(strconv.FormatInt(int64(monoclock.Now()), 10) + "\n")
	os.Exit(0)
}
