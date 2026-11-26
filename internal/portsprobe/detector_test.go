package portsprobe

// The ladder is the far helper's half of port discovery (nocx-oalo1.13.2): the
// helper runs the host's own tools, one rung at a time, and answers what it
// read. These tests drive it through an injected Runner, so the classification
// — absent, unsupported, transient, canceled — is asserted without an exec and
// without a host.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ssTable is a probe body in ss's own dialect, for tests that only need the
// ladder to accept a valid table.
const ssTable = `LISTEN 0      128          0.0.0.0:22    0.0.0.0:* users:(("sshd",pid=1,fd=3))`

// fakeRunner is the injected exec seam: it records every rung it was asked to
// run, and answers through run.
type fakeRunner struct {
	mu  sync.Mutex
	ran []Command
	run func(ctx context.Context, c Command) (*Result, error)
}

func (f *fakeRunner) Run(ctx context.Context, c Command) (*Result, error) {
	f.mu.Lock()
	f.ran = append(f.ran, c)
	f.mu.Unlock()
	return f.run(ctx, c)
}

// programs lists the rungs that actually executed, in order.
func (f *fakeRunner) programs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.ran))
	for _, c := range f.ran {
		out = append(out, c.Argv[0])
	}
	return out
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ran)
}

// forget clears the record without changing how the runner answers.
func (f *fakeRunner) forget() {
	f.mu.Lock()
	f.ran = nil
	f.mu.Unlock()
}

func absentProgram(name string) error {
	return fmt.Errorf("%w: %s", ErrCommandAbsent, name)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// newTestDetector builds a Detector over runner and applies the knobs a test
// owns: the one-pass bound and the clock. Both are fields of the production
// struct whose production values are the defaults (DefaultSampleTimeout,
// time.Now) and which nothing outside a test ever sets, so they are set here
// by an unexported helper instead of by an option this package exports.
func newTestDetector(runner Runner, tweaks ...func(*Detector)) *Detector {
	d := NewDetector(runner)
	for _, tweak := range tweaks {
		tweak(d)
	}
	return d
}

// withSampleTimeout replaces the one-pass bound, so a test sees a timeout at
// its own scale rather than after the production ten seconds.
func withSampleTimeout(timeout time.Duration) func(*Detector) {
	return func(d *Detector) { d.timeout = timeout }
}

// withNow replaces the clock the backoff window is measured against. The
// window is a rule about time, and a test that waits ten real minutes to see
// the last level is a test nobody runs.
func withNow(now func() time.Time) func(*Detector) {
	return func(d *Detector) { d.now = now }
}

func TestTheLadderFallsFromSSToNetstatAndThenRunsOnlyTheSelectedRung(t *testing.T) {
	runner := &fakeRunner{run: func(_ context.Context, c Command) (*Result, error) {
		if c.Argv[0] == "netstat" {
			return &Result{Stdout: []byte(netstatFixture)}, nil
		}
		return nil, absentProgram(c.Argv[0])
	}}
	d := NewDetector(runner)

	s := d.Sample(context.Background())
	if s.State != StateAvailable {
		t.Fatalf("state = %q, want available (netstat answered)", s.State)
	}
	if s.Probe != "netstat" {
		t.Fatalf("probe = %q, want netstat", s.Probe)
	}
	if !equalStrings(s.ProbesTried, []string{"ss", "netstat"}) {
		t.Fatalf("probesTried = %v, want [ss netstat]", s.ProbesTried)
	}
	if len(s.Listeners) != 4 || s.Listeners[0].Port != 53 {
		t.Fatalf("listeners = %+v, want the fixture's four rows starting at port 53", s.Listeners)
	}
	if s.Listeners[0].Process.Evidence != EvidenceKnown || s.Listeners[0].Process.PID != 714 {
		t.Fatalf("process = %+v, want the known owner netstat printed", s.Listeners[0].Process)
	}

	// The second pass runs the selected rung and nothing above it: the missing
	// tools are cached for the connection's lifetime, not re-probed.
	runner.forget()
	if second := d.Sample(context.Background()); second.State != StateAvailable {
		t.Fatalf("second sample state = %q, want available", second.State)
	}
	if got := runner.programs(); !equalStrings(got, []string{"netstat"}) {
		t.Fatalf("second pass ran %v, want only the selected rung [netstat]", got)
	}
	// The diagnostics list is the connection's, not the pass's: it names each
	// rung once however many passes a long-lived connection makes.
	runner.forget()
	third := d.Sample(context.Background())
	if !equalStrings(third.ProbesTried, []string{"ss", "netstat"}) {
		t.Fatalf("third pass probesTried = %v, want [ss netstat] once each", third.ProbesTried)
	}
}

// TestEveryRungAbsentIsUnavailable is scenario 8's unit half: a host with no
// port tool at all answers `unavailable`, and the four rungs it tried are
// named. busybox's netstat is not tried after netstat itself is gone — it is
// the same binary.
func TestEveryRungAbsentIsUnavailable(t *testing.T) {
	runner := &fakeRunner{run: func(_ context.Context, c Command) (*Result, error) {
		return nil, absentProgram(c.Argv[0])
	}}

	s := NewDetector(runner).Sample(context.Background())
	if s.State != StateUnavailable {
		t.Fatalf("state = %q, want unavailable", s.State)
	}
	if !equalStrings(s.ProbesTried, []string{"ss", "netstat", "lsof", "sockstat"}) {
		t.Fatalf("probesTried = %v, want the four rungs without busybox's", s.ProbesTried)
	}
	if s.Classification != "no probe tool usable on this host" {
		t.Fatalf("classification = %q, want the ladder's own sentence", s.Classification)
	}
	if len(s.Listeners) != 0 {
		t.Fatalf("listeners = %+v, want none", s.Listeners)
	}
}

// TestANonZeroExitCachesTheRungAsUnsupported: a tool that exists but cannot do
// its job is tried once and skipped for the connection's lifetime, and the next
// rung answers.
func TestANonZeroExitCachesTheRungAsUnsupported(t *testing.T) {
	runner := &fakeRunner{run: func(_ context.Context, c Command) (*Result, error) {
		switch c.Argv[0] {
		case "ss":
			return &Result{Stderr: []byte("ss: invalid option -- 'p'"), ExitStatus: 2}, nil
		case "netstat":
			return &Result{Stdout: []byte(netstatFixture)}, nil
		}
		return nil, absentProgram(c.Argv[0])
	}}
	d := NewDetector(runner)

	s := d.Sample(context.Background())
	if s.State != StateAvailable {
		t.Fatalf("state = %q, want available (netstat answered after ss refused its flags)", s.State)
	}
	if !equalStrings(s.ProbesTried, []string{"ss", "netstat"}) {
		t.Fatalf("probesTried = %v, want [ss netstat]", s.ProbesTried)
	}

	runner.forget()
	d.Sample(context.Background())
	if got := runner.programs(); !equalStrings(got, []string{"netstat"}) {
		t.Fatalf("second pass ran %v, want the unusable rung cached away", got)
	}
}

// TestLsofExitOneWithNoMatchesIsAnEmptySample: lsof exits 1 when nothing
// matches, and that is a valid empty sample rather than a failure.
func TestLsofExitOneWithNoMatchesIsAnEmptySample(t *testing.T) {
	runner := &fakeRunner{run: func(_ context.Context, c Command) (*Result, error) {
		if c.Argv[0] == "lsof" {
			return &Result{ExitStatus: 1}, nil
		}
		return nil, absentProgram(c.Argv[0])
	}}

	s := NewDetector(runner).Sample(context.Background())
	if s.State != StateAvailable {
		t.Fatalf("state = %q, want available (lsof said there is nothing)", s.State)
	}
	if s.Probe != "lsof" {
		t.Fatalf("probe = %q, want lsof", s.Probe)
	}
	if len(s.Listeners) != 0 {
		t.Fatalf("listeners = %+v, want none", s.Listeners)
	}
}

// TestTruncatedOutputIsTransient: a capped capture is a partial table, and a
// partial table must never surface as "no ports".
func TestTruncatedOutputIsTransient(t *testing.T) {
	runner := &fakeRunner{run: func(_ context.Context, c Command) (*Result, error) {
		if c.Argv[0] == "ss" {
			return &Result{Stdout: []byte("LISTEN 0 1 "), Truncated: true}, nil
		}
		return nil, absentProgram(c.Argv[0])
	}}

	s := NewDetector(runner).Sample(context.Background())
	if s.State != StateFailedTransiently {
		t.Fatalf("state = %q, want failed-transiently", s.State)
	}
	if s.Classification != "probe output incomplete" {
		t.Fatalf("classification = %q, want the truncation sentence", s.Classification)
	}
}

// TestAPassPastItsBoundTimesOutAndIsNeverConnectionLoss: the bound is the
// sample's own, and the far helper has no connection to lose — the sentence a
// person is shown must say the probe timed out.
func TestAPassPastItsBoundTimesOutAndIsNeverConnectionLoss(t *testing.T) {
	runner := &fakeRunner{run: func(ctx context.Context, _ Command) (*Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}

	s := newTestDetector(runner, withSampleTimeout(20*time.Millisecond)).Sample(context.Background())
	if s.State != StateFailedTransiently {
		t.Fatalf("state = %q, want failed-transiently", s.State)
	}
	if s.Classification != "probe timed out" {
		t.Fatalf("classification = %q, want %q", s.Classification, "probe timed out")
	}
	if strings.Contains(strings.ToLower(s.Classification), "lost") {
		t.Fatalf("classification = %q: a probe timeout is not a lost connection", s.Classification)
	}
}

// TestACanceledPassDiscardsItsResult: a canceled request returns the previous
// sample marked canceled, and never promotes a half-run pass to state.
func TestACanceledPassDiscardsItsResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeRunner{run: func(runCtx context.Context, _ Command) (*Result, error) {
		cancel()
		return nil, runCtx.Err()
	}}
	d := NewDetector(runner)

	s := d.Sample(ctx)
	if !s.Canceled {
		t.Fatalf("sample %+v was not marked canceled", s)
	}
	if s.State != StatePending {
		t.Fatalf("state = %q, want the pre-first-sample state pending", s.State)
	}
}

// fakeClock is the detector's clock, owned by the test: the backoff is a rule
// about a window, and the window is what this moves.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// TestTransientFailureBacksOffFourLevelsAndResetsOnSuccess: 10 s → 30 s → 2 min
// → 10 min, and a good sample returns to the first level.
func TestTransientFailureBacksOffFourLevelsAndResetsOnSuccess(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	var healthy bool
	runner := &fakeRunner{run: func(_ context.Context, c Command) (*Result, error) {
		switch {
		case c.Argv[0] != "ss":
			return nil, absentProgram(c.Argv[0])
		case healthy:
			return &Result{Stdout: []byte(ssTable)}, nil
		default:
			return &Result{Truncated: true}, nil
		}
	}}
	d := newTestDetector(runner, withNow(clock.now))

	if s := d.Sample(context.Background()); s.State != StateFailedTransiently {
		t.Fatalf("first sample state = %q, want failed-transiently", s.State)
	}
	ran := runner.count()

	// Each level is a window: nothing runs inside it, and the first sample
	// after it runs exactly one rung.
	for _, level := range []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute} {
		clock.advance(level - time.Millisecond)
		d.Sample(context.Background())
		if got := runner.count(); got != ran {
			t.Fatalf("a sample inside the %s window ran %d rungs, want %d (the cached answer)", level, got, ran)
		}
		clock.advance(time.Millisecond)
		d.Sample(context.Background())
		if got := runner.count(); got != ran+1 {
			t.Fatalf("a sample after the %s window ran %d rungs, want %d", level, got, ran+1)
		}
		ran++
	}

	// A success returns the ladder to its first level: the next failure waits
	// 10 s, not 10 min.
	healthy = true
	clock.advance(10 * time.Minute)
	if s := d.Sample(context.Background()); s.State != StateAvailable {
		t.Fatalf("the successful sample answered %q", s.State)
	}
	healthy = false
	ran = runner.count()
	d.Sample(context.Background())
	if got := runner.count(); got != ran+1 {
		t.Fatalf("the failure after a success ran %d rungs, want %d", got, ran+1)
	}
	ran++
	clock.advance(9 * time.Second)
	d.Sample(context.Background())
	if got := runner.count(); got != ran {
		t.Fatal("the ladder did not return to the 10 s first level after a success")
	}
}

// TestOneSampleIsInFlightPerConnection: a concurrent request waits for the
// pass already running, and a request whose context dies while it waits is
// answered canceled rather than started.
func TestOneSampleIsInFlightPerConnection(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	runner := &fakeRunner{run: func(_ context.Context, _ Command) (*Result, error) {
		close(entered)
		<-release
		return &Result{Stdout: []byte(ssTable)}, nil
	}}
	d := NewDetector(runner)

	first := make(chan Sample, 1)
	go func() { first <- d.Sample(context.Background()) }()
	<-entered

	waiting, cancel := context.WithCancel(context.Background())
	cancel()
	if s := d.Sample(waiting); !s.Canceled {
		t.Fatalf("a sample canceled behind an in-flight pass answered %+v, want canceled", s)
	}
	if got := runner.count(); got != 1 {
		t.Fatalf("%d rungs ran, want exactly the in-flight one", got)
	}

	close(release)
	if s := <-first; s.State != StateAvailable {
		t.Fatalf("the in-flight pass answered %q", s.State)
	}
}

// TestTheLadderIsArgvAndNeverAShell pins the shape of every rung: a program and
// its fixed arguments, so no rung can be re-read as shell syntax by the host's
// own shell.
func TestTheLadderIsArgvAndNeverAShell(t *testing.T) {
	want := map[string][]string{
		"ss":       {"ss", "-H", "-lntp"},
		"netstat":  {"netstat", "-lntp"},
		"lsof":     {"lsof", "-nP", "-iTCP", "-sTCP:LISTEN"},
		"sockstat": {"sockstat", "-4", "-l"},
	}
	if len(ladder) != len(want)+1 {
		t.Fatalf("the ladder holds %d rungs, want %d (busybox's netstat included)", len(ladder), len(want)+1)
	}
	for _, st := range ladder {
		if st.name == "busybox-netstat" {
			if !equalStrings(st.argv, []string{"netstat", "-ltn"}) {
				t.Fatalf("busybox netstat argv = %v, want netstat -ltn", st.argv)
			}
			continue
		}
		if !equalStrings(st.argv, want[st.name]) {
			t.Fatalf("%s argv = %v, want %v", st.name, st.argv, want[st.name])
		}
		for _, arg := range st.argv {
			if strings.ContainsAny(arg, "|&;<>$`\\'\" \n") {
				t.Fatalf("%s carries %q: a rung is argv and never shell syntax", st.name, arg)
			}
		}
	}
	// ss's table is locale-dependent: the one rung that needs an environment
	// declares it beside itself.
	for _, st := range ladder {
		if st.name == "ss" && !equalStrings(st.env, []string{"LC_ALL=C"}) {
			t.Fatalf("ss env = %v, want LC_ALL=C", st.env)
		}
	}
}

var _ Runner = (*fakeRunner)(nil)
