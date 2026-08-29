package transport

// The run lease (ADR-0020 decision 2): every execution runs under a
// wall-clock deadline (bounded ceiling), a separate inactivity deadline —
// silence is a different failure from slowness, and a single timeout cannot
// tell them apart — an output budget, and cancellation that escalates
// INT → TERM → KILL against the execution's OWN process group, so
// cancellation reaches the command's children rather than only the shell.
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
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// RunLeaseConfig bounds one run execution. Zero disables the corresponding
// bound; all zero disables the lease entirely (the broker's pre-lease
// timeout applies). The WSServer's defaults are defaultRunLease.
type RunLeaseConfig struct {
	// WallClock bounds the whole execution, from the run request to the
	// resolution — the bounded ceiling of decision 2. It is the ONLY bound
	// when the renderer never answers at all.
	WallClock time.Duration
	// Inactivity bounds the silence: no output for this long ends the
	// execution. Independent of WallClock so a command can be slow AND
	// alive — only a command that is both silent and endless is wedged.
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

// DefaultRunLeaseConfig is the production lease, named at the composition
// root the way the lane capacity is: the transport's own default and the
// production value are the SAME value, and this accessor is what lets the
// composition root say so explicitly (and a future settings surface flip
// one bound in one place).
func DefaultRunLeaseConfig() RunLeaseConfig { return defaultRunLease }

// defaultRunLease is the product's lease: the pre-lease runRequestTimeout
// stays as the wall-clock ceiling (continuity — the bound that existed was
// ten minutes), inactivity and the output budget are the new bounds the
// decision adds, and the escalation grace is the time a well-behaved
// program gets to exit on each signal.
var defaultRunLease = RunLeaseConfig{
	WallClock:    10 * time.Minute,
	Inactivity:   2 * time.Minute,
	OutputBudget: 4 << 20, // 4 MiB
	SignalGrace:  time.Second,
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
	log  log.Logger
	sid  session.ID
	sess runLeaseSession // nil → no local process group to signal
	ring *outputRing     // nil → no output observation (wall-clock only)
	lane *laneState      // nil → no awaiting-takeover observation
	cfg  RunLeaseConfig

	mu            sync.Mutex
	cancel        context.CancelFunc
	wallTimer     *time.Timer
	inactTimer    *time.Timer
	unwatch       func()
	lastOutput    time.Time
	output        int64
	firedReason   content.TerminationReason
	done          bool
	suspended     bool
	cancelStarted bool
}

// supervise runs fn (the broker request) under the lease, INLINE on the
// caller's goroutine. It returns fn's result when the run completes within
// its bounds, or a RunLeaseError naming the bound that fired — after the
// escalation has run to completion, so the caller can assert, on
// RequestRun's return, that the execution is actually dead, not merely
// scheduled to die.
func (l *runLease) supervise(ctx context.Context, fn func(ctx context.Context) error) error {
	cfg := l.cfg
	if cfg.SignalGrace <= 0 {
		cfg.SignalGrace = defaultRunSignalGrace
	}
	enabled := cfg.WallClock > 0 || cfg.Inactivity > 0 || cfg.OutputBudget > 0
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer l.disarm()

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
		l.lastOutput = time.Now()
		if cfg.WallClock > 0 {
			l.wallTimer = time.AfterFunc(cfg.WallClock, func() { l.fire(content.TermTimeout) })
		}
		if cfg.Inactivity > 0 {
			l.inactTimer = time.AfterFunc(cfg.Inactivity, l.checkInactivity)
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

	// Disarm before the verdict: every trigger is stopped or neutered, so
	// firedReason is final and no observer can act after this point.
	l.disarm()
	l.mu.Lock()
	reason := l.firedReason
	l.mu.Unlock()
	if reason != "" {
		l.escalate()
		return &assistant.RunLeaseError{Reason: reason, Err: err}
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
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// onOutput is the ring observer: it counts bytes for the budget and stamps
// the last-output time for the inactivity deadline. It runs under the
// ring's lock and never blocks — the budget check fires outside the lock.
func (l *runLease) onOutput(n int) {
	l.mu.Lock()
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
// since the LAST byte, never the last check). On genuine silence it fires
// the inactivity bound. Reset from inside the AfterFunc callback is the
// standard self-rescheduling timer pattern: the timer is expired while its
// function runs, which is exactly the state Reset is documented for, and
// no other goroutine resets it.
func (l *runLease) checkInactivity() {
	l.mu.Lock()
	if l.done || l.suspended || l.firedReason != "" {
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
	l.firedReason = content.TermInactivity
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	// No protected-group fallback (nocx-7l4ex.10): the lease supervises the
	// agent's run and holds no authenticated lifecycle attempt, so over a
	// protected group it has nothing that says a program is in there. It
	// takes the honest refusal rather than writing a byte on a guess.
	return stopForeground(l.log, l.sid, l.sess, l.cfg.SignalGrace, nil)
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
	l.mu.Unlock()
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

// effectiveRunLease returns the server's lease config: the config named by
// WithRunLease when it names any bound, the package default otherwise.
func (s *WSServer) effectiveRunLease() RunLeaseConfig {
	if s.runLeaseCfg.WallClock > 0 || s.runLeaseCfg.Inactivity > 0 || s.runLeaseCfg.OutputBudget > 0 {
		return s.runLeaseCfg
	}
	return defaultRunLease
}

// newRunLease builds the lease supervising one run on sid: the session's
// replay ring is the output observation point (bytes counted, never
// sniffed), the session itself is the process-group signaler when it has
// one, and the lane state carries the awaiting-takeover transition.
func (s *WSServer) newRunLease(sid session.ID, cfg RunLeaseConfig) *runLease {
	l := &runLease{
		log:  s.log.With("component", "run-lease"),
		sid:  sid,
		lane: s.laneInteractivity,
		cfg:  cfg,
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
