package capability

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/operation/schedule"
	"github.com/shady2k/nocx/internal/transport/control"
)

// gitAllowance is the catalogue's lifetime for one git invocation, read from
// the scope the dispatcher began. Turn bounds the wait for a checkout key and a
// capacity permit; max bounds the work itself, from the moment it starts.
type gitAllowance struct {
	Operation string
	Turn      time.Duration
	Max       time.Duration
	Expired   catalog.Expired
	Merge     catalog.Merge
	Origin    catalog.Origin
}

func allowanceFromContext(ctx context.Context) (gitAllowance, bool) {
	scope, ok := catalog.ScopeFrom(ctx)
	if !ok {
		return gitAllowance{}, false
	}
	d := scope.Declaration()
	return gitAllowance{
		Operation: string(d.ID),
		Turn:      d.Turn.Duration(),
		Max:       d.Max.Duration(),
		Expired:   d.Expired,
		Merge:     d.Merge,
		Origin:    scope.Origin(),
	}, true
}

// NotStartedError says a call never began: its turn expired while it waited
// for the checkout or for capacity, and nothing was changed.
type NotStartedError struct {
	Operation string
	Waited    time.Duration
}

func (e *NotStartedError) Error() string {
	return fmt.Sprintf("%s did not start: waited %s for this checkout's turn and nothing changed", e.Operation, e.Waited)
}

// ExpiredError says the work outlived its declaration's max. Outcome is the
// declaration's own answer — a mutation's is outcome-unknown until its record
// says otherwise, a read's is retry-read.
type ExpiredError struct {
	Operation string
	Outcome   catalog.Expired
	Domain    string
	Budget    time.Duration
	// Suspected says the failure domain, not the call's own budget, is why
	// nothing started: its abandoned workers reached the breaker's bound.
	Suspected bool
}

func (e *ExpiredError) Error() string {
	if e.Suspected {
		return fmt.Sprintf("%s did not start: %s is not answering", e.Operation, domainName(e.Domain))
	}
	switch e.Outcome {
	case catalog.ExpiredOutcomeUnknown:
		return fmt.Sprintf("%s reached its %s deadline and its outcome is unknown: check this checkout before retrying", e.Operation, e.Budget)
	case catalog.ExpiredRetryRead:
		return fmt.Sprintf("%s reached its %s deadline and changed nothing: retry the read", e.Operation, e.Budget)
	default:
		return fmt.Sprintf("%s did not start within its %s deadline", e.Operation, e.Budget)
	}
}

// gitRun is one git invocation's lifetime. acquire takes whatever the call
// must hold — the checkout's key, the mount's permit, the lane — on the worker
// goroutine, and answers the functions that give it back. fn is the work. The
// caller settles at whichever comes first: fn returns, the turn expires, or the
// max expires.
//
// The worker is not the caller: at the max the call settles and the worker
// keeps what it holds until it really returns (ADR-8eej3e66 §6). Abandon tells
// the bulkhead that this worker outlived its request, so the failure domain's
// breaker can count it.
//
// Whether the call STARTED has one owner, startDecision: the worker claiming
// its start and the caller's turn running out race for it under one lock, and
// the loser reads the winner's answer. Two independent selects would let both
// win at the turn's edge — a start published and a mutation entered while the
// caller answered "nothing changed" (ADR-8eej3e66 §4).
//
// Without a catalogue scope (a direct capability test), the call runs with no
// deadline: there is nothing to enforce and inventing one would be a second
// declaration.
func gitRun(ctx context.Context, allow gitAllowance, acquire func(context.Context) (finish, abandon func(), err error), fn func(context.Context) error) error {
	if allow.Turn <= 0 && allow.Max <= 0 {
		finish, _, err := acquire(ctx)
		if err != nil {
			return err
		}
		defer finish()
		return fn(ctx)
	}

	turnCtx, cancelTurn := context.WithTimeout(ctx, allow.Turn)
	defer cancelTurn()

	decision := newStartDecision()
	done := make(chan error, 1)

	go func() {
		finish, abandon, err := acquire(turnCtx)
		if err != nil {
			decision.fail(err)
			return
		}
		// A grant that arrived in the same instant as the turn's expiry did
		// not start anything. It is decided against the caller's answer in
		// one step below, never here alone.
		if cerr := turnCtx.Err(); cerr != nil {
			finish()
			decision.fail(cerr)
			return
		}
		lifetime := allow.Max
		if scope, ok := catalog.ScopeFrom(ctx); ok {
			// Max begins when work begins, never at the invocation, and a
			// child stays inside its parent's remainder.
			if rearmed := scope.StartWork(); rearmed > 0 {
				lifetime = rearmed
			}
		}
		workCtx, cancelWork := context.WithCancel(ctx)
		if lifetime > 0 {
			// The cause a provider sees is the deadline, not a bare
			// cancellation: internal/git/local and the helper both report
			// ctx.Err() upward, and "deadline exceeded" is what the panel's
			// expiry answer is built on.
			workCtx, cancelWork = context.WithDeadline(ctx, time.Now().Add(lifetime))
		}
		expired := make(chan struct{})
		var once sync.Once
		// Only one of these wins: the work returning inside its budget, or
		// the deadline. A finish in the deadline's own instant is a
		// completion, the same rule local git reads apply to a child that
		// finishes as its wall clock is cut.
		settle := func(err error) {
			once.Do(func() {
				cancelWork()
				done <- err
			})
		}
		if !decision.begin(gitStart{expired: expired, abandon: abandon}) {
			// The caller's turn ran out while this worker was acquiring, and
			// it answered that nothing changed. Nothing may change.
			cancelWork()
			finish()
			return
		}
		var timer *time.Timer
		if lifetime > 0 {
			timer = time.AfterFunc(lifetime, func() {
				once.Do(func() { close(expired) })
			})
		}
		err = fn(workCtx)
		if timer != nil {
			timer.Stop()
		}
		settle(err)
		finish()
	}()

	select {
	case <-decision.ready:
	case <-turnCtx.Done():
		if decision.expire(turnCtx.Err()) {
			return gitWaitError(ctx, allow, turnCtx.Err())
		}
		// The worker claimed the start first, so this call settles with the
		// work's own outcome rather than with "nothing changed".
	}
	s, err := decision.outcome()
	if err != nil {
		return gitWaitError(ctx, allow, err)
	}

	select {
	case err := <-done:
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			s.abandon()
			return &ExpiredError{Operation: allow.Operation, Outcome: allow.Expired, Budget: allow.Max}
		}
		return err
	case <-s.expired:
		s.abandon()
		return &ExpiredError{Operation: allow.Operation, Outcome: allow.Expired, Budget: allow.Max}
	}
}

// gitStart is what the caller needs to watch a call that began: the channel
// that says its max deadline arrived, and the way to tell the bulkhead the
// worker outlived the request. Everything else the worker keeps to itself.
type gitStart struct {
	expired chan struct{}
	abandon func()
}

// startDecision is the ONE owner of "did this call start". The worker that
// acquired the resources and the caller whose turn ran out race for it under
// one lock, and the loser reads the winner's answer.
//
// TWO SELECTS COULD NOT DO THIS. The worker used to check the turn, publish
// its start and enter the mutation while the caller's own select — with its
// start arm and its deadline arm both ready — took whichever Go happened to
// pick. A mutation could therefore apply while the person was told nothing
// changed, which is the one sentence a timeout must never say wrongly
// (ADR-8eej3e66 §4).
type startDecision struct {
	mu      sync.Mutex
	settled bool
	start   gitStart
	err     error
	ready   chan struct{}
}

func newStartDecision() *startDecision {
	return &startDecision{ready: make(chan struct{})}
}

// begin is the worker's claim, taken once the resources are acquired and the
// turn is still live. It answers false when the caller has already settled —
// its turn ran out while this worker was acquiring — and the worker must then
// give its resources back without running anything.
func (d *startDecision) begin(s gitStart) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.settled {
		return false
	}
	d.settled, d.start = true, s
	close(d.ready)
	return true
}

// fail is the worker's answer when it could not acquire: the call never
// started, for the reason given. A decision already settled by the caller's
// turn keeps the caller's answer — the worker's late failure describes a wait
// nobody is waiting for.
func (d *startDecision) fail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.settled {
		return
	}
	d.settled, d.err = true, err
	close(d.ready)
}

// expire is the caller's turn running out. It answers whether that answer
// stands: false means the worker claimed the start first, so the call is
// running and the caller must wait for its outcome.
func (d *startDecision) expire(err error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.settled {
		return false
	}
	d.settled, d.err = true, err
	close(d.ready)
	return true
}

// outcome is the settled decision: the start when the call began, otherwise
// why it did not. Read it only after ready is closed.
func (d *startDecision) outcome() (gitStart, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.start, d.err
}

// gitWaitError turns a failed wait into the declaration's answer. A cancelled
// request is the caller's own cancellation; an expired turn is not-started.
func gitWaitError(ctx context.Context, allow gitAllowance, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		settleScope(ctx, catalog.OutcomeNotStarted)
		return &NotStartedError{Operation: allow.Operation, Waited: allow.Turn}
	}
	return err
}

// gitBulkheadError turns a bulkhead refusal into the declaration's answer. A
// suspect domain is the entry's expired outcome, naming the domain; a full
// queue is the existing capacity refusal, which stage 8 removes with the rest
// of them.
func gitBulkheadError(ctx context.Context, allow gitAllowance, err error) error {
	var refusal *schedule.Refusal
	if errors.As(err, &refusal) && refusal.Reason == schedule.ReasonDomainSuspect {
		settleScope(ctx, catalog.OutcomeNotStarted)
		return &ExpiredError{Operation: allow.Operation, Outcome: allow.Expired, Domain: refusal.Domain, Suspected: true}
	}
	if errors.As(err, &refusal) {
		return gitRefused(ctx, &control.Rejection{Reason: refusal.Reason, Scope: refusal.Scope})
	}
	return err
}

// gitRefused is a capacity refusal at the git seam: it settles the scope as
// refused before the dispatcher's own Complete can record a completion, so
// the record says the call never ran.
func gitRefused(ctx context.Context, rejection *control.Rejection) error {
	settleScope(ctx, catalog.OutcomeRefused)
	return refused(ctx, rejection)
}

func settleScope(ctx context.Context, outcome catalog.Outcome) {
	if scope, ok := catalog.ScopeFrom(ctx); ok {
		scope.Settle(outcome)
	}
}

// domainName names a failure domain in a person's terms. The key is server
// vocabulary (a mount device or a remote session), so it is reported as the
// checkout's disk rather than shown raw.
func domainName(domain string) string {
	if domain == "" {
		return "the checkout"
	}
	return "the disk for this checkout"
}

// gitClass is the bulkhead class of an invocation: a person's call has the
// reserve, a background poll does not.
func gitClass(origin catalog.Origin) (class string) {
	if origin == catalog.OriginBackground {
		return "background"
	}
	return "person"
}
