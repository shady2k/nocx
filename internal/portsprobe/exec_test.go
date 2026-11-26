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
