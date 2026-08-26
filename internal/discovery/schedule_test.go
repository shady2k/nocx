package discovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/testwait"
)

// ---------------------------------------------------------------------------
// Scripted ssh-flavored fake — the scheduler's connector surface speaks
// ssh.DiscoveryConn (the connector stays SSH-shaped at the composition
// boundary; the scheduler adapts it to the exec seam). queueConn pre-scripts
// the NEXT acquisition (a refusal, a loss) instead of pre-seeding a list the
// scheduler never reads.
// ---------------------------------------------------------------------------

type sshFakeConn struct {
	mu        sync.Mutex
	responses []sshFakeResponse
	execs     []string
	block     chan struct{} // when non-nil, Exec blocks until ctx done or this closes
	done      chan struct{}
	closed    bool
	lost      bool
	// autoValid answers every exec with a valid "normal host" sample,
	// without consuming a queued response.
	autoValid bool
}

type sshFakeResponse struct {
	result *ssh.ExecResult
	err    error
}

func newSSHFakeConn() *sshFakeConn {
	return &sshFakeConn{done: make(chan struct{})}
}

func (f *sshFakeConn) Exec(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	f.mu.Lock()
	f.execs = append(f.execs, cmd)
	block := f.block
	f.mu.Unlock()

	if block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-block:
		case <-f.done:
			return nil, ssh.ErrExecLost
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.closed:
		return nil, ssh.ErrExecClosed
	case f.lost:
		return nil, ssh.ErrExecLost
	}
	if f.autoValid {
		return framedSSH(knownRow), nil
	}
	if len(f.responses) == 0 {
		return nil, errors.New("fake: no queued response for " + cmd)
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp.result, resp.err
}

func (f *sshFakeConn) Done() <-chan struct{} { return f.done }
func (f *sshFakeConn) LostErr() error        { return nil }
func (f *sshFakeConn) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *sshFakeConn) queue(resps ...sshFakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, resps...)
}

func (f *sshFakeConn) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...)
}

// framedSSH builds the ssh lease's version of a sentinel-framed probe
// response — what the real lease returns, before adaptSSH converts it.
func framedSSH(body string) *ssh.ExecResult {
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return &ssh.ExecResult{Stdout: []byte("NOCX-PD/1\n" + body + "NOCX-PD/1\n"), ExitStatus: 0}
}

type fakeConnector struct {
	mu     sync.Mutex
	conns  []*sshFakeConn
	queued []*sshFakeConn
	err    error
	// autoValid answers every exec on FRESH conns with a valid "normal
	// host" sample. queueConn'd conns are scripted by hand and skip this.
	autoValid bool
}

func (c *fakeConnector) DiscoveryConn(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.DiscoveryConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	if len(c.queued) > 0 {
		f := c.queued[0]
		c.queued = c.queued[1:]
		c.conns = append(c.conns, f)
		return f, nil
	}
	f := newSSHFakeConn()
	f.autoValid = c.autoValid
	c.conns = append(c.conns, f)
	return f, nil
}

// queueConn hands the next acquisition a pre-scripted conn.
func (c *fakeConnector) queueConn(f *sshFakeConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queued = append(c.queued, f)
}

func (c *fakeConnector) acquired() []*sshFakeConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*sshFakeConn(nil), c.conns...)
}

func (c *fakeConnector) execCount() int {
	n := 0
	for _, f := range c.acquired() {
		n += len(f.commands())
	}
	return n
}

// testScheduler builds a scheduler with tiny cadence for tests and a
// t.Cleanup Close. Cadence is scaled down so a test asserts on wall time in
// milliseconds instead of seconds.
func testScheduler(t *testing.T, conn *fakeConnector, opts ...SchedulerOption) *Scheduler {
	t.Helper()
	base := []SchedulerOption{
		WithSettleDelay(10 * time.Millisecond),
		WithPromptDebounce(15 * time.Millisecond),
		WithSampleInterval(25 * time.Millisecond),
	}
	conn.autoValid = true
	s := NewScheduler(conn, log.NewSlogAdapter(nil), append(base, opts...)...)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestScheduler_SettleSampleAfterConnectionUp(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn, WithSettleDelay(20*time.Millisecond))

	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())

	// One sample must run once the settle delay passes — the panel that
	// samples instantly shows an empty host (spec §4).
	testwait.WaitFor(t, "settle sample to run", func() bool { return conn.execCount() >= 1 })
	// No observable event represents the absence of a periodic sample; retain
	// this short window to exercise the negative cadence assertion.
	time.Sleep(60 * time.Millisecond)
	if got := conn.execCount(); got != 1 {
		t.Fatalf("exec count after settle = %d, want exactly 1", got)
	}
	st := s.Status("ssh:p1:1")
	if st.Sample.State != StateAvailable {
		t.Fatalf("state after settle = %q, want %q", st.Sample.State, StateAvailable)
	}
	if st.Host != "host.example" {
		t.Fatalf("status host = %q, want %q", st.Host, "host.example")
	}
}

func TestScheduler_PromptDebounceCoalesces(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn)
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	testwait.WaitFor(t, "settle sample", func() bool { return conn.execCount() >= 1 })

	// A user hammering Enter must not queue probes: five rapid hints
	// produce exactly ONE debounced sample.
	for range 5 {
		s.PromptHint("ssh:p1:1")
	}
	testwait.WaitFor(t, "debounced sample", func() bool { return conn.execCount() >= 2 })
	// No observable event represents the absence of another debounced sample;
	// retain this short window to exercise the negative cadence assertion.
	time.Sleep(60 * time.Millisecond)
	if got := conn.execCount(); got != 2 {
		t.Fatalf("exec count after 5 prompt hints = %d, want exactly 2 (settle + one debounced)", got)
	}
}

func TestScheduler_PromptHintsWhileSamplingCoalesceToOne(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn)
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	testwait.WaitFor(t, "settle sample", func() bool { return conn.execCount() >= 1 })

	f := conn.acquired()[0]
	block := make(chan struct{})
	f.block = block

	// One hint starts a debounced sample that blocks on the remote exec.
	s.PromptHint("ssh:p1:1")
	testwait.WaitFor(t, "blocked sample to start", func() bool { return conn.execCount() >= 2 })

	// More hints while the sample is in flight: at most one follow-up.
	for range 5 {
		s.PromptHint("ssh:p1:1")
	}
	close(block)

	// settle + blocked sample + exactly one coalesced follow-up.
	testwait.WaitFor(t, "coalesced follow-up sample", func() bool { return conn.execCount() >= 3 })
	// No observable event represents the absence of another coalesced sample;
	// retain this short window to exercise the negative cadence assertion.
	time.Sleep(80 * time.Millisecond)
	if got := conn.execCount(); got != 3 {
		t.Fatalf("exec count = %d, want exactly 3 (settle + blocked + one coalesced)", got)
	}
}

func TestScheduler_HiddenPaneStopsPeriodicSampling(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn)
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	testwait.WaitFor(t, "settle sample", func() bool { return conn.execCount() >= 1 })

	// No watcher yet: periodic sampling must NOT run — a background poll
	// with nobody rendering it is the defect this cadence exists to avoid.
	// No observable event represents hidden periodic work; retain this window
	// to exercise the negative cadence assertion.
	time.Sleep(120 * time.Millisecond)
	if got := conn.execCount(); got != 1 {
		t.Fatalf("exec count with no visible watcher = %d, want 1 (settle only)", got)
	}

	// Panel opens: periodic sampling starts.
	s.SetVisible("ssh:p1:1", true)
	testwait.WaitFor(t, "periodic samples", func() bool { return conn.execCount() >= 3 })

	// Panel hides: periodic sampling stops. Snapshot the count first — a
	// timer that already fired before the hide may legitimately land one
	// more sample.
	beforeHide := conn.execCount()
	s.SetVisible("ssh:p1:1", false)
	// No observable event represents the absence of work after hiding; retain
	// this short window to exercise the negative cadence assertion.
	time.Sleep(80 * time.Millisecond)
	if got := conn.execCount(); got != beforeHide {
		t.Fatalf("exec count after hide = %d, want %d (no periodic samples while hidden)", got, beforeHide)
	}
}

func TestScheduler_PauseSuppressesAutomaticSamples(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn)
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	testwait.WaitFor(t, "settle sample", func() bool { return conn.execCount() >= 1 })

	s.SetPaused("ssh:p1:1", true)

	// A prompt hint and a reconnect must both be suppressed while paused.
	s.PromptHint("ssh:p1:1")
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	// No observable event represents paused automatic work; retain this short
	// window to exercise the negative cadence assertion.
	time.Sleep(100 * time.Millisecond)
	if got := conn.execCount(); got != 1 {
		t.Fatalf("exec count while paused = %d, want 1", got)
	}

	// Resume restores automatic sampling.
	s.SetPaused("ssh:p1:1", false)
	testwait.WaitFor(t, "sample after resume", func() bool { return conn.execCount() >= 2 })
}

func TestScheduler_SampleNowActsAsRetry(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn)

	// First sample: the server refuses the extra session (MaxSessions 1).
	f0 := newSSHFakeConn()
	f0.queue(sshFakeResponse{err: ssh.ErrExecSessionRefused})
	conn.queueConn(f0)

	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	// Wait for the REFUSAL TO BE RECORDED, not for the exec to be issued.
	// execCount rises when the command reaches the fake conn; Sample.State is
	// written afterwards, once the scheduler has processed the error. Gating
	// on the first while asserting the second read "pending" about 1% of the
	// time (nocx-s8df).
	testwait.WaitFor(t, "refusal recorded in state", func() bool {
		return s.Status("ssh:p1:1").Sample.State == StatePermissionOrPolicyRefused
	})
	// And it took exactly one exec to get there — the assertion the old
	// execCount gate was carrying, kept rather than lost to the fix.
	if got := conn.execCount(); got != 1 {
		t.Fatalf("exec count for the refused sample = %d, want 1", got)
	}

	// Automatic sampling is disabled by the refusal; SampleNow (the panel's
	// Retry) clears it and samples immediately.
	f0.queue(sshFakeResponse{result: framedSSH(knownRow)})
	s.SampleNow("ssh:p1:1")
	testwait.WaitFor(t, "retry sample", func() bool {
		return s.Status("ssh:p1:1").Sample.State == StateAvailable
	})
}

func TestScheduler_ConnectionLossMarksLostAndReconnectResamples(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn)
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	testwait.WaitFor(t, "settle sample", func() bool { return conn.execCount() >= 1 })

	// Transport dies: the lease's Done channel closes (the loss watcher).
	// A result from the old connection must never apply after reconnect.
	f0 := conn.acquired()[0]
	close(f0.done)
	testwait.WaitFor(t, "conn lost", func() bool { return s.Status("ssh:p1:1").ConnLost })

	// No further execs are attempted on the dead lease.
	s.PromptHint("ssh:p1:1")
	s.SetVisible("ssh:p1:1", true)
	// No observable event represents the absence of probes after loss; retain
	// this short window to exercise the negative cadence assertion.
	time.Sleep(80 * time.Millisecond)
	if got := conn.execCount(); got != 1 {
		t.Fatalf("exec count after loss = %d, want 1 (no probes on a dead connection)", got)
	}

	// Reconnect: ConnectionUp resets the stale result and a fresh detector
	// (fresh lease — probe selection is once per connection) samples.
	f1 := newSSHFakeConn()
	f1.queue(sshFakeResponse{result: framedSSH(knownRow)})
	conn.queueConn(f1)
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	// Wait on the STATE, not the exec count: the count increments while the
	// sample is still in flight, before the result lands in the status.
	testwait.WaitFor(t, "post-reconnect sample", func() bool {
		return s.Status("ssh:p1:1").Sample.State == StateAvailable
	})
	st := s.Status("ssh:p1:1")
	if st.ConnLost {
		t.Fatal("ConnLost still true after reconnect")
	}
	if st.Sample.State != StateAvailable {
		t.Fatalf("state after reconnect = %q, want %q", st.Sample.State, StateAvailable)
	}
}

func TestScheduler_ConnectionDownReleasesLease(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn)
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	testwait.WaitFor(t, "settle sample", func() bool { return conn.execCount() >= 1 })

	// Last tab on the profile closed: the lease is released and the target
	// is forgotten — no background poll outlives its consumer.
	f0 := conn.acquired()[0]
	s.ConnectionDown("ssh:p1:1")
	testwait.WaitFor(t, "lease release", func() bool {
		f0.mu.Lock()
		defer f0.mu.Unlock()
		return f0.closed
	})

	s.PromptHint("ssh:p1:1")
	s.SetVisible("ssh:p1:1", true)
	// No observable event represents work after teardown; retain this short
	// window to exercise the negative cadence assertion.
	time.Sleep(60 * time.Millisecond)
	if got := conn.execCount(); got != 1 {
		t.Fatalf("exec count after ConnectionDown = %d, want 1", got)
	}
}

func TestScheduler_StatusPendingBeforeFirstSample(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn)
	st := s.Status("ssh:never:1")
	if st.Sample.State != StatePending {
		t.Fatalf("state before any connection = %q, want %q", st.Sample.State, StatePending)
	}
	if st.ConnLost || st.Paused || st.Visible {
		t.Fatalf("fresh status must be quiescent, got %+v", st)
	}
	if st.Sample.Listeners != nil {
		t.Fatalf("no listeners before any sample, got %d", len(st.Sample.Listeners))
	}
}

// fakeLocalProvider is a scripted provider for the local target: cadence
// tests drive it, so the machine itself is never probed in scheduler tests
// (the real-machine proof lives in internal/nativeports).
type fakeLocalProvider struct {
	sample Sample
}

func (f *fakeLocalProvider) Sample(context.Context) Sample { return f.sample }
func (f *fakeLocalProvider) Retry()                        {}
func (f *fakeLocalProvider) Close() error                  { return nil }

// localTestProvider wires a scripted "available" provider for LocalTargetID.
func localTestProvider() SchedulerOption {
	return WithLocalProvider(func(log.Logger) Provider {
		return &fakeLocalProvider{sample: Sample{
			State: StateAvailable,
			Listeners: []Listener{{
				Family:  FamilyIPv4,
				Address: "127.0.0.1",
				Port:    9999,
				Process: Process{Evidence: EvidenceKnown, Name: "test", PID: 1},
			}},
			Probe: "fake-local",
		}}
	})
}

// TestScheduler_LocalTarget_SettlesAndSamples: the reserved LocalTargetID
// behaves like any target — ConnectionUp schedules a settle sample — but
// the sample comes from the local provider, never from the SSH connector
// (the local target has nothing to dial).
func TestScheduler_LocalTarget_SettlesAndSamples(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn, localTestProvider())
	s.ConnectionUp(LocalTargetID, "machine-host")

	testwait.WaitFor(t, "local settle sample", func() bool {
		st := s.Status(LocalTargetID)
		return st.Sample.State == StateAvailable || st.Sample.State == StateAvailableLimited
	})
	st := s.Status(LocalTargetID)
	if st.Host != "machine-host" {
		t.Fatalf("host = %q, want machine-host", st.Host)
	}
	if got := conn.execCount(); got != 0 {
		t.Fatalf("ssh connector execs = %d, want 0 (the local target never dials)", got)
	}
}

// TestScheduler_LocalAndSSHTargets_RescopeIndependently is the switching
// contract: a local tab and an SSH tab each scope the panel to their own
// target, and closing either one tears down exactly that target — never
// the other.
func TestScheduler_LocalAndSSHTargets_RescopeIndependently(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn, localTestProvider())

	s.ConnectionUp(LocalTargetID, "machine-host")
	s.ConnectionUp("ssh:p1:1", "host.example", testConnectOption())
	testwait.WaitFor(t, "local settle sample", func() bool {
		st := s.Status(LocalTargetID)
		return st.Sample.State == StateAvailable || st.Sample.State == StateAvailableLimited
	})
	testwait.WaitFor(t, "ssh settle sample", func() bool {
		return s.Status("ssh:p1:1").Sample.State == StateAvailable
	})

	if got := s.Status(LocalTargetID).Host; got != "machine-host" {
		t.Fatalf("local host = %q, want machine-host", got)
	}
	if got := s.Status("ssh:p1:1").Host; got != "host.example" {
		t.Fatalf("ssh host = %q, want host.example", got)
	}

	// Closing the local tab leaves the SSH target sampling.
	s.ConnectionDown(LocalTargetID)
	if st := s.Status(LocalTargetID); st.Sample.State != StatePending {
		t.Fatalf("local state after down = %q, want pending (target forgotten)", st.Sample.State)
	}
	if st := s.Status("ssh:p1:1"); st.Sample.State != StateAvailable {
		t.Fatalf("ssh state after local down = %q, want still available", st.Sample.State)
	}

	// And the reverse: closing the SSH tab leaves nothing behind.
	s.ConnectionDown("ssh:p1:1")
	if st := s.Status("ssh:p1:1"); st.Sample.State != StatePending {
		t.Fatalf("ssh state after down = %q, want pending", st.Sample.State)
	}
}

// TestScheduler_LocalTarget_PauseSuppressesLocalProbes: Pause governs the
// local machine too — a local target is a background poll like any other.
func TestScheduler_LocalTarget_PauseSuppressesLocalProbes(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn, localTestProvider())
	s.ConnectionUp(LocalTargetID, "machine-host")
	testwait.WaitFor(t, "local settle sample", func() bool {
		st := s.Status(LocalTargetID)
		return st.Sample.State == StateAvailable || st.Sample.State == StateAvailableLimited
	})

	before := s.Status(LocalTargetID).Sample
	s.SetPaused(LocalTargetID, true)
	s.PromptHint(LocalTargetID)
	// No observable event represents the paused local target staying quiet;
	// retain this short window to exercise the negative cadence assertion.
	time.Sleep(80 * time.Millisecond)
	after := s.Status(LocalTargetID)
	if !after.Paused {
		t.Fatal("Paused not echoed on the local status")
	}
	if after.Sample.State != before.State {
		t.Fatalf("local state changed while paused: %q -> %q", before.State, after.Sample.State)
	}
}

// testConnectOption returns the resolved-config option the transport would
// pass — the lease the pool keys by.
func testConnectOption() ssh.ConnectOption {
	return func(c *ssh.ConnectConfig) {
		c.User = "test"
	}
}

// A connection that has come up but not yet sampled is a state a user looks
// at, and it went out on the wire as State "" — in no enum, matching no arm
// of the renderer's switch, so the Ports panel drew a heading with nothing
// under it (owner, 2026-08-04). The absent-target path had been guarded and
// this one had not.
func TestStatus_AfterConnectionUpBeforeFirstSample_IsPendingNotEmpty(t *testing.T) {
	conn := &fakeConnector{}
	s := testScheduler(t, conn, WithSettleDelay(time.Hour))

	s.ConnectionUp("ssh:p1:1", "host.example")

	got := s.Status("ssh:p1:1")
	if got.Sample.State == "" {
		t.Fatal("state is the zero string: the panel renders this as an empty section, not as a degrade")
	}
	if got.Sample.State != StatePending {
		t.Fatalf("state = %q, want %q", got.Sample.State, StatePending)
	}
}
