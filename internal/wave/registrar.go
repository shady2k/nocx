package wave

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/log"
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

	// attention is the undispatched fact set and its two routes out
	// (nocx-dkawo.3). It is never nil: an unwired one still records every
	// fact and says at Error that it has nothing to reach anyone with, which
	// is the same stance the supervisor takes with an unwired destination.
	attention *Backstop
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

// WithBackstop replaces the undispatched fact set. The composition root
// supplies one wired to the pane typist and to the notification pipeline; the
// default is wired to neither and says so.
func WithBackstop(b *Backstop) Option { return func(r *Registrar) { r.attention = b } }

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
		attention: NewBackstop(
			log.NewSlogAdapter(nil),
			// No routes. Every fact is still recorded and every missing
			// route names itself in the log, because a fact dropped for
			// want of wiring is how a feature that does not exist survives
			// a release.
			nil, nil),
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
	// The wave exists because a coordinator spawned into it, never because
	// somebody opened one: a coordinator's first spawn is what a wave IS.
	// Its id defaults to the coordinator's session, which is the identity D3
	// answers by — one coordinator session, one wave — so nothing has to
	// carry a second name for the same thing.
	if req.Wave == "" {
		req.Wave = ID(req.CoordinatorSession)
	}
	if err := r.store.EnsureWave(ctx, req.Wave, req.CoordinatorSession); err != nil {
		return Participant{}, fmt.Errorf("wave: ensure: %w", err)
	}
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
	return r.admit(ctx, id, l, FactDeclared, func(ctx context.Context) (Participant, error) {
		return r.store.RecordDeclaration(ctx, id, d)
	})
}

// Exited admits the process fact and reduces.
func (r *Registrar) Exited(ctx context.Context, id ParticipantID, l Liveness, e Exit) (Participant, error) {
	return r.admit(ctx, id, l, FactExited, func(ctx context.Context) (Participant, error) {
		return r.store.RecordExit(ctx, id, e)
	})
}

// admit is the incarnation guard and the reduction, in that order.
//
// The guard is what stops a late fact from a replaced attempt overwriting a
// current one, and it compares the FULL identity rather than an attempt
// number, because output offsets are per-session replay coordinates and a
// domain can be re-established under one session.
func (r *Registrar) admit(ctx context.Context, id ParticipantID, l Liveness, kind FactKind, record func(context.Context) (Participant, error)) (Participant, error) {
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
	if want != after.State {
		if err := r.store.Terminalize(ctx, id, want); err != nil {
			return after, fmt.Errorf("wave: terminalize %q: %w", want, err)
		}
		after.State = want
	}
	// The fact needs judgement, and it enters the set AFTER the record is
	// settled — so what the coordinator is woken about is what the record
	// says, never what the caller believed it was about to say.
	r.route(ctx, after, kind)
	return after, nil
}

// route decides what the record does about an admitted fact, and it is the
// design question §4 of the bead calls the one that decides whether any of
// this is useful.
//
// # The table
//
// A fact whose participant did not SUCCEED needs judgement, whatever else is
// running: a worker that failed or died without saying anything is the
// situation a coordinator exists to handle, and holding it until the wave is
// finished would report a crash after the work that depended on it.
//
// A fact whose participant succeeded needs judgement only when NOTHING ELSE
// IS RUNNING. "A worker finished with two still running" is routine — the
// coordinator is waiting on all of them and has nothing to decide yet — and
// waking it costs a turn to be told what it already expects. When the last
// one lands, the wave is finished and nobody has read it, and that is the
// moment the coordinator is for.
//
// # Why the wave's remaining work and not the fact alone
//
// The same fact means different things at different moments, and the record
// is the only thing that knows which moment it is. A design that classified
// facts in isolation would have to choose between waking on every completion
// (which is the poll this whole mechanism replaced) and waking on none
// (which loses the end of the wave). Nothing on a screen takes part in this:
// what is read is the participant rows.
func (r *Registrar) route(ctx context.Context, p Participant, kind FactKind) {
	f := Fact{
		Participant: p.ID,
		Wave:        p.Wave,
		Kind:        kind,
		State:       p.State,
		Task:        p.Task,
	}
	if !r.needsJudgement(ctx, p) {
		// No coordinator is read for a routine fact: nobody is being
		// addressed, so looking up who would be is a store call the answer
		// does not depend on.
		r.attention.Routine(f)
		return
	}
	// The coordinator is read NOW and not when the deadline fires: by then
	// the wave may hold nothing non-terminal.
	coordinator, err := r.store.CoordinatorSession(ctx, p.Wave)
	if err != nil {
		// The fact is real and the record has it; what is missing is who to
		// tell. Entering it with an empty coordinator would produce a wake
		// addressed to nowhere that still escalates on time, which is the
		// honest half of the two.
		coordinator = ""
	}
	f.CoordinatorSession = coordinator
	r.attention.Entered(ctx, f)
}

// needsJudgement is the table itself, kept apart from the plumbing above so
// that what it decides can be read in one screen.
func (r *Registrar) needsJudgement(ctx context.Context, p Participant) bool {
	if !succeeded(p) {
		return true
	}
	// "Is anything else still working?" — the participant this fact is about
	// does not count, because a worker that just said it finished is not the
	// reason its coordinator should wait.
	open, err := r.store.NonTerminal(ctx, p.Wave)
	if err != nil {
		// A read that failed is not evidence that the wave is finished, and
		// it is not evidence that it is not. Judgement is the fail-closed
		// direction: a fact the coordinator did not need costs it one turn,
		// and a fact it never learns about costs it the wave.
		return true
	}
	for _, other := range open {
		if other.ID != p.ID {
			return false
		}
	}
	return true
}

// succeeded reports whether the participant, as the record now stands, has
// given nobody anything to worry about.
//
// A live participant that declared OK counts as succeeded: it said it
// finished and it is still there, so whether the coordinator is needed
// depends on the rest of the wave rather than on it. Abandoned, failed and
// interrupted do not, and neither does a declaration that reported failure —
// which is the closest thing the record has to a worker ASKING, until the
// mailbox gives it a word of its own.
func succeeded(p Participant) bool {
	if p.Declared != nil && !p.Declared.OK {
		return false
	}
	switch p.State {
	case StateFailed, StateAbandoned, StateInterrupted:
		return false
	default:
		return true
	}
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
	held, err := r.store.HeldBy(ctx, coordinatorSession)
	if err != nil {
		return nil, err
	}
	// This IS the dispatch (D8: the cursor advances on the fetch, which is
	// the second of four acknowledgements and the only one a backend can
	// observe). The coordinator has now been told what its session holds, so
	// every fact about those participants has reached it and its deadline
	// stops. Acting on the fact is the fourth acknowledgement and is
	// deliberately not claimed here.
	ids := make([]ParticipantID, 0, len(held))
	for _, p := range held {
		ids = append(ids, p.ID)
	}
	r.attention.Dispatched(ids...)
	return held, nil
}

// Cost is what the mechanism has spent so far — the number §12 of the design
// says the whole thing is judged by. It is a read on the record rather than a
// log line to grep, because "measured and reported, not assumed" is an
// acceptance criterion and a criterion nothing can query is prose.
func (r *Registrar) Cost() Stats { return r.attention.Stats() }

// Undispatched is what the record still owes judgement on. It is the read
// behind "a wave with no undispatched facts wakes nobody": an empty answer
// and no armed alarm are the same statement.
func (r *Registrar) Undispatched() []Fact { return r.attention.Open() }

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
