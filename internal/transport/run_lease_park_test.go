package transport

// The quiet bound ASKS rather than kills (nocx-6dzxq), and the renewal it
// offers cannot outlive the person's ceiling.
//
// These tests drive the supervisor directly, the way run_lease_test.go does:
// the broker request is a fake that observes the lease's park channel
// exactly as Broker.RequestParkable does, so what is proven here is the
// supervisor's decision and not the wire.

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/waittest"
)

// newParkableLease is newUnitLease plus the park channel a real RequestRun
// hands to the broker. A lease with no park channel can never be asked
// about, which is the state every pre-nocx-6dzxq test was written in.
func newParkableLease(t *testing.T, sess runLeaseSession, cfg RunLeaseConfig) (*runLease, *outputRing) {
	t.Helper()
	lease, ring := newUnitLease(t, sess, cfg)
	lease.parkC = make(chan struct{})
	return lease, ring
}

// waitOnParkable stands in for Broker.RequestParkable: it waits for the
// resolution, the caller's context, or the park signal, and answers the park
// with ErrRequestParked exactly as the broker does.
func waitOnParkable(lease *runLease, resolved <-chan struct{}) func(context.Context) error {
	return func(ctx context.Context) error {
		lease.mu.Lock()
		park := lease.parkC
		lease.mu.Unlock()
		select {
		case <-resolved:
			return nil
		case <-park:
			return ErrRequestParked
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// THE REPORTED CASE, at the supervisor. `df` against a stuck mount prints
// nothing and used to be killed for it. The bound is reached and the command
// is NOT killed: no signal is sent, no termination reason is recorded, and
// the supervisor's answer is the park that carries the question to the
// model.
func TestRunLease_QuietBoundAsksTheModelInsteadOfKillingTheCommand(t *testing.T) {
	sess := &fakeLeaseSession{}
	lease, _ := newParkableLease(t, sess, RunLeaseConfig{
		WallClock: 30 * time.Second, Inactivity: 40 * time.Millisecond, SignalGrace: 20 * time.Millisecond,
	})

	err := lease.supervise(context.Background(), func(ctx context.Context) error {
		lease.onAttemptStart()
		return waitOnParkable(lease, nil)(ctx)
	})

	if !errors.Is(err, errRunParked) {
		t.Fatalf("supervise returned %v, want the park — silence must ask, not kill", err)
	}
	if got := sess.got(); len(got) != 0 {
		t.Fatalf("the quiet bound signalled %v; a command that is merely quiet must not be signalled at all", got)
	}
	lease.mu.Lock()
	reason, done, parked := lease.firedReason, lease.done, lease.parked
	lease.mu.Unlock()
	if reason != "" {
		t.Fatalf("firedReason = %q, want empty — nothing was terminalized", reason)
	}
	if done {
		t.Fatal("the lease was disarmed by the park; its wall clock must keep running while the model answers")
	}
	if !parked {
		t.Fatal("the lease does not report itself parked")
	}
}

// THE CEILING IS A CEILING. A model that answers "keep waiting" every single
// time still ends at the person's wall clock: the deadline was armed once,
// at the first submission, and no continuation re-arms it. The observable is
// the outcome — a timeout with the escalation run — not a duration.
func TestRunLease_KeepWaitingForeverStillEndsAtThePersonsCeiling(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, _ := newParkableLease(t, sess, RunLeaseConfig{
		WallClock: 250 * time.Millisecond, Inactivity: 20 * time.Millisecond, SignalGrace: 20 * time.Millisecond,
	})

	err := lease.supervise(context.Background(), func(ctx context.Context) error {
		lease.onAttemptStart()
		return waitOnParkable(lease, nil)(ctx)
	})
	renewals := 0
	for errors.Is(err, errRunParked) {
		// The model's answer: keep waiting, every time, for as long as it is
		// asked. Nothing in this loop touches the wall clock.
		// The parked ending, exactly as the parked-run registry installs it:
		// a bound that fires while nobody is waiting must escalate itself.
		lease.setParkedEnd(func(content.TerminationReason) foregroundOutcome {
			return lease.escalate()
		})
		parkC := make(chan struct{})
		lease.resumeQuiet(20*time.Millisecond, parkC)
		renewals++
		if renewals > 200 {
			t.Fatal("the renewals never met the ceiling: the wall clock is being re-armed somewhere")
		}
		err = lease.superviseResume(context.Background(), waitOnParkable(lease, nil))
	}

	le := leaseErrorOf(t, err)
	if le.Reason != content.TermTimeout {
		t.Fatalf("reason = %s, want timeout — the ceiling is what ends an endlessly renewed run", le.Reason)
	}
	if renewals < 2 {
		t.Fatalf("renewals = %d, want several: the test proves the ceiling holds ACROSS renewals", renewals)
	}
	// WAIT FOR THE LADDER, DO NOT ASSUME IT. The ceiling here can fire on
	// either of two goroutines — the caller's, when superviseResume observes
	// its own cancelled context and escalates inline, or a timer's, when the
	// bound beats the continuation to the lease and the parked ending runs
	// instead. In the second case superviseResume returns the verdict the
	// moment takeBack refuses, which can be before that goroutine has
	// finished escalating. "The escalation has run" is an observable state,
	// so it is waited for as one rather than inferred from having been
	// returned to.
	var signals []syscall.Signal
	waittest.WaitForTimeout(t, "the escalation to reach the execution", 10*time.Second, func() bool {
		signals = sess.got()
		return len(signals) > 0
	})
	if signals[0] != syscall.SIGINT {
		t.Fatalf("signals = %v, want the escalation to have started at INT", signals)
	}
	if lease.renewalCount() != renewals {
		t.Fatalf("lease renewals = %d, want %d — the count the model is shown must be the real one", lease.renewalCount(), renewals)
	}
}

// A resumption re-opens the QUIET interval and nothing else. If it re-armed
// the ceiling, the remaining wall clock after a park would be back at the
// full configured value.
func TestRunLease_ResumingDoesNotRearmTheCeiling(t *testing.T) {
	lease, _ := newParkableLease(t, &fakeLeaseSession{}, RunLeaseConfig{
		WallClock: 10 * time.Second, Inactivity: 30 * time.Millisecond, SignalGrace: 20 * time.Millisecond,
	})
	err := lease.supervise(context.Background(), func(ctx context.Context) error {
		lease.onAttemptStart()
		return waitOnParkable(lease, nil)(ctx)
	})
	if !errors.Is(err, errRunParked) {
		t.Fatalf("supervise returned %v, want the park", err)
	}
	before := lease.remainingWallClock()

	resolved := make(chan struct{})
	close(resolved)
	parkC := make(chan struct{})
	lease.resumeQuiet(10*time.Second, parkC)
	if err := lease.superviseResume(context.Background(), waitOnParkable(lease, resolved)); err != nil {
		t.Fatalf("the resumed wait failed: %v", err)
	}
	after := lease.remainingWallClock()
	if after > before {
		t.Fatalf("remaining ceiling grew across the renewal: %v -> %v", before, after)
	}
}

// A command that resolves normally after the model chose to keep waiting is
// the reported case's happy ending: the SAME execution answers, and the
// supervisor reports no bound at all.
func TestRunLease_KeepWaitingThenTheCommandCompletes(t *testing.T) {
	sess := &fakeLeaseSession{}
	lease, _ := newParkableLease(t, sess, RunLeaseConfig{
		WallClock: 30 * time.Second, Inactivity: 30 * time.Millisecond, SignalGrace: 20 * time.Millisecond,
	})
	err := lease.supervise(context.Background(), func(ctx context.Context) error {
		lease.onAttemptStart()
		return waitOnParkable(lease, nil)(ctx)
	})
	if !errors.Is(err, errRunParked) {
		t.Fatalf("supervise returned %v, want the park", err)
	}

	resolved := make(chan struct{})
	close(resolved)
	parkC := make(chan struct{})
	lease.resumeQuiet(30*time.Second, parkC)
	if err := lease.superviseResume(context.Background(), waitOnParkable(lease, resolved)); err != nil {
		t.Fatalf("the command completed but the lease reported %v", err)
	}
	if got := sess.got(); len(got) != 0 {
		t.Fatalf("a command that completed on its own was signalled: %v", got)
	}
}

// ── the clamp, both ends (AGENTS.md rule 3) ───────────────────────────────

func TestRunLeaseConfig_AnAskBelowThePersonsBoundIsHonoured(t *testing.T) {
	cfg := RunLeaseConfig{WallClock: 10 * time.Minute, Inactivity: 10 * time.Minute}
	got, clamped := cfg.withAskedQuiet(90 * time.Second)
	if got.Inactivity != 90*time.Second {
		t.Fatalf("quiet bound = %v, want the 90s this call asked for", got.Inactivity)
	}
	if clamped {
		t.Fatal("an ask below the person's bound was reported as clamped")
	}
	if got.WallClock != 10*time.Minute {
		t.Fatalf("wall clock = %v, want it untouched — a call may not move the ceiling", got.WallClock)
	}
}

func TestRunLeaseConfig_AnAskAboveThePersonsBoundIsClampedAndSaidSo(t *testing.T) {
	cfg := RunLeaseConfig{WallClock: 10 * time.Minute, Inactivity: 10 * time.Minute}
	got, clamped := cfg.withAskedQuiet(45 * time.Minute)
	if got.Inactivity != 10*time.Minute {
		t.Fatalf("quiet bound = %v, want the person's 10m", got.Inactivity)
	}
	if !clamped {
		t.Fatal("the clamp was silent; a bound the model believes in and nobody enforces is the defect")
	}
	if from := clampedFrom(clamped, 45*time.Minute); from != 45*time.Minute {
		t.Fatalf("clampedFrom = %v, want the 45m that was asked for — the result must say what it was cut from", from)
	}
}

func TestRunLeaseConfig_NoAskLeavesThePersonsBoundExactly(t *testing.T) {
	cfg := RunLeaseConfig{WallClock: 7 * time.Minute, Inactivity: 3 * time.Minute}
	got, clamped := cfg.withAskedQuiet(0)
	if got.Inactivity != 3*time.Minute || got.WallClock != 7*time.Minute {
		t.Fatalf("bounds = %v/%v, want the person's 3m/7m unchanged", got.Inactivity, got.WallClock)
	}
	if clamped {
		t.Fatal("a call that asked for nothing was reported as clamped")
	}
	if from := clampedFrom(clamped, 0); from != 0 {
		t.Fatalf("clampedFrom = %v, want zero", from)
	}
}

// ── the settings→lease path ───────────────────────────────────────────────

// A lease is bound by the numbers on the person's screen, read when the run
// starts. Changing one changes what the NEXT run is bound by; the config
// snapshotted into a running lease is never re-read, which is what keeps a
// bound from moving under a command already in flight.
func TestEffectiveRunLease_TakesBothCeilingsFromTheSettingsTheNextRunReads(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	s := &WSServer{log: log.NewSlogAdapter(nil), settings: reg}

	// The declared defaults, which are what a person who has changed
	// nothing runs under.
	cfg := s.effectiveRunLease()
	if cfg.WallClock != 10*time.Minute {
		t.Fatalf("default wall clock = %v, want 10m", cfg.WallClock)
	}
	if cfg.Inactivity != 10*time.Minute {
		t.Fatalf("default quiet bound = %v, want 10m", cfg.Inactivity)
	}

	if err := reg.SetNumber(settings.AgentRunWallClockMinutes, 30); err != nil {
		t.Fatalf("SetNumber(wall clock): %v", err)
	}
	if err := reg.SetNumber(settings.AgentRunQuietMinutes, 2); err != nil {
		t.Fatalf("SetNumber(quiet): %v", err)
	}
	next := s.effectiveRunLease()
	if next.WallClock != 30*time.Minute {
		t.Fatalf("wall clock after the change = %v, want 30m — the next run must see the new number", next.WallClock)
	}
	if next.Inactivity != 2*time.Minute {
		t.Fatalf("quiet bound after the change = %v, want 2m", next.Inactivity)
	}
	// And the lease already built from the earlier read is untouched: its
	// config is a snapshot, so no bound moves under a running command.
	if cfg.WallClock != 10*time.Minute || cfg.Inactivity != 10*time.Minute {
		t.Fatalf("the earlier snapshot changed to %v/%v; a running command's bounds must not move", cfg.WallClock, cfg.Inactivity)
	}
}

// With no settings registry at all (a unit test, cmd/devharness) the lease
// still has the person's declared defaults rather than a second copy of the
// numbers.
func TestDefaultRunLease_ReadsTheDeclaredDefaultsRatherThanRestatingThem(t *testing.T) {
	if defaultRunLease.WallClock != settingMinutes(settings.AgentRunWallClockMinutes.DefaultValue()) {
		t.Fatalf("default wall clock = %v, want the declared %v",
			defaultRunLease.WallClock, settings.AgentRunWallClockMinutes.DefaultValue())
	}
	if defaultRunLease.Inactivity != settingMinutes(settings.AgentRunQuietMinutes.DefaultValue()) {
		t.Fatalf("default quiet bound = %v, want the declared %v",
			defaultRunLease.Inactivity, settings.AgentRunQuietMinutes.DefaultValue())
	}
}

// ── the parked registry ───────────────────────────────────────────────────

// A parked run that nobody answers about is still bounded: the wall clock is
// still armed, and when it fires the parked ending withdraws the request and
// runs the ladder. This is the "the model never called back" case.
func TestParkedRun_TheCeilingStillEndsARunNobodyCameBackFor(t *testing.T) {
	sess := &fakeLeaseSession{dieOn: syscall.SIGTERM}
	lease, _ := newParkableLease(t, sess, RunLeaseConfig{
		WallClock: 120 * time.Millisecond, Inactivity: 20 * time.Millisecond, SignalGrace: 20 * time.Millisecond,
	})
	lease.sid = session.ID("sid-park")

	err := lease.supervise(context.Background(), func(ctx context.Context) error {
		lease.onAttemptStart()
		return waitOnParkable(lease, nil)(ctx)
	})
	if !errors.Is(err, errRunParked) {
		t.Fatalf("supervise returned %v, want the park", err)
	}

	// THE SIGNAL IS SENT AFTER THE LADDER, NOT BEFORE IT. Receiving from
	// `ended` has to mean "the parked ending has finished", because that is
	// the fact the assertion below reads. Sending first made the receive
	// mean only "the callback started" — the checker then raced the very
	// escalation it was checking for, and won on an idle machine and lost on
	// a loaded CI runner. No timing here was ever wrong; the order was.
	ended := make(chan content.TerminationReason, 1)
	lease.setParkedEnd(func(reason content.TerminationReason) foregroundOutcome {
		outcome := lease.escalate()
		ended <- reason
		return outcome
	})

	select {
	case reason := <-ended:
		if reason != content.TermTimeout {
			t.Fatalf("the parked ending fired with %s, want timeout", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wall clock never fired on a parked run: a parked command would run forever")
	}
	// Safe to read directly now: the send above is the escalation's own
	// completion, so the ladder has run before this line can be reached.
	if got := sess.got(); len(got) == 0 {
		t.Fatal("the parked ending did not escalate; a run that outlived its ceiling must be killed, not abandoned")
	}
}

// The sentence the model receives names the bound that fired, its value and
// the continuation. Asserted on the STRING: the model acts on words, not on
// a reason code.
func TestRunStillRunningSentence_NamesTheBoundTheCeilingAndTheContinuation(t *testing.T) {
	got := assistant.RunStillRunningSentence("session.run", &assistant.RunStillRunningError{
		RunID:     "abc123",
		Quiet:     10 * time.Minute,
		Remaining: 4 * time.Minute,
	})
	for _, want := range []string{
		"STILL RUNNING",
		"printed nothing for 10 minutes",
		"has NOT been stopped",
		"session.wait",
		"abc123",
		"continue",
		"stop",
		"4 minutes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the sentence does not say %q:\n%s", want, got)
		}
	}
}
