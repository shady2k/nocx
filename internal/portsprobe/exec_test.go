package portsprobe

// ExecRunner is the far helper's execution seam: the host's own tools, run as
// argv with no shell in between. These tests drive the real os/exec path — a
// script on the test's PATH is the host's tool — so the absent/success pair is
// asserted at the seam rather than through a fake.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRung installs an executable script named name into dir and returns dir.
func writeRung(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // test-only script
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestExecRunnerReadsTheTableTheHostsOwnToolPrints is the paired success half:
// a host whose PATH holds ss answers `available` with the parsed listeners.
func TestExecRunnerReadsTheTableTheHostsOwnToolPrints(t *testing.T) {
	dir := t.TempDir()
	writeRung(t, dir, "ss", "printf '%s\\n' '"+ssTable+"'\n")
	t.Setenv("PATH", dir)

	s := NewDetector(NewExecRunner()).Sample(context.Background())
	if s.State != StateAvailable {
		t.Fatalf("state = %q (%s), want available", s.State, s.Classification)
	}
	if s.Probe != "ss" {
		t.Fatalf("probe = %q, want ss", s.Probe)
	}
	if len(s.Listeners) != 1 || s.Listeners[0].Port != 22 {
		t.Fatalf("listeners = %+v, want the script's one row on port 22", s.Listeners)
	}
	if s.Listeners[0].Process.Evidence != EvidenceKnown || s.Listeners[0].Process.PID != 1 {
		t.Fatalf("process = %+v, want the known owner the script printed", s.Listeners[0].Process)
	}
}

// TestExecRunnerOnAnEmptyPATHReportsEveryRungAbsent is the paired failure half:
// the same runner, the same host, no tool — and the answer is `unavailable`
// rather than an empty table.
func TestExecRunnerOnAnEmptyPATHReportsEveryRungAbsent(t *testing.T) {
	t.Setenv("PATH", "")

	s := NewDetector(NewExecRunner()).Sample(context.Background())
	if s.State != StateUnavailable {
		t.Fatalf("state = %q (%s), want unavailable", s.State, s.Classification)
	}
	if !equalStrings(s.ProbesTried, []string{"ss", "netstat", "lsof", "sockstat"}) {
		t.Fatalf("probesTried = %v, want every rung tried and none found", s.ProbesTried)
	}
}

// TestExecRunnerReportsACappedStreamAsTruncated: the 64 KiB bound is the
// runner's, and it must say the table was cut rather than hand back a prefix as
// though it were whole.
func TestExecRunnerReportsACappedStreamAsTruncated(t *testing.T) {
	dir := t.TempDir()
	writeRung(t, dir, "ss", "i=0; while [ $i -lt 2000 ]; do echo 'LISTEN 0 1 127.0.0.1:1 0.0.0.0:* padding-padding-padding-padding'; i=$((i+1)); done\n")
	t.Setenv("PATH", dir)

	res, err := NewExecRunner().Run(context.Background(), Command{Argv: []string{"ss", "-H", "-lntp"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated {
		t.Fatal("a stream past the cap was not reported truncated")
	}
	if len(res.Stdout) != OutputCap {
		t.Fatalf("captured %d bytes, want the %d-byte cap", len(res.Stdout), OutputCap)
	}

	s := NewDetector(NewExecRunner()).Sample(context.Background())
	if s.State != StateFailedTransiently || s.Classification != "probe output incomplete" {
		t.Fatalf("a truncated table answered %q / %q, want failed-transiently (incomplete)", s.State, s.Classification)
	}
}

// TestExecRunnerAbandonsADescendantThatHoldsTheCapture is rule 3's external
// call that does not come back: the rung's program exits at once, but the child
// it forked inherits stdout and outlives the pass. A runner that signals only
// the direct child waits for that descendant's EOF — the bound is then the
// descendant's lifetime, not the pass's — so the assertion is the bound.
func TestExecRunnerAbandonsADescendantThatHoldsTheCapture(t *testing.T) {
	dir := t.TempDir()
	// The script is a wrapper: the shell forks the tool it names and waits for
	// it, so the process this runner signals is not the one holding stdout. The
	// tool runs for six seconds, twelve times the pass's bound.
	writeRung(t, dir, "ss", "sleep 6\n")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	bound := 500 * time.Millisecond
	start := time.Now()
	s := NewDetector(NewExecRunner(), WithSampleTimeout(bound)).Sample(context.Background())
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("the sample took %v under a %v bound: the pass waited for a descendant's capture", elapsed, bound)
	}
	if s.State != StateFailedTransiently || s.Classification != "probe timed out" {
		t.Fatalf("state/classification = %q/%q, want failed-transiently/probe timed out", s.State, s.Classification)
	}
}

// TestExecRunnerStopsARungThatFloodsItsStderr: the cap is per stream, and
// crossing it ends the child on the stream that crossed it. A tool that only
// ever writes to stderr would otherwise run until the pass's own deadline.
func TestExecRunnerStopsARungThatFloodsItsStderr(t *testing.T) {
	dir := t.TempDir()
	// Past the 64 KiB cap on stderr, then it keeps running: nothing but the cap
	// can end it before the context below does.
	writeRung(t, dir, "ss", "i=0; while [ $i -lt 3000 ]; do echo 'noise padding-padding-padding-padding' 1>&2; i=$((i+1)); done\nwhile :; do sleep 1; done\n")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	res, err := NewExecRunner().Run(ctx, Command{Argv: []string{"ss", "-H", "-lntp"}})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated {
		t.Fatal("stderr past the cap was not reported truncated")
	}
	if len(res.Stderr) != OutputCap {
		t.Fatalf("captured %d stderr bytes, want the %d-byte cap", len(res.Stderr), OutputCap)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the rung ran %v: a stderr overflow did not end it", elapsed)
	}
}

// TestExecRunnerReportsAMissingProgramAsAbsent pins the one error the ladder
// switches on: a lookup miss is a fact about the host's tools, not a transient
// exec failure to back off on.
func TestExecRunnerReportsAMissingProgramAsAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := NewExecRunner().Run(context.Background(), Command{Argv: []string{"ss", "-H", "-lntp"}})
	if !errors.Is(err, ErrCommandAbsent) {
		t.Fatalf("run of a program no PATH directory holds = %v, want ErrCommandAbsent", err)
	}
}

// TestExecRunnerKeepsTheRungEnvironment: ss's table is locale-dependent, so the
// rung that declares LC_ALL=C must actually reach the child with it. The script
// answers the table only when the variable arrived, so a runner that dropped
// the environment answers `unavailable` instead.
func TestExecRunnerKeepsTheRungEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeRung(t, dir, "ss", "[ \"$LC_ALL\" = C ] || exit 3\nprintf '%s\\n' '"+ssTable+"'\n")
	t.Setenv("PATH", dir)

	s := NewDetector(NewExecRunner()).Sample(context.Background())
	if s.State != StateAvailable || s.Probe != "ss" {
		t.Fatalf("sample answered %q by %q (%s), want the ss rung's table", s.State, s.Probe, s.Classification)
	}
	if len(s.Listeners) != 1 || s.Listeners[0].Port != 22 {
		t.Fatalf("listeners = %+v, want the script's one row on port 22", s.Listeners)
	}
}
