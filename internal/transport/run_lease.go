package transport

// The run lease (ADR-0020 decision 2): every execution runs under a
// wall-clock deadline (bounded ceiling), a separate inactivity deadline —
// silence is a different failure from slowness, and a single timeout cannot
// tell them apart — an output budget, and cancellation that escalates
// INT → TERM → KILL against the execution's OWN process group, so
// cancellation reaches the command's children rather than only the shell.
//
// THE ESCALATION LADDER FOR A QUIET COMMAND HAS EXACTLY TWO RUNGS, and only
// the first of them is built here (nocx-6dzxq):
//
//	the quiet bound is reached  ->  NOCX ASKS THE MODEL
//	                            ->  the model, at its own discretion, may
//	                                ask the person
//
// The second rung is NOT built here because it already exists: the model can
// answer the turn in words, or propose a call that raises an ordinary
// approval. A path from this file to a human would be a second owner for
// "how a person is asked about a run" (AD-8) and would wake somebody for the
// exact question the model was handed so that they would not be woken. If
// you came here looking for the missing human path: it is missing on
// purpose. Do not add one.
//
// AND THIS IS NOT decision 3's awaiting-takeover, however similar the two
// look from a distance. There, a program has activated the alternate screen
// or blocked on stdin: it WANTS INPUT, only a person can supply it, and the
// lease SUSPENDS so an interactive program is not killed for being
// interactive. Here nobody wants input at all — a command is merely quiet,
// and what is wanted is a JUDGEMENT about whether the silence is expected.
// The model has the command and the task in front of it and is the party
// that can make that judgement, so the model is who is asked. Same lease,
// different answerer, different reason; neither is a special case of the
// other.
//
// The quiet bound therefore does NOT terminalize. It PARKS: the broker
// request stays armed, the renderer is never told to cancel, the command
// keeps running, and the tool call returns to the model with the question
// and the handle to continue with (ws_run.go's RequestRunWait). The wall
// clock is the only bound that still kills, and it keeps running across
// every renewal — see the ceiling note on defaultRunLease.
//
// The lease lives in the backend even though the command it bounds was
// submitted by the renderer (design §2.2): signals, deadlines and teardown
// are things the product does on a person's behalf — a human does not send
// SIGTERM with the keyboard either — and the split is the one this bead
// must not re-litigate. The one fact the backend cannot own is the
// alternate screen (AD-6), so interactivity travels up from the renderer
// and the awaiting-takeover transition is decided here, in Go, from it; a
// lane that enters awaiting-takeover suspends the lease — killing an
// interactive program is exactly what decision 3 refuses to do.
//
// The lease observes three facts it owns: the wall clock, the session's
// output (through the session's replay ring — bytes counted, never sniffed,
// AD-6), and the lane's transition (laneState). When a bound fires it
// cancels the broker request FIRST — a late resolution (the block froze
// because the kill worked) must not win the race and report the run
// completed, or the ledger could not say which bound ended it — then
// escalates the signals, and returns a RunLeaseError whose Reason the
// attempt ledger records.
//
// Goroutine discipline (ADR-0026 enforcement): the lease spawns NOTHING.
// The broker request runs inline on the caller's goroutine — the ask
// stream's task, admission-backed and shutdown-accounted — and its own
// select already waits on the resolution, the kind's timeout and ctx.Done.
// The lease's bounds are triggers that CANCEL that context from goroutines
// the control plane already owns: runtime-managed timers (time.AfterFunc —
// the same class as the broker's own time.NewTimer), the session pump's
// ring hook, and a lane-state callback fired by the reporting read-loop
// handler. Terminalization and the escalation run synchronously on the
// caller's goroutine after the request returns.

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
)

// RunLeaseConfig bounds one run execution. Zero disables the corresponding
// bound; all zero disables the lease entirely (the broker's pre-lease
// timeout applies). The WSServer's defaults are defaultRunLease.
//
// WallClock is independent of shell integration and arms in supervise.
// Inactivity and OutputBudget require an authenticated lifecycle attempt,
// which only shell integration can open; RequestRun refuses a run rather than
// silently advertising either bound when that integration is unavailable.
type RunLeaseConfig struct {
	// WallClock bounds the whole execution, from the run request to the
	// resolution — the bounded ceiling of decision 2. It is the ONLY bound
	// when the renderer never answers at all.
	WallClock time.Duration
	// Inactivity bounds the silence: no output for this long PARKS the
	// execution and asks the model whether to keep waiting. It does not end
	// it — see the ladder in this file's header. Independent of WallClock so
	// a command can be slow AND alive; the timer restarts on every byte,
	// which is what makes silence a different question from duration.
	Inactivity time.Duration
	// OutputBudget bounds what one execution produces, in bytes of
	// terminal output. When it fires the run is terminalized with a reason
	// the block names — bounded visibly, never truncated silently.
	OutputBudget int64
	// SignalGrace is how long the escalation waits after each signal for
	// the execution to cooperate before escalating. Zero means the default
	// (defaultRunSignalGrace).
	SignalGrace time.Duration
}

// needsShellIntegration is the single declaration of which lease bounds
// depend on the authenticated lifecycle attempt. WallClock does not: it is
// armed in supervise before delivery. The output and inactivity observers do.
func (cfg RunLeaseConfig) needsShellIntegration() bool {
	return cfg.Inactivity > 0 || cfg.OutputBudget > 0
}

// DefaultRunLeaseConfig is the production lease's non-settings half, named
// at the composition root the way the lane capacity is: the output budget
// and the escalation grace are product decisions with no control on any
// screen. The two TIME bounds it carries are only fallbacks — the person
// owns them (settings.AgentRunWallClockMinutes and
// settings.AgentRunQuietMinutes), and effectiveRunLease reads them on every
// run. The comment that used to stand here predicted "a future settings
// surface flip one bound in one place"; that surface exists now, and this is
// no longer where either number is decided.
func DefaultRunLeaseConfig() RunLeaseConfig { return defaultRunLease }

// defaultRunLease is the product's lease when NO settings registry is wired
// (a unit test, cmd/devharness). Both time bounds are read back from the
// settings declarations that own them rather than restated, so the number a
// person sees on the screen and the number a registry-less backend uses can
// never drift apart. effectiveRunLease reads the person's current values on
// every run.
//
// A CONSEQUENCE OF 10/10 WORTH KNOWING BEFORE YOU DELETE SOMETHING. The
// quiet timer restarts on every byte of output; the wall clock restarts on
// nothing. With both ceilings at ten minutes the quiet bound therefore
// cannot fire BEFORE the wall clock: a command that has been silent for ten
// minutes has, by construction, also been running for ten. That is not a
// bug and the quiet bound is not dead code — it is exactly the fix the
// reported case needed (a healthy `df` against a stuck mount was being
// killed after two minutes of normal silence), and it becomes load-bearing
// again the moment somebody raises "Stop an assistant's command after" above
// "Ask about a silent command after", which is what those two settings are
// for. The ordering is the person's to choose; nothing here enforces one.
var defaultRunLease = RunLeaseConfig{
	WallClock:    settingMinutes(settings.AgentRunWallClockMinutes.DefaultValue()),
	Inactivity:   settingMinutes(settings.AgentRunQuietMinutes.DefaultValue()),
	OutputBudget: 4 << 20, // 4 MiB
	SignalGrace:  time.Second,
}

// settingMinutes converts a settings number declared in minutes into a
// duration. One conversion, so the unit stated on the screen and the unit
// the lease is armed with are the same statement.
func settingMinutes(v float64) time.Duration {
	if v <= 0 {
		return 0
	}
	return time.Duration(v * float64(time.Minute))
}

// withAskedQuiet applies the model's own quiet bound to this run's config.
// ADR-0047's spirit binds here: a program may ASK, it never CHOOSES. A call
// asking for LESS silence than the person allows gets exactly what it asked
// for; a call asking for more is clamped to the person's number and the tool
// result says so — a silent clamp would be a bound the model believes in and
// nobody enforces. Zero means the call asked for nothing and the person's
// number applies unchanged.
//
// THIS IS THE ONLY PLACE A MODEL-SUPPLIED BOUND MEETS THE PERSON'S. The wall
// clock has no asked form at all: it is the ceiling, and a renewal that could
// move it would not be a renewal, it would be the removal of the bound.
func (cfg RunLeaseConfig) withAskedQuiet(asked time.Duration) (RunLeaseConfig, bool) {
	if asked <= 0 || cfg.Inactivity <= 0 {
		return cfg, false
	}
	if asked >= cfg.Inactivity {
		return cfg, asked > cfg.Inactivity
	}
	cfg.Inactivity = asked
	return cfg, false
}

// defaultRunSignalGrace is the escalation's per-signal patience when the
// config leaves SignalGrace zero.
const defaultRunSignalGrace = time.Second

// runLeaseSession is what the lease needs from the session it supervises:
// the ability to signal the execution's process group. A local session
// implements it through the pty (which owns the master and discovers the
// foreground group via TIOCGPGRP); a remote session and the stub report
// pty.ErrNoForeground — the escalation is a no-op there and the run is
// terminalized without a kill, which is the honest answer for a host this
// process cannot signal (the remote kill is the remote-footprint bead's
// job, not this one's).
type runLeaseSession interface {
	// SignalForeground is the ONE-SHOT form: signal whatever job is in front
	// right now. It is what an interrupt means and it is all an interrupt
	// needs.
	SignalForeground(sig syscall.Signal) error
	// ForegroundJob names that job's process group, and SignalProcessGroup
	// signals it. An ESCALATION is built out of these two rather than out of
	// repeated SignalForeground calls: the ladder must keep the addressee it
	// started against, or a job that exits between two rungs hands the next
	// rung to whatever the person started in its place (nocx-uvac6.11).
	ForegroundJob() (int, error)
	SignalProcessGroup(pgid int, sig syscall.Signal) error
}

// runLease supervises one run request. It is created per RequestRun and is
// not safe for concurrent use: the OBSERVERS (the ring hook, the timers'
// goroutines, the lane callback) call its methods concurrently, but the
// verdict — firedReason — is read only after every observer is disarmed
// (disarm before the read), so a callback can never be mid-flight when the
// caller decides. State read by observers is guarded by mu.
type runLease struct {
	log       log.Logger
	sid       session.ID
	sess      runLeaseSession // nil → no local process group to signal
	ring      *outputRing     // nil → no output observation (wall-clock only)
	lane      *laneState      // nil → no awaiting-takeover observation
	protected protectedForeground
	cfg       RunLeaseConfig
	// submissionDelivered is the broker-owned fact that the run request
	// reached at least one renderer. It is distinct from accountingStarted:
	// the latter is an authenticated lifecycle observation unavailable on
	// conventional shells. The lifecycle attempt bridge is separate and is
	// used only to name the exact ledger attempt.
	submissionDelivered bool

	// parkC is closed exactly once, by the quiet bound, to tell the broker
	// request to STOP WAITING WITHOUT WITHDRAWING. It is a channel and not a
	// context cancellation on purpose: cancelling the request's context is
	// what makes the broker send agent.runCancel and drop the pending id,
	// which is precisely the kill this bound no longer performs.
	parkC chan struct{}

	mu                sync.Mutex
	cancel            context.CancelFunc
	wallTimer         *time.Timer
	wallDeadline      time.Time
	inactTimer        *time.Timer
	unwatch           func()
	lastOutput        time.Time
	output            int64
	accountingStarted bool
	firedReason       content.TerminationReason
	done              bool
	suspended         bool
	cancelStarted     bool
	// parked is set by the quiet bound and never cleared: it is a fact about
	// THIS wait, and a resumed wait is a new one. renewals counts how many
	// times the model answered "keep waiting" on this execution — carried on
	// the lease because the lease is what the run's bounds belong to, and
	// reported to the model so it can see it is in a loop.
	parked   bool
	renewals int
	// parkedEnd is how a PARKED lease ends. While a caller is waiting on the
	// broker request, cancelling its context is enough: the caller returns
	// and escalates on its own goroutine. A parked run has no such caller,
	// so whatever ends it must withdraw the request AND run the escalation
	// itself, and this is that one function. Installed by the parked-run
	// registry at park, cleared when a continuation takes the run back.
	parkedEnd func(reason content.TerminationReason) foregroundOutcome
}

// errRunParked is supervise's answer when the quiet bound asked the model
// instead of killing: not a failure and not a terminalization — the command
// is still running, and the caller must park the broker request rather than
// withdraw it. It never leaves the transport.
var errRunParked = errors.New("run lease: the quiet bound parked the run")

// setParkedEnd installs (or clears, with nil) the parked termination path.
func (l *runLease) setParkedEnd(end func(reason content.TerminationReason) foregroundOutcome) {
	l.mu.Lock()
	l.parkedEnd = end
	l.mu.Unlock()
}

// takeBack moves a parked lease back under a caller that is waiting again:
// the parked termination path is cleared and the new caller's cancellation
// installed in ONE critical section, so a bound firing in between can never
// find neither of them. It refuses when the run has already ended — a bound
// fired, the person cancelled the turn — which is what the continuation
// reports instead of waiting on an execution that is gone.
func (l *runLease) takeBack(cancel context.CancelFunc) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done || l.firedReason != "" || l.cancelStarted {
		return false
	}
	l.parkedEnd = nil
	l.cancel = cancel
	return true
}

// remainingWallClock is what is left of the person's ceiling. Reported to
// the model with the quiet bound's question so "keep waiting" is a decision
// against a number rather than a reflex. Zero when no wall clock is armed.
func (l *runLease) remainingWallClock() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.wallDeadline.IsZero() {
		return 0
	}
	if d := time.Until(l.wallDeadline); d > 0 {
		return d
	}
	return 0
}

// isParked reports whether the quiet bound asked the model about this run
// AND no verdict has been reached since. The second half is what makes it
// safe to read as "keep this lease alive": a wall clock that fired while the
// model was being asked is a verdict, and a verdict always wins over a
// question — the ceiling ends the run whatever anybody was about to answer.
func (l *runLease) isParked() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.parked && l.firedReason == ""
}

// supervise runs fn (the broker request) under the lease, INLINE on the
// caller's goroutine. It returns fn's result when the run completes within
// its bounds, or a RunLeaseError naming the bound that fired. For a delivered
// request the escalation has run to completion before return, so the caller
// can assert the execution is actually dead, not merely scheduled to die.
//
// A transport disconnect is deliberately treated like abandonment, not like
// a reconnectable pause: the renderer that owns this request is gone and
// leaving its command alive would recreate the orphan this lease prevents.
// AD-9 preserves the session, replay ring and ledger for reconnect, so the
// next connection reads the terminalized run rather than inheriting a live
// process with no answerer. The lease only escalates after broker delivery
// establishes that a renderer could submit the command; before that point
// there is no execution for it to signal. The exact foreground group is still
// captured by protectedForeground, so no later command can be signalled.
func (l *runLease) supervise(ctx context.Context, fn func(ctx context.Context) error) error {
	cfg := l.cfg
	if cfg.SignalGrace <= 0 {
		cfg.SignalGrace = defaultRunSignalGrace
	}
	enabled := cfg.WallClock > 0 || cfg.Inactivity > 0 || cfg.OutputBudget > 0
	ctx, cancel := context.WithCancel(ctx)
	// A PARKED LEASE IS NOT TORN DOWN. Its wall timer is the person's
	// ceiling, armed once at the first submission and never re-armed, so it
	// keeps running while the model is being asked and across every renewal
	// — which is what makes "renewals stop at the person's number" true by
	// construction rather than by a counter somebody has to remember to
	// check. The caller (RequestRun) hands the still-armed lease to the
	// parked-run registry and replaces its cancellation handle there.
	defer func() {
		if l.isParked() {
			return
		}
		cancel()
		l.disarm()
	}()

	// The cancellation handle is armed before broker delivery even when the
	// ordinary lease bounds are disabled. agent.cancel owns this exact request,
	// so it must be able to withdraw it without restoring the old broad
	// foreground-session signal path.
	l.mu.Lock()
	l.cancel = cancel
	if l.cancelStarted {
		l.mu.Unlock()
		cancel()
		return context.Canceled
	}
	if enabled {
		if cfg.WallClock > 0 {
			// ARMED ONCE, HERE, AND NEVER AGAIN. Every continuation of this
			// run re-opens only the quiet interval; this deadline is the
			// person's ceiling and it keeps counting through every park and
			// every renewal. That is what makes the ceiling a ceiling
			// rather than a budget somebody has to remember to decrement.
			l.wallDeadline = time.Now().Add(cfg.WallClock)
			l.wallTimer = time.AfterFunc(cfg.WallClock, func() { l.fire(content.TermTimeout) })
		}
		if l.lane != nil {
			l.unwatch = l.lane.watch(l.sid, l.onLane)
		}
	}
	l.mu.Unlock()
	if enabled && l.ring != nil {
		l.ring.setWriteObserver(func(n int) { l.onOutput(n) })
	}

	// A lane already awaiting takeover (the report raced the run start)
	// suspends the bounds from the first byte. Exact cancellation remains
	// armed: it belongs to this assistant request, not to lane ownership.
	if enabled && l.lane != nil && l.lane.awaitingTakeover(l.sid) {
		l.mu.Lock()
		l.suspended = true
		l.mu.Unlock()
	}

	err := fn(ctx)

	// The quiet bound's outcome is read FIRST and returns without disarming:
	// nothing was terminalized, nothing is escalated, and the command is
	// still running. errRunParked is the caller's signal to park the broker
	// request and ask the model.
	if l.isParked() {
		return errRunParked
	}

	// Disarm before the verdict: every trigger is stopped or neutered, so
	l.disarm()
	l.mu.Lock()
	reason := l.firedReason
	cancelStarted := l.cancelStarted
	submitted := l.submissionDelivered
	l.mu.Unlock()
	if reason != "" {
		leaseErr := &assistant.RunLeaseError{
			Reason:            reason,
			Err:               err,
			SubmissionExpired: !submitted,
		}
		if submitted {
			l.escalate()
		}
		return leaseErr
	}
	// A caller cancellation can end the broker request without a lease
	// observer firing. A renderer disappearing is the same abandonment under
	// the broker's explicit ErrRequestDisconnected/ErrRequestUndelivered
	// outcomes, even when that error wins the race with ctx.Done. The request
	// still owns the execution until this return, so synchronously run the
	// same ladder unless cancelExecution already did.
	if err != nil && !cancelStarted &&
		(errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, ErrRequestDisconnected) ||
			errors.Is(err, ErrRequestUndelivered)) {
		l.escalate()
	}
	return err
}

// superviseResume supervises a CONTINUATION of an execution that is already
// running: the model answered "keep waiting", so a new caller is waiting on
// the same broker request under the same lease.
//
// It is deliberately not supervise. supervise ARMS the bounds — the wall
// clock above all — and arming them again is precisely the bug this whole
// arrangement exists to avoid: a re-armed ceiling is a ceiling the model can
// push away by asking. So this re-points only the cancellation handle at the
// new caller's context (a bound firing must reach the goroutine that is
// actually waiting) and leaves every timer exactly as it was.
func (l *runLease) superviseResume(ctx context.Context, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	if !l.takeBack(cancel) {
		cancel()
		l.mu.Lock()
		reason := l.firedReason
		l.mu.Unlock()
		if reason != "" {
			// A bound fired in the instant this continuation was taking the
			// run back. The parked ending already withdrew and escalated;
			// this reports what actually happened rather than a cancellation
			// nobody caused.
			return &assistant.RunLeaseError{Reason: reason, Err: context.Canceled}
		}
		return context.Canceled
	}
	defer func() {
		if l.isParked() {
			return
		}
		cancel()
		l.disarm()
	}()

	err := fn(ctx)

	if l.isParked() {
		return errRunParked
	}
	l.disarm()
	l.mu.Lock()
	reason := l.firedReason
	l.mu.Unlock()
	if reason != "" {
		leaseErr := &assistant.RunLeaseError{Reason: reason, Err: err}
		// The submission was delivered long ago — this execution has been
		// running since before the first park — so the escalation always
		// applies here. There is no SubmissionExpired case to consider.
		l.escalate()
		return leaseErr
	}
	if err != nil && (errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrRequestDisconnected) ||
		errors.Is(err, ErrRequestUndelivered)) {
		l.escalate()
	}
	return err
}

// fire records the bound that ended the run and cancels the broker
// request's context. It runs on an observer's goroutine (a timer's, the
// pump's via the ring hook, the read loop's via the lane callback) and must
// not block: idempotent, first fire wins. The broker's own select observes
// the cancellation, drops the pending request id, and returns — a late
// resolution is answered "Unknown request id" (ADR-0026 item 16).
func (l *runLease) fire(reason content.TerminationReason) {
	l.mu.Lock()
	if l.done || l.firedReason != "" || l.suspended {
		l.mu.Unlock()
		return
	}
	l.firedReason = reason
	cancel := l.cancel
	parkedEnd := l.parkedEnd
	l.mu.Unlock()
	// A PARKED run has nobody waiting on the broker request, so cancelling
	// its context would reach no select and the command would outlive its
	// ceiling. parkedEnd withdraws and escalates instead, inline on this
	// timer's own goroutine — the same runtime-managed goroutine class the
	// lease already runs its bounds on, and still nothing this file spawned
	// (ADR-0026).
	if parkedEnd != nil {
		parkedEnd(reason)
		return
	}
	if cancel != nil {
		cancel()
	}
}

// onAttemptStart begins output and inactivity accounting at the authenticated
// lifecycle start. The ring lock is taken first so a write racing this
// callback is ordered: bytes completed before the start are ignored, while
// bytes written after it are counted exactly once.
//
// The observer is deliberately installed by supervise, before broker
// delivery. This method only opens the accounting interval; it never changes
// the observer under l.mu, preserving ring.mu → l.mu.
func (l *runLease) onAttemptStart() {
	if l.ring != nil {
		l.ring.mu.Lock()
		defer l.ring.mu.Unlock()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done || l.suspended || l.firedReason != "" || l.accountingStarted {
		return
	}
	l.accountingStarted = true
	l.output = 0
	l.lastOutput = time.Now()
	if l.cfg.Inactivity > 0 {
		l.inactTimer = time.AfterFunc(l.cfg.Inactivity, l.checkInactivity)
	}
}

// onOutput is the ring observer: it counts bytes for the budget and stamps
// the last-output time for the inactivity deadline after authenticated start.
// It runs under the ring's lock and never blocks — the budget check fires
// outside the lock.
func (l *runLease) onOutput(n int) {
	l.mu.Lock()
	if !l.accountingStarted || l.done || l.suspended || l.firedReason != "" {
		l.mu.Unlock()
		return
	}
	l.output += int64(n)
	l.lastOutput = time.Now()
	budget := l.cfg.OutputBudget
	hit := budget > 0 && l.output >= budget
	l.mu.Unlock()
	if hit {
		l.fire(content.TermOutputBudget)
	}
}

// checkInactivity is the inactivity timer's own callback — the timer
// re-arms itself while output flows, so the deadline is exact (silence
// since the LAST byte, never the last check). On genuine silence it PARKS
// the run: the model is asked whether the silence is expected, and the
// command is left running while it answers. Reset from inside the AfterFunc callback is the
// standard self-rescheduling timer pattern: the timer is expired while its
// function runs, which is exactly the state Reset is documented for, and
// no other goroutine resets it.
func (l *runLease) checkInactivity() {
	l.mu.Lock()
	if !l.accountingStarted || l.done || l.suspended || l.parked || l.firedReason != "" {
		l.mu.Unlock()
		return
	}
	remaining := l.cfg.Inactivity - time.Since(l.lastOutput)
	if remaining > 0 {
		t := l.inactTimer
		l.mu.Unlock()
		if t != nil {
			t.Reset(remaining)
		}
		return
	}
	// SILENCE IS NOT A VERDICT. No firedReason is recorded — the execution
	// was not terminalized and content.TermInactivity is deliberately not
	// written here — and no context is cancelled, so the renderer is never
	// told to withdraw the command it is running. The park channel is what
	// releases the broker request's wait; the timers stay armed and the wall
	// clock keeps counting.
	l.parked = true
	parkC := l.parkC
	l.mu.Unlock()
	if parkC != nil {
		close(parkC)
	}
}

// resumeQuiet re-opens the quiet interval on a lease that parked: the model
// answered "keep waiting", so the silence is measured again from NOW under
// the (possibly narrower) bound this continuation asked for. The wall timer
// is untouched — that is the point of the whole arrangement.
func (l *runLease) resumeQuiet(quiet time.Duration, parkC chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done || l.firedReason != "" {
		return
	}
	l.parked = false
	l.renewals++
	l.parkC = parkC
	l.cfg.Inactivity = quiet
	l.lastOutput = time.Now()
	if l.inactTimer != nil {
		l.inactTimer.Stop()
		l.inactTimer = nil
	}
	if quiet > 0 && l.accountingStarted {
		l.inactTimer = time.AfterFunc(quiet, l.checkInactivity)
	}
}

// renewalCount is how many times the model answered "keep waiting" on this
// execution. Reported to the model so a loop is visible to the party in it.
func (l *runLease) renewalCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.renewals
}

// onLane is the lane-state callback, fired synchronously by the reporting
// goroutine (the read loop's agent.laneInteractivity handler) on every
// transition. When the lane enters awaiting-takeover the lease SUSPENDS:
// a TUI owns the lane, the human owns the execution, and the product's
// bounds no longer apply to it — killing an interactive program is exactly
// what decision 3 refuses to do. The run request stays pending until the
// renderer resolves (the human finishes, detaches or kills the program and
// the block freezes) or the renderer dies.
func (l *runLease) onLane() {
	l.mu.Lock()
	if l.done || l.suspended || l.firedReason != "" || !l.lane.awaitingTakeover(l.sid) {
		l.mu.Unlock()
		return
	}
	l.suspended = true
	l.mu.Unlock()
	l.disarm()
}

// disarm stops every trigger: the ring hook, both timers and the lane
// watch, and marks the lease done so an in-flight observer callback cannot
// act. Idempotent — called by onLane (suspension) and by supervise on both
// the success and the fired paths. Lock order: ring.mu alone first, then
// mu — the onOutput order.
func (l *runLease) disarm() {
	if l.ring != nil {
		l.ring.setWriteObserver(nil)
	}
	l.mu.Lock()
	l.done = true
	if l.wallTimer != nil {
		l.wallTimer.Stop()
		l.wallTimer = nil
	}
	if l.inactTimer != nil {
		l.inactTimer.Stop()
		l.inactTimer = nil
	}
	unwatch := l.unwatch
	l.unwatch = nil
	l.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
}

// escalate sends INT → TERM → KILL to the execution's process group,
// waiting SignalGrace between signals for the execution to cooperate. The
// escalation returns only when the execution is gone (cooperated), the
// KILL is sent (nothing can ignore it), or there is nothing in the
// foreground to signal — never before, so the caller can assert the
// execution is dead when this returns. Runs on the caller's goroutine.
//
// The ladder itself lives in foreground_signal.go, because the lease is no
// longer its only caller: a person's Stop on a running block's menu asks
// the same question — "how do you stop a process" — and a second answer to
// it would be two policies that agree until the day one of them changes
// (nocx-23rph, AGENTS.md "look for the existing answer"). What stays here
// is the lease's own part: which session, and what to say when there is no
// local process group at all.
func (l *runLease) escalate() foregroundOutcome {
	if l.sess == nil {
		l.log.Warn("run lease: no local process group to signal — the execution keeps running",
			"session_id", string(l.sid))
		return foregroundUnsupported
	}
	// The exact-attempt fallback is used only for this run's correlated
	// lifecycle attempt. The ordinary session projection intentionally
	// refuses to choose among multiple lanes, which is correct for a
	// human stop but not for an assistant request that already has an id.
	return stopForeground(l.log, l.sid, l.sess, l.cfg.SignalGrace, l.protected)
}

// cancelExecution withdraws this exact broker request and synchronously runs
// the established INT → TERM → KILL ladder while the request still owns the
// foreground execution. A completed/disarmed lease is inert — checked when the
// claim is taken AND again after the withdrawal, because those are two
// different instants and the request can finish between them — so a late turn
// cancellation can never signal a command the person started afterwards.
func (l *runLease) cancelExecution() foregroundOutcome {
	l.mu.Lock()
	if l.done || l.cancelStarted {
		l.mu.Unlock()
		return foregroundNothingRunning
	}
	l.cancelStarted = true
	cancel := l.cancel
	parkedEnd := l.parkedEnd
	l.mu.Unlock()
	// A parked run is ended through its one termination path, which already
	// withdraws the request and runs the ladder. Going round it here would
	// leave the broker request pending with nobody able to resolve it.
	if parkedEnd != nil {
		return parkedEnd(content.TermUserKilled)
	}
	if cancel == nil {
		// The control attached this lease before supervise armed the broker
		// request. Nothing owned by the assistant exists yet: mark the
		// cancellation for supervise to consume, but never signal whatever
		// unrelated foreground group happens to be in the session.
		return foregroundNothingRunning
	}
	// THE ADDRESSEE IS NAMED BEFORE THE WITHDRAWAL (nocx-uvac6.11), and that
	// ordering is the whole guarantee. While the broker request is still in
	// flight the foreground job IS this request's, by construction. Cancelling
	// first and asking afterwards inverts that: the request resolves, its
	// command exits, and the question "what is in front" is then answered by
	// whatever the person started next.
	//
	// Naming it does not commit to signalling it. If the command exits on its
	// own between here and the first rung, the ladder's own signal to that
	// exact group answers ESRCH and reports nothing-running — a dead addressee
	// is still the right addressee.
	if l.sess == nil {
		l.log.Warn("run lease: no local process group to signal — the execution keeps running",
			"session_id", string(l.sid))
		cancel()
		return foregroundUnsupported
	}
	pgid, outcome, named := foregroundJob(l.log, l.sid, l.sess)
	cancel()
	if !named {
		return outcome
	}
	return stopProcessGroup(l.log, l.sid, l.sess, pgid, l.cfg.SignalGrace)
}

// effectiveRunLease returns the bounds THIS run is supervised under: the
// config named by WithRunLease when it names any bound (the package default
// otherwise), with both time bounds replaced by the person's current
// settings whenever a registry is wired.
//
// THIS IS THE SETTINGS→LEASE PATH, and it is read here — once, when a run
// starts — deliberately. The value is snapshotted into the lease at
// construction and never re-read, so changing a number changes what the NEXT
// command is bound by and can never move a bound under a command that is
// already running. A person raising the ceiling does not rescue the command
// that is about to be killed; a person lowering it does not kill one
// mid-flight.
func (s *WSServer) effectiveRunLease() RunLeaseConfig {
	cfg := defaultRunLease
	if s.runLeaseCfg.WallClock > 0 || s.runLeaseCfg.Inactivity > 0 || s.runLeaseCfg.OutputBudget > 0 {
		cfg = s.runLeaseCfg
	}
	if s.settings == nil {
		return cfg
	}
	if v, err := s.settings.GetNumber(settings.AgentRunWallClockMinutes); err == nil {
		cfg.WallClock = settingMinutes(v)
	}
	if v, err := s.settings.GetNumber(settings.AgentRunQuietMinutes); err == nil {
		cfg.Inactivity = settingMinutes(v)
	}
	return cfg
}

// runLeaseIntegrationAvailable reports whether sid currently has an
// authenticated shell-integration lifecycle lane. The integration axis owns
// the current status; the lane registry proves the corresponding lane exists.
// Snapshot both under their locks and never hold either across a broker call.
func (s *WSServer) runLeaseIntegrationAvailable(sid session.ID) bool {
	if s.lifecyclePub == nil {
		return false
	}
	s.integrationMu.Lock()
	st, ok := s.integrations[sid]
	integrated := ok && st.status == IntegrationIntegrated
	s.integrationMu.Unlock()
	if !integrated {
		return false
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	for _, owner := range s.lifecycleLanes {
		if owner == sid {
			return true
		}
	}
	return false
}

// newRunLease builds the lease supervising one run on sid: the session's
// replay ring is the output observation point (bytes counted, never
// sniffed), the session itself is the process-group signaler when it has
// one, and the lane state carries the awaiting-takeover transition.
func (s *WSServer) newRunLease(sid session.ID, cfg RunLeaseConfig) *runLease {
	l := &runLease{
		log:   s.log.With("component", "run-lease"),
		sid:   sid,
		lane:  s.laneInteractivity,
		cfg:   cfg,
		parkC: make(chan struct{}),
	}
	if sess, err := s.registry.Get(sid); err == nil {
		if sg, ok := sess.(runLeaseSession); ok {
			l.sess = sg
		}
	}
	if rx := s.getRx(sid); rx != nil {
		l.ring = rx.ring
	}
	return l
}
