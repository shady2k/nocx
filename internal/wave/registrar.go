package wave

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Registrar brings a participant into existence and keeps the two terminal
// facts about it.
//
// # The order is the rollback
//
// There is no journal here, and that is a decision rather than an omission. A
// journal exists to support DEFERRED background cleanup, and there is none:
// within one backend lifetime every compensation is available immediately and
// synchronously, and across a restart the worker is gone with the backend that
// held it, so a record that outlived it would describe nothing. What replaces
// the journal is the ORDER — the step that can fail for a reason the caller
// cannot undo goes first, so the failure path is one removal and nothing else:
//
//  1. Validate and reserve the bound. A refusal here has forked NOTHING, which
//     is the whole reason it is first.
//  2. Commit the participant in prepared. A durable record with nothing behind
//     it is deliberately reachable — it is the state the interval exists to
//     make discoverable.
//  3. Create the session and fork the launcher, with the id already minted, so
//     a failed connect registers nothing.
//  4. The launcher enrols BEFORE it execs the real agent. Failure is closed:
//     no enrolment, no orchestration, and no unorchestrated agent either,
//     because the launcher never reached its exec.
//  5. Commit the delegation. Until this, the participant is addressable and
//     nobody may act on it.
//  6. Mark live, THEN attach supervision — in that order, to a record that
//     already exists. A process exiting between the two is still observed,
//     because the watcher finds an already-terminal process rather than
//     missing a transition. The ordering, not a lock, is what makes it
//     race-free.
//
// # A failure is never a verdict
//
// A compensation that itself fails leaves the record NON-TERMINAL and is
// retried. It never writes a terminal state it did not establish. The
// asymmetry is deliberate and the code must not smooth it away: a record that
// outlives its process by one restart costs a stale row, while a wrongly
// adopted participant would cost a coordinator addressing a process that is
// not its worker.
type Registrar struct {
	store Store
	spawn Spawner
	enrol Enrolments
	sup   Supervisor

	bound    int
	deadline time.Duration
	newID    func() ParticipantID
	now      func() time.Time
}

// Option configures a Registrar. The two numbers are injected rather than
// constant because a deadline that is wrong in either direction breaks the
// procedure, and the composition root is where the product's real values live.
type Option func(*Registrar)

// WithBound sets how many non-terminal participants one wave may hold.
func WithBound(n int) Option { return func(r *Registrar) { r.bound = n } }

// WithEnrolmentDeadline bounds step 4. Its VALUE is not decided here — it is
// measured where fan-out is measured — but the procedure needs both ends of
// the interval named, and this is the closing one for an enrolment that never
// arrives.
func WithEnrolmentDeadline(d time.Duration) Option {
	return func(r *Registrar) { r.deadline = d }
}

// WithIDs replaces the participant id source.
func WithIDs(f func() ParticipantID) Option { return func(r *Registrar) { r.newID = f } }

// WithClock replaces the clock. Nothing in this package waits on a duration to
// decide anything; the clock stamps facts, and a test that needs a stable
// stamp replaces it rather than tolerating one.
func WithClock(f func() time.Time) Option { return func(r *Registrar) { r.now = f } }

// NewRegistrar wires the record to its four seams.
func NewRegistrar(s Store, sp Spawner, e Enrolments, sup Supervisor, opts ...Option) *Registrar {
	r := &Registrar{
		store:    s,
		spawn:    sp,
		enrol:    e,
		sup:      sup,
		bound:    defaultBound,
		deadline: defaultEnrolmentDeadline,
		newID:    newParticipantID,
		now:      time.Now,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// RegisterRequest is one participant's worth of registration.
type RegisterRequest struct {
	Wave               ID
	CoordinatorSession string
	Role               Role
	Task               string
	// Command is the line the participant runs, passed through to the
	// spawner untouched.
	Command     string
	Environment string
	// CreatedByRunID is provenance and nothing else. It records which run
	// asked; it never decides whether an operation is allowed.
	CreatedByRunID string
}

// Register runs the six steps above.
//
// It returns the participant it left behind even on failure, because a caller
// that cannot name the record cannot check what happened to it — and "a
// registration that failed" is exactly when naming it matters.
func (r *Registrar) Register(ctx context.Context, req RegisterRequest) (Participant, error) {
	// Step 1. Nothing is forked and no record exists, so a refusal is free.
	held, reserveErr := r.store.NonTerminal(ctx, req.Wave)
	if reserveErr != nil {
		return Participant{}, fmt.Errorf("wave: reserve: %w", reserveErr)
	}
	if len(held) >= r.bound {
		return Participant{}, fmt.Errorf("wave %q holds %d of %d: %w", req.Wave, len(held), r.bound, ErrBoundExceeded)
	}

	// Step 2. From here on there is a record, and every later failure
	// terminalizes it rather than leaving it to be found as a live worker.
	p := Participant{
		ID:           r.newID(),
		Wave:         req.Wave,
		Role:         req.Role,
		State:        StatePrepared,
		Task:         req.Task,
		RegisteredAt: r.now(),
	}
	if err := r.store.CommitPrepared(ctx, p); err != nil {
		return Participant{}, fmt.Errorf("wave: commit prepared: %w", err)
	}

	// Step 3. The id is already minted and travels with the request, so a
	// launcher that fails to connect has registered nothing under a name we
	// did not choose.
	spawned, spawnErr := r.spawn.Spawn(ctx, SpawnRequest{
		Participant: p.ID,
		Wave:        req.Wave,
		Task:        req.Task,
		Command:     req.Command,
		Environment: req.Environment,
	})
	if spawnErr != nil {
		return p, r.compensate(ctx, p, nil, false, fmt.Errorf("wave: spawn: %w", spawnErr))
	}

	// Step 4. Bounded, because an enrolment that never arrives must not hold
	// the record open forever. The bound closes the interval; it does not
	// decide anything about the participant.
	awaitCtx, cancel := context.WithTimeout(ctx, r.deadline)
	live, enrolErr := r.enrol.Await(awaitCtx, p.ID)
	cancel()
	if enrolErr != nil {
		return p, r.compensate(ctx, p, spawned, false, fmt.Errorf("wave: await enrolment: %w", enrolErr))
	}

	// Step 5. Membership already exists; this is what makes the participant
	// controllable, and the bundle deliberately withholds delegate-further.
	del := Delegation{
		ControllerSession: req.CoordinatorSession,
		Participant:       p.ID,
		Epoch:             live.Epoch,
		CreatedByRunID:    req.CreatedByRunID,
		Effects:           DefaultBundle(),
		State:             DelegationActive,
	}
	if err := r.store.PutDelegation(ctx, del); err != nil {
		return p, r.compensate(ctx, p, spawned, true, fmt.Errorf("wave: delegation: %w", err))
	}

	// Step 6, and the order inside it is the point.
	if err := r.store.MarkLive(ctx, p.ID, live); err != nil {
		return p, r.compensate(ctx, p, spawned, true, fmt.Errorf("wave: mark live: %w", err))
	}
	p.State = StateLive
	p.Liveness = live
	if err := r.sup.Attach(ctx, p); err != nil {
		return p, r.compensate(ctx, p, spawned, true, fmt.Errorf("wave: attach supervision: %w", err))
	}
	return p, nil
}

// compensate undoes what was built, in the reverse order of building it, and
// returns cause. It never replaces cause with its own error: what the caller
// needs to know is why the registration failed, and a compensation that also
// failed is a fact about the record, which the record itself then carries by
// staying non-terminal.
func (r *Registrar) compensate(ctx context.Context, p Participant, spawned Spawned, enrolled bool, cause error) error {
	if enrolled {
		if err := r.enrol.Withdraw(ctx, p.ID); err != nil {
			return errors.Join(cause, fmt.Errorf("wave: withdraw enrolment: %w", err))
		}
	}
	if spawned != nil {
		if err := spawned.Kill(ctx); err != nil {
			return errors.Join(cause, fmt.Errorf("wave: kill launcher: %w", err))
		}
	}
	if err := r.store.Terminalize(ctx, p.ID, StateInterrupted); err != nil {
		// A failure is never a verdict. The record stays non-terminal and
		// the next pass completes what this one could not.
		return errors.Join(cause, fmt.Errorf("wave: terminalize: %w", err))
	}
	return cause
}

// Declared admits the participant's own terminal fact and reduces.
func (r *Registrar) Declared(ctx context.Context, id ParticipantID, l Liveness, d Declaration) (Participant, error) {
	return r.admit(ctx, id, l, func(ctx context.Context) (Participant, error) {
		return r.store.RecordDeclaration(ctx, id, d)
	})
}

// Exited admits the process fact and reduces.
func (r *Registrar) Exited(ctx context.Context, id ParticipantID, l Liveness, e Exit) (Participant, error) {
	return r.admit(ctx, id, l, func(ctx context.Context) (Participant, error) {
		return r.store.RecordExit(ctx, id, e)
	})
}

// admit is the incarnation guard and the reduction, in that order.
//
// The guard is what stops a late fact from a replaced attempt overwriting a
// current one, and it compares the FULL identity rather than an attempt
// number, because output offsets are per-session replay coordinates and a
// domain can be re-established under one session.
func (r *Registrar) admit(ctx context.Context, id ParticipantID, l Liveness, record func(context.Context) (Participant, error)) (Participant, error) {
	cur, err := r.store.Participant(ctx, id)
	if err != nil {
		return Participant{}, err
	}
	if !cur.Liveness.SameIncarnation(l) {
		return cur, fmt.Errorf("wave: participant %q: %w", id, ErrStaleEvidence)
	}
	// A record that is already terminal takes no more facts, with exactly one
	// exception: abandoned is the half-answered state, and the declaration
	// that completes the conjunction may still arrive. Interrupted is not
	// that — it was written by our own compensation or by the restart sweep,
	// so a fact arriving against it is evidence about a process we already
	// established is not ours to judge.
	if cur.State.Terminal() && cur.State != StateAbandoned {
		return cur, fmt.Errorf("wave: participant %q is %s: %w", id, cur.State, ErrTerminal)
	}
	after, err := record(ctx)
	if err != nil {
		return Participant{}, err
	}
	want := reduce(after)
	if want == after.State {
		return after, nil
	}
	if err := r.store.Terminalize(ctx, id, want); err != nil {
		return after, fmt.Errorf("wave: terminalize %q: %w", want, err)
	}
	after.State = want
	return after, nil
}

// reduce derives the state from the FACT SET, never from the order the facts
// arrived in. That is what makes the conjunction hold both ways round: an exit
// observed before the declaration reads abandoned, and the declaration that
// follows REFINES it to completed. That refinement is the second half of a
// conjunction arriving late, not a resurrection — nothing about the process
// becomes untrue, and no other transition out of a terminal state is possible
// here because no other fact exists to make one.
func reduce(p Participant) State {
	if p.Exited == nil {
		// A declaration with no exit is not terminal. The agent said it
		// finished and is still running; it may be given more work.
		return p.State
	}
	if p.Declared == nil {
		// Gone, and it never said what it produced. Terminal, and named so
		// it can never be misread as a completion.
		return StateAbandoned
	}
	if p.Declared.OK {
		return StateCompleted
	}
	return StateFailed
}

// HeldBy answers D3: a restarted coordinator asks what its SESSION holds and
// is told by name. It asks the session and not the run, because the run that
// spawned the worker has ended by the time the question is asked — which is
// the entire situation the question exists for. A lost context has no lease
// handle to present, and lineage proves provenance and confers no authority,
// so neither is what is asked here.
func (r *Registrar) HeldBy(ctx context.Context, coordinatorSession string) ([]Participant, error) {
	return r.store.HeldBy(ctx, coordinatorSession)
}

// Sweep terminalizes every non-terminal participant of a wave as interrupted.
//
// It is the second pass the compensation rule promises — "a failure is never a
// verdict", so a compensation that could not run leaves the record open and
// THIS completes it — and it is also what a backend restart runs, for the same
// reason and with the same effect. One procedure, because the two situations
// are one fact: a participant is open and the process behind it is not this
// backend's to judge.
//
// It NEVER adopts. Adoption would require identifying the process found at the
// far end as the one we spawned, and no pin exists in this tree to do that
// with. The asymmetry is the argument: an interrupted record costs a row that
// outlives its process by one restart, while a wrongly adopted participant
// costs a coordinator addressing a process that is not its worker.
func (r *Registrar) Sweep(ctx context.Context) error {
	held, err := r.store.AllNonTerminal(ctx)
	if err != nil {
		return fmt.Errorf("wave: sweep: %w", err)
	}
	var errs []error
	for _, p := range held {
		if err := r.store.Terminalize(ctx, p.ID, StateInterrupted); err != nil {
			// Keep going: one record that could not be closed must not
			// leave the rest open behind it.
			errs = append(errs, fmt.Errorf("wave: sweep %q: %w", p.ID, err))
		}
	}
	return errors.Join(errs...)
}
