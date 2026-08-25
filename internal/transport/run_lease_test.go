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
		log:  log.NewSlogAdapter(nil),
		sid:  session.ID("sid-unit"),
		sess: sess,
		ring: ring,
		cfg:  cfg,
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

	err := lease.supervise(context.Background(), blockForever(nil))
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

	errCh := make(chan error, 1)
	go func() { errCh <- lease.supervise(context.Background(), blockForever(nil)) }()

	// The execution produces bytes through the ring: the observer hits the
	// budget and the lease terminalizes — the run is bounded, never
	// truncated silently. The writes retry so the observer's arming cannot
	// be raced (the lease arms it in its goroutine); the OUTCOME is the
	// observable, never a duration.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := ring.write(make([]byte, 100)); err != nil {
			t.Fatalf("ring write: %v", err)
		}
		select {
		case err := <-errCh:
			le := leaseErrorOf(t, err)
			if le.Reason != content.TermOutputBudget {
				t.Fatalf("reason = %s, want output-budget", le.Reason)
			}
			return
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the budget never fired")
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
	deadline := time.Now().Add(5 * time.Second)
	for !leaseSuspended(lease) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !leaseSuspended(lease) {
		t.Fatal("the lease never suspended on the awaiting-takeover transition")
	}

	// Wait well past every bound and assert NOTHING fired, then let the
	// run resolve normally (the human finished; the renderer resolves).
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

type errSession struct{ err error }

func (e *errSession) SignalForeground(syscall.Signal) error { return e.err }

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
