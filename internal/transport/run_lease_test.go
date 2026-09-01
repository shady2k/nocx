package transport

// The run lease supervisor (ADR-0020 decision 2), unit-tested with fakes:
// each bound fires its own reason, the escalation reaches the execution's
// process group in INT → TERM → KILL order and only as far as the execution
// forces, awaiting-takeover suspends the lease, and a run that completes
// within its bounds is untouched. The PROCESS side (a real child that
// ignores INT or TERM) is proven in ws_run_lease_test.go with a real pty;
// here the supervisor's decisions are pinned deterministically.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// fakeLeaseSession is the lease's process-group seam: it records every
// signal and simulates an execution that dies when the lease's escalation
// reaches the signal that kills it.
type fakeLeaseSession struct {
	mu      sync.Mutex
	signals []syscall.Signal
	// dieOn records the first signal that ends the execution. 0 = nothing
	// ever dies (KILL always ends it: the lease sends KILL regardless).
	dieOn syscall.Signal
}

// The fake is one pty with one job in front of it. The lease's escalation
// now names that job's group once and talks to it (nocx-uvac6.11), so the
// fake answers with a stable group and routes the signal through the same
// bookkeeping SignalForeground has always used — every assertion below still
// reads `signals`.
const fakeLeaseJobGroup = 1

func (f *fakeLeaseSession) ForegroundJob() (int, error) {
	return fakeLeaseJobGroup, nil
}

func (f *fakeLeaseSession) SignalProcessGroup(pgid int, sig syscall.Signal) error {
	if pgid != fakeLeaseJobGroup {
		return pty.ErrNoForeground
	}
	return f.SignalForeground(sig)
}

func (f *fakeLeaseSession) SignalForeground(sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sig == 0 {
		// The existence check: the execution is alive until it died.
		if len(f.signals) == 0 {
			return nil
		}
		if f.dieOn == 0 {
			return nil
		}
		for _, s := range f.signals {
			if s == f.dieOn {
				return pty.ErrNoForeground
			}
		}
		return nil
	}
	f.signals = append(f.signals, sig)
	return nil
}

func (f *fakeLeaseSession) got() []syscall.Signal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]syscall.Signal(nil), f.signals...)
}

// blockForever is the broker request under supervision: it never completes
// until release is closed (the renderer resolved, the ask was cancelled).
func blockForever(release <-chan struct{}) func(context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func newUnitLease(t *testing.T, sess runLeaseSession, cfg RunLeaseConfig) (*runLease, *outputRing) {
	t.Helper()
	if sess == nil {
		sess = &fakeLeaseSession{}
	}
	ring := newOutputRing()
	return &runLease{
		log:                 log.NewSlogAdapter(nil),
		sid:                 session.ID("sid-unit"),
		sess:                sess,
		ring:                ring,
		cfg:                 cfg,
		submissionDelivered: true,
	}, ring
}

func leaseErrorOf(t *testing.T, err error) *assistant.RunLeaseError {
	t.Helper()
	var le *assistant.RunLeaseError
	if !errors.As(err, &le) {
		t.Fatalf("supervise returned %v, want a RunLeaseError naming the bound", err)
	}
	return le
}

func TestRunLease_WallClockFiresAndEscalates(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{WallClock: 50 * time.Millisecond, SignalGrace: 50 * time.Millisecond})

	err := lease.supervise(context.Background(), blockForever(nil))
	le := leaseErrorOf(t, err)
	if le.Reason != content.TermTimeout {
		t.Fatalf("reason = %s, want timeout (the wall-clock bound)", le.Reason)
	}
	// The execution died on TERM: the escalation reached TERM, never KILL.
	sig := sess.got()
	if len(sig) != 2 || sig[0] != syscall.SIGINT || sig[1] != syscall.SIGTERM {
		t.Fatalf("signals = %v, want [INT TERM] — the escalation stops at the signal that works", sig)
	}
}

func TestRunLease_BoundBeforeSubmissionDoesNotEscalateOrClaimTerminalization(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{
		WallClock:   50 * time.Millisecond,
		SignalGrace: 20 * time.Millisecond,
	})
	lease.submissionDelivered = false

	err := lease.supervise(context.Background(), blockForever(nil))
	le := leaseErrorOf(t, err)
	if !le.SubmissionExpired {
		t.Fatal("lease error says the submission started, want an expired pre-execution submission")
	}
	_, sentence := classifyAskFailure(le, "qwen3")
	if sentence != "the run submission expired before execution started" {
		t.Fatalf("pre-execution sentence = %q, want the submission-expired sentence", sentence)
	}
	if strings.Contains(sentence, "terminalized") {
		t.Fatalf("pre-execution sentence = %q, must not claim terminalization", sentence)
	}
	if got := sess.got(); len(got) != 0 {
		t.Fatalf("signals = %v, want none when no submission exists", got)
	}
}

// A conventional shell has no authenticated start fact, but a delivered
// request still owns the command. The lease must keep the normal
// terminalization sentence instead of treating accountingStarted as proof
// that nothing was submitted.
func TestRunLease_DeliveredWithoutObservedStartStillTerminalizes(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{
		WallClock:   50 * time.Millisecond,
		SignalGrace: 20 * time.Millisecond,
	})
	lease.submissionDelivered = true

	err := lease.supervise(context.Background(), blockForever(nil))
	le := leaseErrorOf(t, err)
	if le.SubmissionExpired {
		t.Fatal("delivered request was reported as pre-execution")
	}
	sentence := assistant.RunLeaseSentence(le.Reason, le.SubmissionExpired)
	if !strings.Contains(sentence, "terminalized") ||
		!strings.Contains(sentence, "wall-clock") {
		t.Fatalf("delivered no-start sentence = %q, want wall-clock terminalization", sentence)
	}
	if got := sess.got(); len(got) == 0 {
		t.Fatal("delivered request sent no escalation signals")
	}
}

// A parent context can end the request without a lease timer firing. The
// execution still belongs to this run, so supervise must close it rather than
// return over a live process.
func TestRunLease_ContextCancellationEscalatesBeforeReturning(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: 0}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{SignalGrace: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- lease.supervise(ctx, blockForever(nil)) }()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("supervise error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not terminalize the run")
	}
	sig := sess.got()
	if len(sig) != 3 || sig[0] != syscall.SIGINT || sig[1] != syscall.SIGTERM || sig[2] != syscall.SIGKILL {
		t.Fatalf("signals = %v, want [INT TERM KILL] — cancellation must not orphan the execution", sig)
	}
}

func TestRunLease_EscalationReachesKillWhenTheExecutionIgnoresEverySignal(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: 0} // nothing but KILL ends it
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{WallClock: 50 * time.Millisecond, SignalGrace: 20 * time.Millisecond})

	err := lease.supervise(context.Background(), blockForever(nil))
	_ = leaseErrorOf(t, err)
	sig := sess.got()
	if len(sig) != 3 || sig[0] != syscall.SIGINT || sig[1] != syscall.SIGTERM || sig[2] != syscall.SIGKILL {
		t.Fatalf("signals = %v, want [INT TERM KILL] — a process that ignores both dies only on KILL", sig)
	}
}

func TestRunLease_InactivityFiresAndIsDistinctFromWallClock(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	// Wall-clock far longer than the test: only the inactivity bound can
	// explain the outcome.
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{WallClock: 30 * time.Second, Inactivity: 50 * time.Millisecond, SignalGrace: 50 * time.Millisecond})

	err := lease.supervise(context.Background(), func(ctx context.Context) error {
		lease.onAttemptStart()
		return blockForever(nil)(ctx)
	})
	le := leaseErrorOf(t, err)
	if le.Reason != content.TermInactivity {
		t.Fatalf("reason = %s, want inactivity — a silent execution is wedged, not slow", le.Reason)
	}
}

func TestRunLease_OutputBreaksTheSilence(t *testing.T) {
	// The inactivity clock must restart on output: a command that prints
	// continuously never fires the inactivity bound. Wall-clock is the
	// bound that ends THIS run; the assertion is that inactivity did not.
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, ring := newUnitLease(t, sess, RunLeaseConfig{WallClock: 120 * time.Millisecond, Inactivity: 30 * time.Millisecond, SignalGrace: 50 * time.Millisecond})

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lease.supervise(context.Background(), func(ctx context.Context) error {
			lease.onAttemptStart()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
					// The observer fires through the ring's write hook: the
					// lease counts output and re-arms the silence clock.
					if err := ring.write([]byte("x")); err != nil {
						return err
					}
				case <-stop:
					return nil
				}
			}
		})
	}()

	select {
	case err := <-done:
		le := leaseErrorOf(t, err)
		if le.Reason != content.TermTimeout {
			t.Fatalf("reason = %s, want timeout — continuous output broke the silence, so only the wall clock could fire", le.Reason)
		}
	case <-time.After(10 * time.Second):
		close(stop)
		t.Fatal("the lease never fired")
	}
}

func TestRunLease_OutputBudgetFiresAndNamesItself(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, ring := newUnitLease(t, sess, RunLeaseConfig{WallClock: 30 * time.Second, OutputBudget: 100, SignalGrace: 50 * time.Millisecond})

	ready := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- lease.supervise(context.Background(), func(ctx context.Context) error {
			close(ready)
			return blockForever(nil)(ctx)
		})
	}()
	<-ready
	lease.onAttemptStart()

	// The execution produces bytes through the ring: the observer hits the
	// budget and the lease terminalizes — the run is bounded, never
	// truncated silently. The writes retry so the observer's arming cannot
	// be raced (the lease arms it in its goroutine); the OUTCOME is the
	// observable, never a duration.
	var leaseErr error
	waittest.WaitForTimeout(t, "output budget to fire", 5*time.Second, func() bool {
		if err := ring.write(make([]byte, 100)); err != nil {
			t.Fatalf("ring write: %v", err)
		}
		select {
		case leaseErr = <-errCh:
			return true
		default:
			return false
		}
	})
	le := leaseErrorOf(t, leaseErr)
	if le.Reason != content.TermOutputBudget {
		t.Fatalf("reason = %s, want output-budget", le.Reason)
	}
}

func TestRunLease_OutputBudgetIgnoresPreStartCommandLine(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, ring := newUnitLease(t, sess, RunLeaseConfig{
		WallClock:    30 * time.Second,
		OutputBudget: 4,
		SignalGrace:  50 * time.Millisecond,
	})

	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lease.supervise(context.Background(), func(ctx context.Context) error {
			close(ready)
			return blockForever(nil)(ctx)
		})
	}()
	<-ready

	// The shell echoes this command before its authenticated start. The
	// command line is longer than the budget and must not consume it.
	if err := ring.write([]byte("echo a command line longer than budget")); err != nil {
		t.Fatalf("pre-start ring write: %v", err)
	}
	if output, started, reason := leaseAccounting(lease); output != 0 || started || reason != "" {
		t.Fatalf("pre-start accounting = output %d, started %v, reason %q; want untouched", output, started, reason)
	}

	lease.onAttemptStart()
	if output, started, reason := leaseAccounting(lease); output != 0 || !started || reason != "" {
		t.Fatalf("start accounting = output %d, started %v, reason %q; want zero and active", output, started, reason)
	}
	lease.mu.Lock()
	lastOutput := lease.lastOutput
	lease.mu.Unlock()
	if lastOutput.IsZero() {
		t.Fatal("authenticated start did not stamp last output")
	}
	if err := ring.write([]byte("ok")); err != nil {
		t.Fatalf("first command output: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("budget fired after only command output below the bound: %v", err)
	default:
	}
	if err := ring.write([]byte("!")); err != nil {
		t.Fatalf("second command output: %v", err)
	}
	if err := ring.write([]byte("?")); err != nil {
		t.Fatalf("third command output: %v", err)
	}
	le := leaseErrorOf(t, <-done)
	if le.Reason != content.TermOutputBudget {
		t.Fatalf("reason = %s, want output-budget", le.Reason)
	}
	if output, started, _ := leaseAccounting(lease); output != 4 || !started {
		t.Fatalf("final accounting = output %d, started %v; want 4 and active", output, started)
	}
}

func TestRunLease_OutputBurstImmediatelyAfterStartIsCountedOnce(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, ring := newUnitLease(t, sess, RunLeaseConfig{
		WallClock:    30 * time.Second,
		OutputBudget: 8,
		SignalGrace:  50 * time.Millisecond,
	})

	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lease.supervise(context.Background(), func(ctx context.Context) error {
			close(ready)
			return blockForever(nil)(ctx)
		})
	}()
	<-ready
	lease.onAttemptStart()

	// Two writes can race the lifecycle start in production. Once start has
	// authenticated, both writes belong to this attempt and count exactly once.
	if err := ring.write([]byte("1234")); err != nil {
		t.Fatalf("first output burst: %v", err)
	}
	// A repeated authenticated-start publication must not reset the first
	// burst before the second write.
	lease.onAttemptStart()
	if err := ring.write([]byte("5678")); err != nil {
		t.Fatalf("second output burst: %v", err)
	}
	le := leaseErrorOf(t, <-done)
	if le.Reason != content.TermOutputBudget {
		t.Fatalf("reason = %s, want output-budget", le.Reason)
	}
	if output, started, _ := leaseAccounting(lease); output != 8 || !started {
		t.Fatalf("burst accounting = output %d, started %v; want 8 and active", output, started)
	}
}

func leaseAccounting(l *runLease) (output int64, started bool, reason content.TerminationReason) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.output, l.accountingStarted, l.firedReason
}

func TestRunLease_AwaitingTakeoverSuspendsTheLease(t *testing.T) {
	// A TUI owns the lane: the human owns the execution and the product's
	// bounds no longer apply (ADR-0020 decision 3) — the run request stays
	// pending until the renderer resolves it, and no bound fires.
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lane := newLaneState()
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{WallClock: 50 * time.Millisecond, Inactivity: 50 * time.Millisecond})
	lease.lane = lane

	release := make(chan struct{})
	errCh := make(chan error, 1)
	go func() { errCh <- lease.supervise(context.Background(), blockForever(release)) }()

	// The transition happens (the renderer reported the alternate screen):
	// the lane callback suspends the lease synchronously. Whether the
	// callback catches the note or the note raced the watch registration,
	// the lease ends suspended — the pre-request state check covers the
	// race — so the observable is polled, never a duration.
	lane.note(session.ID("sid-unit"), "alternate")
	waittest.WaitForTimeout(t, "lease suspension on the awaiting-takeover transition", 5*time.Second, func() bool {
		return leaseSuspended(lease)
	})

	// The lease exposes no expiry event; this duration crosses both configured
	// 50 ms bounds, which is the negative behavior under test.
	time.Sleep(120 * time.Millisecond)
	select {
	case err := <-errCh:
		t.Fatalf("the lease fired while the lane awaited takeover: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("the suspended run must resolve with the renderer's answer, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the suspended run never resolved")
	}
	if sig := sess.got(); len(sig) != 0 {
		t.Fatalf("signals = %v, want none — a TUI under a human is never signaled by the lease", sig)
	}
}

// leaseSuspended reads the lease's suspension state (test access to the
// guarded field).
func leaseSuspended(l *runLease) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.suspended
}

func TestRunLease_ShortCommandIsUntouched(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: 0}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{WallClock: 30 * time.Second, Inactivity: 30 * time.Second, OutputBudget: 1 << 20})

	release := make(chan struct{})
	close(release) // the renderer resolves promptly
	err := lease.supervise(context.Background(), blockForever(release))
	if err != nil {
		t.Fatalf("a run that completes within its bounds must return the resolution, got %v", err)
	}
	if sig := sess.got(); len(sig) != 0 {
		t.Fatalf("signals = %v, want none — a completing run is never signaled", sig)
	}
}

func TestRunLease_FullyDisabledPassesThrough(t *testing.T) {
	sess := &fakeLeaseSession{}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{})
	want := errors.New("broker failure")
	err := lease.supervise(context.Background(), func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("a disabled lease must pass the broker result through untouched, got %v", err)
	}
	if sig := sess.got(); len(sig) != 0 {
		t.Fatalf("signals = %v, want none", sig)
	}
}

func TestRunLease_NoLocalProcessGroupStillTerminalizes(t *testing.T) {
	// A remote session has no local process group (SignalForeground →
	// ErrNoForeground): the run is still terminalized by its bound — the
	// kill is the gap the remote-footprint bead closes, not a reason to
	// let the run hang.
	remote := &errSession{err: pty.ErrNoForeground}
	lease, _ := newUnitLease(t, remote, RunLeaseConfig{WallClock: 50 * time.Millisecond})
	err := lease.supervise(context.Background(), blockForever(nil))
	le := leaseErrorOf(t, err)
	if le.Reason != content.TermTimeout {
		t.Fatalf("reason = %s, want timeout", le.Reason)
	}
}

func TestRunLease_CancelExecutionStopsBeforeReturning(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGINT}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{})
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lease.supervise(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started

	if got := lease.cancelExecution(); got != foregroundDelivered {
		t.Fatalf("cancel outcome = %q, want %q", got, foregroundDelivered)
	}
	if got := sess.got(); len(got) != 1 || got[0] != syscall.SIGINT {
		t.Fatalf("signals before cancel returned = %v, want [SIGINT]", got)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("supervise error = %v, want context canceled", err)
	}
}

func TestRunLease_CancelBeforeSuperviseEmitsNothingAndSignalsNothing(t *testing.T) {
	sess := &fakeLeaseSession{}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{})

	if got := lease.cancelExecution(); got != foregroundNothingRunning {
		t.Fatalf("pre-arm cancel outcome = %q, want %q", got, foregroundNothingRunning)
	}
	called := false
	err := lease.supervise(context.Background(), func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("supervise error = %v, want context canceled", err)
	}
	if called {
		t.Fatal("pre-cancelled lease delivered the broker request")
	}
	if got := sess.got(); len(got) != 0 {
		t.Fatalf("pre-cancelled lease signalled an unrelated foreground: %v", got)
	}
}

func TestRunLease_CancelExecutionAfterDisarmSignalsNothing(t *testing.T) {
	sess := &fakeLeaseSession{}
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{})
	if err := lease.supervise(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("supervise: %v", err)
	}

	if got := lease.cancelExecution(); got != foregroundNothingRunning {
		t.Fatalf("late cancel outcome = %q, want %q", got, foregroundNothingRunning)
	}
	if got := sess.got(); len(got) != 0 {
		t.Fatalf("late cancel signalled a later foreground: %v", got)
	}
}

func TestAgentRunControl_RejectsLeaseAttachedAfterCancellation(t *testing.T) {
	control := &agentRunControl{cancelDone: make(chan struct{})}
	if !control.beginCancel() {
		t.Fatal("fresh control refused cancellation")
	}
	lease, _ := newUnitLease(t, &fakeLeaseSession{}, RunLeaseConfig{})
	if control.attachRunLease(lease) {
		t.Fatal("lease attached after cancellation; RequestRun would emit a broker request")
	}
}

func TestAgentRunControl_CancelSignalsOnlyRegisteredAssistantLease(t *testing.T) {
	owned := &fakeLeaseSession{dieOn: syscall.SIGINT}
	userStarted := &fakeLeaseSession{}
	lease, _ := newUnitLease(t, owned, RunLeaseConfig{})
	lease.cancel = func() {}
	control := &agentRunControl{runLeases: map[*runLease]struct{}{lease: {}}}

	if control.cancelRunLeases() {
		t.Fatal("local assistant lease reported unsupported")
	}
	if got := owned.got(); len(got) != 1 || got[0] != syscall.SIGINT {
		t.Fatalf("owned signals = %v, want [SIGINT]", got)
	}
	if got := userStarted.got(); len(got) != 0 {
		t.Fatalf("unregistered user command was signalled: %v", got)
	}
}

func TestRunLease_CancelExecutionWithoutLocalProcessStillCancelsRequest(t *testing.T) {
	lease, _ := newUnitLease(t, &fakeLeaseSession{}, RunLeaseConfig{})
	lease.sess = nil
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lease.supervise(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started

	if got := lease.cancelExecution(); got != foregroundUnsupported {
		t.Fatalf("cancel outcome = %q, want %q", got, foregroundUnsupported)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("supervise error = %v, want context canceled", err)
	}
}

type errSession struct{ err error }

func (e *errSession) SignalForeground(syscall.Signal) error { return e.err }

func (e *errSession) ForegroundJob() (int, error) { return 0, e.err }

func (e *errSession) SignalProcessGroup(int, syscall.Signal) error { return e.err }

func TestRunLeaseError_NamesTheBoundAndCarriesTheReason(t *testing.T) {
	le := &assistant.RunLeaseError{Reason: content.TermInactivity, Err: context.Canceled}
	if !strings.Contains(le.Error(), "inactivity") {
		t.Fatalf("Error() = %q, want the bound named", le.Error())
	}
	if !errors.Is(le, context.Canceled) {
		t.Fatal("Unwrap must expose the underlying terminalization")
	}
	var got *assistant.RunLeaseError
	if !errors.As(le, &got) || got.Reason != content.TermInactivity {
		t.Fatal("errors.As must recover the reason")
	}
}

// THE ADDRESSEE IS NAMED BEFORE THE WITHDRAWAL (nocx-uvac6.11).
//
// cancelExecution used to withdraw the broker request and only then ask what
// was in front. Withdrawing is exactly what lets the request's command exit,
// so the answer could be the command the person started in its place — and the
// ladder went to that one.
//
// The naming now happens first, while the request still owns the foreground by
// construction. The withdrawal itself is the injection point here, because it
// is what ends the request: a cancel that hands the foreground to another group
// IS the race, deterministically.
func TestRunLease_CancelExecutionNamesItsOwnGroupBeforeWithdrawing(t *testing.T) {
	const owned, personStarted = 5100, 5200
	sess := newHandoffSession(owned)
	sess.diesOn = syscall.SIGINT
	lease, _ := newUnitLease(t, sess, RunLeaseConfig{})
	lease.cancel = func() { sess.takeOver(personStarted) }

	if got := lease.cancelExecution(); got != foregroundDelivered {
		t.Fatalf("cancel outcome = %q, want %q", got, foregroundDelivered)
	}
	if got := sess.signalsTo(owned); len(got) != 1 || got[0] != syscall.SIGINT {
		t.Fatalf("the request's own group got %v, want exactly [SIGINT]", got)
	}
	if got := sess.signalsTo(personStarted); len(got) != 0 {
		t.Fatalf("a command the person started under the withdrawal was signalled: %v", got)
	}
}
