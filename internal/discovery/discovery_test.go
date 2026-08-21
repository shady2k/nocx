package discovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ---------------------------------------------------------------------------
// Scripted fake exec seam — the ladder and detector are protocol-level
// logic; only the lease/cancel semantics need a real connection (covered in
// internal/ssh and local_test.go). Exec records the command it ran, waits on
// the block channel when set, and then consumes the next canned response.
// ---------------------------------------------------------------------------

type fakeConn struct {
	mu        sync.Mutex
	responses []fakeResponse
	execs     []string
	block     chan struct{} // when non-nil, Exec blocks until ctx done or this closes
	closed    bool
	lost      bool
	// autoValid answers every exec with a valid "normal host" sample,
	// without consuming a queued response. Used by the scheduler tests.
	autoValid bool
}

type fakeResponse struct {
	result *ExecResult
	err    error
}

func newFakeConn() *fakeConn {
	return &fakeConn{}
}

func (f *fakeConn) Exec(ctx context.Context, cmd string) (*ExecResult, error) {
	f.mu.Lock()
	f.execs = append(f.execs, cmd)
	block := f.block
	f.mu.Unlock()

	if block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-block:
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.closed:
		return nil, &ExecError{Kind: ExecErrLeaseClosed}
	case f.lost:
		return nil, &ExecError{Kind: ExecErrConnectionLost}
	}
	if f.autoValid {
		return framed(knownRow), nil
	}
	if len(f.responses) == 0 {
		return nil, errors.New("fake: no queued response for " + cmd)
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp.result, resp.err
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeConn) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...)
}

func (f *fakeConn) queue(resps ...fakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, resps...)
}

// framed builds a valid sentinel-framed probe response. A body that does not
// end with a newline would fuse the trailing sentinel into the last row —
// real probe output always newline-terminates its rows, so mirror that.
func framed(body string) *ExecResult {
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return &ExecResult{Stdout: []byte("NOCX-PD/1\n" + body + "NOCX-PD/1\n"), ExitStatus: 0}
}

// knownRow is a single ss row with visible process evidence — a "normal
// host" answer that yields StateAvailable.
const knownRow = "LISTEN 0 511 0.0.0.0:6768 0.0.0.0:* users:((\"app\",pid=1,fd=1))\n"

func absentTool() fakeResponse {
	return fakeResponse{result: &ExecResult{
		Stdout: []byte("NOCX-PD/1\nNOCX-PD/1\n"),
		Stderr: []byte("sh: ss: not found\n"),
		// ExitStatus 127 is the shell's command-not-found status.
		ExitStatus: 127,
	}}
}

func newTestDetector(t *testing.T, f *fakeConn, opts ...DetectorOption) *Detector {
	t.Helper()
	d := NewDetector(f, log.NewSlogAdapter(nil), opts...)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// ---------------------------------------------------------------------------
// The ladder
// ---------------------------------------------------------------------------

// TestDetector_NormalHost_SelectsSSOnce is the happy path: a normal Linux
// host answers ss, selection happens once per connection, and only the
// selected probe runs afterwards.
func TestDetector_NormalHost_SelectsSSOnce(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{result: framed(ssMixedFixture)}, fakeResponse{result: framed(ssMixedFixture)})
	d := newTestDetector(t, f)

	s1 := d.Sample(context.Background())
	if s1.State != StateAvailable {
		t.Fatalf("state = %v, want available", s1.State)
	}
	if len(s1.Listeners) != 9 {
		t.Fatalf("listeners = %d, want 9", len(s1.Listeners))
	}
	if s1.Probe != "ss" {
		t.Errorf("probe = %q, want ss", s1.Probe)
	}
	// The real measured shape: mixed process evidence.
	known, denied := 0, 0
	for _, l := range s1.Listeners {
		switch l.Process.Evidence {
		case EvidenceKnown:
			known++
		case EvidencePermissionDenied:
			denied++
		}
	}
	if known != 3 || denied != 6 {
		t.Errorf("known = %d, denied = %d, want 3/6", known, denied)
	}

	// Second sample runs ONLY the selected probe — no ladder re-selection.
	s2 := d.Sample(context.Background())
	if s2.State != StateAvailable {
		t.Fatalf("second state = %v, want available", s2.State)
	}
	cmds := f.commands()
	if len(cmds) != 2 {
		t.Fatalf("execs = %d, want 2 (one selection pass + one sample), got %v", len(cmds), cmds)
	}
	if cmds[0] != ssCmd || cmds[1] != ssCmd {
		t.Errorf("execs = %v, want [%s, %s]", cmds, ssCmd, ssCmd)
	}
	if s2.Duration <= 0 {
		t.Error("Duration = 0, want a real wall time")
	}
}

// TestDetector_NoProbeTools_Unavailable: a host with no ss, no netstat and
// no lsof reports unavailable with the probes tried, and the absence is
// cached — a second sample runs no execs at all.
func TestDetector_NoProbeTools_Unavailable(t *testing.T) {
	f := newFakeConn()
	f.queue(absentTool(), absentTool(), absentTool(), absentTool())
	d := newTestDetector(t, f)

	s1 := d.Sample(context.Background())
	if s1.State != StateUnavailable {
		t.Fatalf("state = %v, want unavailable", s1.State)
	}
	if len(s1.Listeners) != 0 {
		t.Errorf("listeners = %d, want 0", len(s1.Listeners))
	}
	want := []string{"ss", "netstat", "lsof", "sockstat"}
	if strings.Join(s1.ProbesTried, ",") != strings.Join(want, ",") {
		t.Errorf("probes tried = %v, want %v (busybox is the same binary as netstat)", s1.ProbesTried, want)
	}

	// Absence is cached for the connection lifetime: a second sample does
	// not re-run the ladder.
	s2 := d.Sample(context.Background())
	if s2.State != StateUnavailable {
		t.Fatalf("second state = %v, want unavailable", s2.State)
	}
	if got := len(f.commands()); got != 4 {
		t.Errorf("execs = %d, want 4 (absence cached, no re-probe)", got)
	}
}

// TestDetector_UnsupportedOutput_TriesNextProbeOnce: ss exists but emits an
// unrecognized body — the strategy is cached as failed and the next probe
// runs once; if it succeeds it becomes the selected probe.
func TestDetector_UnsupportedOutput_TriesNextProbeOnce(t *testing.T) {
	f := newFakeConn()
	netstatBody := "Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name\ntcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN      1234/sshd\n"
	f.queue(
		fakeResponse{result: framed("garbage that is not ss shape")},
		fakeResponse{result: framed(netstatBody)},
		fakeResponse{result: framed(netstatBody)},
	)
	d := newTestDetector(t, f)

	s1 := d.Sample(context.Background())
	if s1.State != StateAvailable {
		t.Fatalf("state = %v, want available", s1.State)
	}
	if s1.Probe != "netstat" {
		t.Fatalf("probe = %q, want netstat", s1.Probe)
	}
	if len(s1.Listeners) != 1 || s1.Listeners[0].Port != 22 {
		t.Errorf("listeners = %+v, want one 0.0.0.0:22", s1.Listeners)
	}
	if s1.Listeners[0].Process.Evidence != EvidenceKnown || s1.Listeners[0].Process.Name != "sshd" {
		t.Errorf("process = %+v, want known sshd", s1.Listeners[0].Process)
	}

	// The failed strategy is cached; the next sample runs netstat only.
	s2 := d.Sample(context.Background())
	if s2.Probe != "netstat" {
		t.Fatalf("second probe = %q, want netstat", s2.Probe)
	}
	cmds := f.commands()
	if len(cmds) != 3 || cmds[1] != netstatCmd || cmds[2] != netstatCmd {
		t.Errorf("execs = %v, want [ss, netstat, netstat]", cmds)
	}
}

// TestDetector_Exit127_AdvancesLadder: an absent ss is skipped; netstat
// answers.
func TestDetector_Exit127_AdvancesLadder(t *testing.T) {
	f := newFakeConn()
	f.queue(absentTool(), fakeResponse{result: framed("tcp 0 0 0.0.0.0:22 0.0.0.0:* LISTEN\n")})
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.Probe != "netstat" {
		t.Fatalf("probe = %q, want netstat (ss absent)", s.Probe)
	}
	cmds := f.commands()
	if len(cmds) != 2 || cmds[0] != ssCmd || cmds[1] != netstatCmd {
		t.Errorf("execs = %v, want [ss, netstat]", cmds)
	}
}

// TestDetector_NonzeroExit_WithStderr_CachesAndAdvances: a tool that exists
// but cannot run its job (usage error) is cached as failed, not retried.
func TestDetector_NonzeroExit_WithStderr_CachesAndAdvances(t *testing.T) {
	f := newFakeConn()
	f.queue(
		fakeResponse{result: &ExecResult{Stdout: []byte("NOCX-PD/1\nNOCX-PD/1\n"), Stderr: []byte("ss: invalid option -- 'H'\n"), ExitStatus: 1}},
		fakeResponse{result: framed("tcp 0 0 0.0.0.0:22 0.0.0.0:* LISTEN\n")},
	)
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.Probe != "netstat" {
		t.Fatalf("probe = %q, want netstat (ss unusable)", s.Probe)
	}
	// The failed strategy is recorded for diagnostics; the successful
	// sample's classification is clean.
	if !containsStr(s.ProbesTried, "ss") {
		t.Errorf("probes tried = %v, want ss recorded", s.ProbesTried)
	}
	if s.Classification != "" {
		t.Errorf("classification = %q, want empty on a clean success", s.Classification)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestDetector_NoMatchExit_IsValidEmpty: lsof exits 1 with NO matches — a
// valid empty sample, not a failure.
func TestDetector_NoMatchExit_IsValidEmpty(t *testing.T) {
	f := newFakeConn()
	f.queue(
		absentTool(),
		absentTool(),
		fakeResponse{result: &ExecResult{Stdout: []byte("NOCX-PD/1\nNOCX-PD/1\n"), ExitStatus: 1}},
	)
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.State != StateAvailable {
		t.Fatalf("state = %v, want available (lsof exit 1 = no matches)", s.State)
	}
	if s.Probe != "lsof" {
		t.Fatalf("probe = %q, want lsof", s.Probe)
	}
	if len(s.Listeners) != 0 {
		t.Errorf("listeners = %d, want 0", len(s.Listeners))
	}
}

// ---------------------------------------------------------------------------
// The three things that must not be got wrong
// ---------------------------------------------------------------------------

// TestDetector_FramingViolation_RefusedWhole: output without the sentinel is
// rejected whole — never scavenged for port numbers — and the connection is
// treated as undiscoverable until Retry.
func TestDetector_FramingViolation_RefusedWhole(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{result: &ExecResult{
		Stdout: []byte("Welcome to example.com\nss -lntp\n"),
	}})
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.State != StatePermissionOrPolicyRefused {
		t.Fatalf("state = %v, want permission-or-policy-refused", s.State)
	}
	if len(s.Listeners) != 0 {
		t.Errorf("listeners = %d, want 0 (rejected whole, not scavenged)", len(s.Listeners))
	}
	if !strings.Contains(s.Classification, "forced command") {
		t.Errorf("classification = %q, want the forced-command mention", s.Classification)
	}

	// Terminal until Retry: no further execs.
	if s2 := d.Sample(context.Background()); s2.State != StatePermissionOrPolicyRefused {
		t.Fatalf("second state = %v, want still refused", s2.State)
	}
	if got := len(f.commands()); got != 1 {
		t.Errorf("execs = %d, want 1 (terminal refusal runs nothing)", got)
	}

	// Retry clears it; the next sample tries again — and succeeds when the
	// host starts behaving.
	d.Retry()
	f.queue(fakeResponse{result: framed(knownRow)})
	s3 := d.Sample(context.Background())
	if s3.State != StateAvailable {
		t.Fatalf("state after Retry = %v, want available", s3.State)
	}
	if got := len(f.commands()); got != 2 {
		t.Errorf("execs after Retry = %d, want 2 (probed again)", got)
	}
}

// TestDetector_MaxSessions_Refused: "additional sessions refused" is a named
// state, never "no ports", and disables automatic discovery until Retry.
func TestDetector_MaxSessions_Refused(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{err: &ExecError{Kind: ExecErrSessionRefused}})
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.State != StatePermissionOrPolicyRefused {
		t.Fatalf("state = %v, want permission-or-policy-refused", s.State)
	}
	if !strings.Contains(s.Classification, "additional sessions refused") {
		t.Errorf("classification = %q, want the MaxSessions mention", s.Classification)
	}
	if s2 := d.Sample(context.Background()); s2.State != StatePermissionOrPolicyRefused {
		t.Fatalf("second state = %v, want still refused", s2.State)
	}
	if got := len(f.commands()); got != 1 {
		t.Errorf("execs = %d, want 1 (refusal is terminal)", got)
	}
	d.Retry()
	f.queue(fakeResponse{result: framed(knownRow)})
	s3 := d.Sample(context.Background())
	if s3.State != StateAvailable {
		t.Fatalf("state after Retry = %v, want available", s3.State)
	}
	if got := len(f.commands()); got != 2 {
		t.Errorf("execs after Retry = %d, want 2", got)
	}
}

// TestDetector_ExecProhibited_Refused: an exec request refused by policy is
// permission-or-policy-refused, distinct from tool absence.
func TestDetector_ExecProhibited_Refused(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{err: &ExecError{Kind: ExecErrExecProhibited}})
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.State != StatePermissionOrPolicyRefused {
		t.Fatalf("state = %v, want permission-or-policy-refused", s.State)
	}
	if !strings.Contains(s.Classification, "exec request refused") {
		t.Errorf("classification = %q, want the refusal mention", s.Classification)
	}
}

// TestDetector_Timeout_BacksOff: a probe that times out is
// failed-transiently, and the typed backoff suppresses samples inside the
// window without executing, then lets the next one through.
func TestDetector_Timeout_BacksOff(t *testing.T) {
	f := newFakeConn()
	blocked := make(chan struct{})
	f.block = blocked
	defer close(blocked)
	d := newTestDetector(t, f, WithSampleTimeout(50*time.Millisecond), WithBackoffLevels([]time.Duration{30 * time.Millisecond}))

	s1 := d.Sample(context.Background())
	if s1.State != StateFailedTransiently {
		t.Fatalf("state = %v, want failed-transiently", s1.State)
	}
	if !strings.Contains(s1.Classification, "timed out") {
		t.Errorf("classification = %q, want the timeout mention", s1.Classification)
	}

	// Inside the backoff window: no exec.
	s2 := d.Sample(context.Background())
	if s2.State != StateFailedTransiently {
		t.Fatalf("second state = %v, want failed-transiently (backing off)", s2.State)
	}
	if got := len(f.commands()); got != 1 {
		t.Errorf("execs = %d, want 1 (backoff window suppresses samples)", got)
	}

	// After the window, a sample executes again. Unblock the probe so it
	// succeeds — success resets the backoff.
	f.block = nil
	f.queue(fakeResponse{result: framed(knownRow)})
	time.Sleep(40 * time.Millisecond)
	s3 := d.Sample(context.Background())
	if s3.State != StateAvailable {
		t.Fatalf("state after backoff = %v, want available", s3.State)
	}
	if got := len(f.commands()); got != 2 {
		t.Errorf("execs = %d, want 2 (sample ran after the window)", got)
	}
}

// TestDetector_CancelInFlight_NoStateChange: canceling the caller's context
// while a sample is in flight returns the previous result marked Canceled —
// no state change, no new exec.
func TestDetector_CancelInFlight_NoStateChange(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{result: framed(ssMixedFixture)})
	d := newTestDetector(t, f)

	first := d.Sample(context.Background())
	if first.State != StateAvailable {
		t.Fatalf("first state = %v, want available", first.State)
	}

	f.block = make(chan struct{})
	defer close(f.block)
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	start := time.Now()
	s := d.Sample(ctx)
	if time.Since(start) > 5*time.Second {
		t.Fatal("Sample did not return promptly on cancel")
	}
	if !s.Canceled {
		t.Error("Canceled = false, want true")
	}
	if s.State != StateAvailable {
		t.Errorf("state = %v, want the previous available state", s.State)
	}
	if len(s.Listeners) != 9 {
		t.Errorf("listeners = %d, want the previous 9", len(s.Listeners))
	}
}

// TestDetector_CloseMidSample_DiscardsLateResult: closing the detector while
// a sample is in flight discards the late result — the previous state is
// returned marked Canceled, the detector's state is not overwritten with a
// failure, and no backoff is scheduled (spec §7.3: discard late results).
func TestDetector_CloseMidSample_DiscardsLateResult(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{result: framed(knownRow)})
	d := newTestDetector(t, f)

	first := d.Sample(context.Background())
	if first.State != StateAvailable {
		t.Fatalf("first state = %v, want available", first.State)
	}

	// Start a sample that blocks inside the probe, then close the detector
	// underneath it.
	release := make(chan struct{})
	f.block = release
	f.queue(fakeResponse{result: framed(knownRow)})
	resCh := make(chan Sample, 1)
	go func() { resCh <- d.Sample(context.Background()) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(f.commands()) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("in-flight sample never entered the probe (execs = %d)", len(f.commands()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(release) // unblock the probe; the lease is already closed

	select {
	case s := <-resCh:
		if !s.Canceled {
			t.Error("Canceled = false, want true (lease closed mid-sample)")
		}
		if s.State != StateAvailable {
			t.Errorf("state = %v, want the previous available state (late result discarded)", s.State)
		}
		if s.Classification != "" {
			t.Errorf("classification = %q, want empty (no failure promoted)", s.Classification)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Sample did not return after Close")
	}

	// No backoff was scheduled: a fresh sample still executes (and is
	// discarded again) instead of being suppressed by a backoff window.
	before := len(f.commands())
	_ = d.Sample(context.Background())
	if got := len(f.commands()); got != before+1 {
		t.Errorf("execs = %d, want %d (no backoff window after close)", got, before+1)
	}
}

// TestDetector_ConnectionLost_Transient: transport death mid-sample is
// failed-transiently, not a refusal and not "no ports".
func TestDetector_ConnectionLost_Transient(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{err: &ExecError{Kind: ExecErrConnectionLost}})
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.State != StateFailedTransiently {
		t.Fatalf("state = %v, want failed-transiently", s.State)
	}
	if !strings.Contains(s.Classification, "connection lost") {
		t.Errorf("classification = %q, want the loss mention", s.Classification)
	}
}

// TestDetector_IncompleteOutput_Transient: a framed body cut short (missing
// trailing sentinel) must not surface as "no ports".
func TestDetector_IncompleteOutput_Transient(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{result: &ExecResult{
		Stdout: []byte("NOCX-PD/1\nLISTEN 0 4096 127.0.0.1:53 0.0.0.0:*\n"),
	}})
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.State != StateFailedTransiently {
		t.Fatalf("state = %v, want failed-transiently (incomplete)", s.State)
	}
	if len(s.Listeners) != 0 {
		t.Errorf("listeners = %d, want 0 (partial table rejected)", len(s.Listeners))
	}
}

// TestDetector_AvailableLimited_Busybox: when every row's process evidence
// is unsupported (busybox netstat without -p), the sample is
// available-limited, never plain available with a blank process column.
func TestDetector_AvailableLimited_Busybox(t *testing.T) {
	f := newFakeConn()
	f.queue(
		absentTool(),
		fakeResponse{result: &ExecResult{Stdout: []byte("NOCX-PD/1\nNOCX-PD/1\n"), Stderr: []byte("netstat: invalid option -- 'p'\n"), ExitStatus: 1}},
		fakeResponse{result: framed(busyboxFixture)},
	)
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.State != StateAvailableLimited {
		t.Fatalf("state = %v, want available-limited", s.State)
	}
	if s.Probe != "busybox-netstat" {
		t.Fatalf("probe = %q, want busybox-netstat", s.Probe)
	}
	for _, l := range s.Listeners {
		if l.Process.Evidence != EvidenceUnsupported {
			t.Errorf("listener %d evidence = %q, want unsupported", l.Port, l.Process.Evidence)
		}
	}
}

// TestDetector_PendingBeforeFirstSample: State is pending, a defined value,
// before the first sample completes — never an empty string.
func TestDetector_PendingBeforeFirstSample(t *testing.T) {
	f := newFakeConn()
	d := newTestDetector(t, f)

	if got := d.State(); got != StatePending {
		t.Errorf("state before first sample = %q, want %q", got, StatePending)
	}
	if got := d.State(); got == "" {
		t.Error("state before first sample is the empty string")
	}
}

// ---------------------------------------------------------------------------
// Backoff and concurrency
// ---------------------------------------------------------------------------

func TestBackoff_EscalatesAndResets(t *testing.T) {
	b := NewBackoff()
	want := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i, w := range want {
		if got := b.Next(); got != w {
			t.Errorf("Next() #%d = %v, want %v", i, got, w)
		}
	}
	b.Reset()
	if got := b.Next(); got != 10*time.Second {
		t.Errorf("Next() after Reset = %v, want 10s", got)
	}
}

// TestDetector_OneInFlight: concurrent samples serialize — exactly one exec
// at a time per target (spec §4).
func TestDetector_OneInFlight(t *testing.T) {
	f := newFakeConn()
	release := make(chan struct{})
	f.block = release
	f.queue(
		fakeResponse{result: framed(knownRow)},
		fakeResponse{result: framed(knownRow)},
	)
	d := newTestDetector(t, f)

	res1 := make(chan Sample, 1)
	res2 := make(chan Sample, 1)
	go func() { res1 <- d.Sample(context.Background()) }()

	// Wait until the first sample is inside the probe, then start the
	// second — it must wait on the one-in-flight guard, not execute.
	deadline := time.Now().Add(5 * time.Second)
	for len(f.commands()) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("first sample never entered the probe (execs = %d)", len(f.commands()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	go func() { res2 <- d.Sample(context.Background()) }()

	time.Sleep(50 * time.Millisecond)
	if got := len(f.commands()); got != 1 {
		t.Fatalf("execs while first sample in flight = %d, want 1", got)
	}

	// Release the first probe; the second sample then runs its own.
	close(release)
	if s := <-res1; s.State != StateAvailable {
		t.Errorf("first sample state = %v, want available", s.State)
	}
	select {
	case s := <-res2:
		if s.State != StateAvailable {
			t.Errorf("second sample state = %v, want available", s.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second sample never ran after the first finished")
	}
	if got := len(f.commands()); got != 2 {
		t.Errorf("execs = %d, want 2 (both samples ran, serially)", got)
	}
}

// A command nocx itself refused is TERMINAL, and it is not a fact about the
// host (nocx-e4ir3).
//
// internal/ssh refuses a remote command at or above its declared bound before
// sending it. That error arrives here like any other, and the ladder's default
// for an error it does not recognise is a transient failure — the honest
// default for a host that might behave differently in a minute. It is the wrong
// answer for this one: the command will be exactly as long on every retry, so
// the ladder would back off and re-run a command that cannot ever be sent,
// forever, while the sample says "failed transiently" about a host that did
// nothing.
//
// It is a refusal, and the classification says who refused. Attributing it to
// the far side would be a guess about the host, which this ladder deliberately
// never makes.
func TestDetector_CommandRefusedByOurOwnBound_IsTerminalAndNamesUs(t *testing.T) {
	f := newFakeConn()
	f.queue(fakeResponse{err: &ExecError{Kind: ExecErrCommandTooLong}})
	d := newTestDetector(t, f)

	s := d.Sample(context.Background())
	if s.State == StateFailedTransiently {
		t.Fatalf("state = %v: a command nocx refuses is refused on every retry, so a transient "+
			"state schedules a backoff for a probe that can never succeed", s.State)
	}
	if s.State != StatePermissionOrPolicyRefused {
		t.Fatalf("state = %v, want permission-or-policy-refused", s.State)
	}
	if !strings.Contains(s.Classification, "nocx") {
		t.Errorf("classification = %q — it must name nocx as the refuser; the host did nothing", s.Classification)
	}
}
