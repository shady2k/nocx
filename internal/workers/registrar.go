package workers

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
//  2. Commit the participant in prepared. A committed record with nothing
//     behind it is deliberately reachable — it is the state the interval
//     exists to make discoverable.
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

	// closer ends a participant. It is a seam because ending one means
	// closing a SESSION, and what a session is belongs to the composition
	// root; this package knows only that a participant has one.
	closer Closer

	// screener reads a participant's pane (nocx-f545a.6), for closer's
	// reason: the grid is the composition root's, not this package's.
	screener Screener

	// answerer answers a participant's menu (nocx-f545a.4), for the same
	// reason.
	answerer Answerer

	// attention is the undispatched fact set and its two routes out
	// (nocx-dkawo.3). It is never nil: an unwired one still records every
	// fact and says at Error that it has nothing to reach anyone with, which
	// is the same stance the supervisor takes with an unwired destination.
	attention *Backstop

	// log is what the six steps of Register say they are doing. It is never
	// nil, and a Registrar built without one writes to slog's default rather
	// than to nothing: the failure that bought this field (nocx-4l2a5.4) was
	// thirty seconds of silence between "worker participant spawned" and
	// "enrolment never arrived", in which the record could not say which step
	// was waiting or for how long.
	log log.Logger
}

// Option configures a Registrar. The two numbers are injected rather than
// constant because a deadline that is wrong in either direction breaks the
// procedure, and the composition root is where the product's real values live.
type Option func(*Registrar)

// WithBound sets how many non-terminal participants one worker may hold.
func WithBound(n int) Option { return func(r *Registrar) { r.bound = n } }

// WithLogger gives the record the composition root's logger, so its steps land
// in the same file as everything else that serves the same call.
func WithLogger(lg log.Logger) Option {
	return func(r *Registrar) {
		if lg != nil {
			r.log = lg
		}
	}
}

// WithEnrolmentDeadline bounds step 4. Its VALUE is not decided here — it is
// measured where fan-out is measured — but the procedure needs both ends of
// the interval named, and this is the closing one for an enrolment that never
// arrives.
func WithEnrolmentDeadline(d time.Duration) Option {
	return func(r *Registrar) { r.deadline = d }
}

// WithCloser wires the seam that ends a participant. Without it Close
// refuses and says so, rather than reporting a worker ended that is still
// running.
func WithCloser(c Closer) Option { return func(r *Registrar) { r.closer = c } }

// WithScreener wires the seam that reads a participant's pane. Without it
// Screen refuses and says so, rather than answering an empty screen that a
// coordinator would read as a pane with nothing on it.
func WithScreener(s Screener) Option { return func(r *Registrar) { r.screener = s } }

// WithAnswerer wires the seam that answers a participant's menu. Without it
// Answer refuses and says so.
func WithAnswerer(a Answerer) Option { return func(r *Registrar) { r.answerer = a } }

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
		log:      log.NewSlogAdapter(nil),
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
	Group              ID
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

// Registration is what Register hands back: the participant it left behind,
// and what became of the participant's task at spawn.
//
// Participant is embedded so a caller that only ever asked for the record
// reads it exactly as before. Delivery rides beside it rather than inside it
// for TaskDelivery's own reason: it is answered once, to the caller that
// registered, and it is not the record's.
type Registration struct {
	Participant
	Delivery TaskDelivery
}

// Register runs the six steps above.
//
// It returns the participant it left behind even on failure, because a caller
// that cannot name the record cannot check what happened to it — and "a
// registration that failed" is exactly when naming it matters.
func (r *Registrar) Register(ctx context.Context, req RegisterRequest) (_ Registration, err error) {
	ctx, lg, end := log.Start(ctx, r.log, "workers.register",
		"group", string(req.Group), "coordinator_session", req.CoordinatorSession,
		"role", string(req.Role), "command", req.Command)
	defer func() { end(err) }()
	// The worker exists because a coordinator spawned into it, never because
	// somebody opened one: a coordinator's first spawn is what a worker IS.
	// Its id defaults to the coordinator's session, which is the identity D3
	// answers by — one coordinator session, one worker — so nothing has to
	// carry a second name for the same thing.
	if req.Group == "" {
		req.Group = ID(req.CoordinatorSession)
	}
	if err := r.store.EnsureGroup(ctx, req.Group, req.CoordinatorSession); err != nil {
		return Registration{}, fmt.Errorf("worker: ensure: %w", err)
	}
	// Step 1. Nothing is forked and no record exists, so a refusal is free.
	held, reserveErr := r.store.NonTerminal(ctx, req.Group)
	if reserveErr != nil {
		return Registration{}, fmt.Errorf("worker: reserve: %w", reserveErr)
	}
	if len(held) >= r.bound {
		return Registration{}, fmt.Errorf("worker %q holds %d of %d: %w", req.Group, len(held), r.bound, ErrBoundExceeded)
	}

	// Step 2. From here on there is a record, and every later failure
	// terminalizes it rather than leaving it to be found as a live worker.
	p := Participant{
		ID:           r.newID(),
		Group:        req.Group,
		Role:         req.Role,
		State:        StatePrepared,
		Task:         req.Task,
		RegisteredAt: r.now(),
	}
	if err := r.store.CommitPrepared(ctx, p); err != nil {
		return Registration{}, fmt.Errorf("worker: commit prepared: %w", err)
	}

	// Steps 3 and 4 share ONE deadline, not two (nocx-ui8q6.4). A launcher
	// that never enrols must not hold the record open forever, and that bound
	// has to cover the fork AND the wait for its enrolment together: Spawn
	// itself now blocks inside the fork for the pane's shell-integration axis
	// to answer (a domain that has not finished establishing refuses the
	// agent's own enrolment call, ErrDomainPending — the race this bead
	// closes), and a spawn that spent the whole deadline there would leave
	// Await nothing to wait with. r.deadline is that one bound, applied once,
	// here, before either step starts — never a second, smaller number
	// invented for Spawn's own wait, which would only be a third bound for an
	// interval that already has one.
	// The line says what it now precedes. It used to sit after the fork and
	// read "waiting for the participant's enrolment", which was true there
	// and is not true here: at this point nothing has been forked yet. A log
	// line that describes the step after next is the kind of small lie that
	// costs an hour when somebody reads the sequence back — this bead exists
	// because "cause=hello-timeout" said the shell had not answered while the
	// shell had answered.
	lg.Info("worker: the participant's budget opens",
		"deadline_ms", r.deadline.Milliseconds(),
		"covers", "spawn and enrolment")
	awaitCtx, cancel := context.WithTimeout(ctx, r.deadline)
	defer cancel()

	// Step 3. The id is already minted and travels with the request, so a
	// launcher that fails to connect has registered nothing under a name we
	// did not choose.
	lg = lg.With("participant", string(p.ID))
	spawned, spawnErr := r.spawn.Spawn(awaitCtx, SpawnRequest{
		Participant:        p.ID,
		Group:              req.Group,
		CoordinatorSession: req.CoordinatorSession,
		Task:               req.Task,
		Command:            req.Command,
		Environment:        req.Environment,
	})
	if spawnErr != nil {
		return Registration{Participant: p}, r.compensate(ctx, p, nil, false, fmt.Errorf("worker: spawn: %w", spawnErr))
	}

	// Step 4. The bound closes the interval; it does not decide anything
	// about the participant.
	// NAMED, because this is the step that was silent. A launcher that never
	// enrols spends the whole deadline here, and until this line the log said
	// only that a pane had been opened and, half a minute later, that the
	// registration had failed.
	// time.Now and not r.now: r.now is the RECORD's clock, the one that stamps
	// a participant's RegisteredAt and that a test freezes to make records
	// comparable. A stopwatch on a frozen clock measures nothing, and this
	// number's whole job is to say how long the wait actually was.
	waitStarted := time.Now()
	live, enrolErr := r.enrol.Await(awaitCtx, p.ID)
	if enrolErr != nil {
		lg.Warn("worker: the enrolment never arrived",
			"waited_ms", time.Since(waitStarted).Milliseconds(),
			"deadline_ms", r.deadline.Milliseconds(), "error", enrolErr)
		return Registration{Participant: p}, r.compensate(ctx, p, spawned, false, fmt.Errorf("worker: await enrolment: %w", enrolErr))
	}
	lg.Debug("worker: the participant enrolled",
		"waited_ms", time.Since(waitStarted).Milliseconds(),
		"session_id", live.SessionID, "lane", live.Lane, "epoch", live.Epoch)

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
		return Registration{Participant: p}, r.compensate(ctx, p, spawned, true, fmt.Errorf("worker: delegation: %w", err))
	}

	// Step 6, and the order inside it is the point.
	if err := r.store.MarkLive(ctx, p.ID, live); err != nil {
		return Registration{Participant: p}, r.compensate(ctx, p, spawned, true, fmt.Errorf("worker: mark live: %w", err))
	}
	p.State = StateLive
	p.Liveness = live
	if err := r.sup.Attach(ctx, p); err != nil {
		return Registration{Participant: p}, r.compensate(ctx, p, spawned, true, fmt.Errorf("worker: attach supervision: %w", err))
	}
	// What became of the task travels OUT with the registration and is never
	// written into the record (nocx-f545a.3): a task left untyped because the
	// pane was asking a question is a screen reading, and a screen reading
	// may not assign status to a participant (ADR-0064 §4). A Spawned that
	// attempted no delivery says nothing, which is the zero value.
	reg := Registration{Participant: p}
	if d, ok := spawned.(TaskDeliverer); ok {
		reg.Delivery = d.TaskDelivery()
	}
	return reg, nil
}

// compensate undoes what was built, in the reverse order of building it, and
// returns cause. It never replaces cause with its own error: what the caller
// needs to know is why the registration failed, and a compensation that also
// failed is a fact about the record, which the record itself then carries by
// staying non-terminal.
func (r *Registrar) compensate(ctx context.Context, p Participant, spawned Spawned, enrolled bool, cause error) error {
	// SAID, because an undo nobody can see is how a participant that outlived
	// its own registration goes unnoticed: nocx-1w3my recorded a call that
	// reported failure and left a participant behind.
	r.log.WithContext(ctx).Info("worker: compensating a failed registration",
		"participant", string(p.ID), "withdraw_enrolment", enrolled,
		"kill_launcher", spawned != nil, "cause", cause)
	if enrolled {
		if err := r.enrol.Withdraw(ctx, p.ID); err != nil {
			return errors.Join(cause, fmt.Errorf("worker: withdraw enrolment: %w", err))
		}
	}
	if spawned != nil {
		if err := spawned.Kill(ctx); err != nil {
			return errors.Join(cause, fmt.Errorf("worker: kill launcher: %w", err))
		}
	}
	if err := r.store.Terminalize(ctx, p.ID, StateInterrupted); err != nil {
		// A failure is never a verdict: the record stays non-terminal
		// rather than claiming a state this pass did not establish. Nothing
		// closes it afterwards — the participant it describes dies with this
		// backend, and so does the record.
		return errors.Join(cause, fmt.Errorf("worker: terminalize: %w", err))
	}
	return cause
}

// Screen reads what a participant's pane is showing, for the session that
// holds it (nocx-f545a.6, ADR-0064 §2).
//
// It asks the two questions Close asks and in the same order, because they
// are the same authority question about the same participant: is it this
// session's (ErrNotHeld when not), and does the delegation still permit the
// act (ErrNotDelegated when not). The act here is EffectObserve, which a human
// takeover leaves in place — a person helping a worker past a prompt does not
// blind its coordinator to the prompt.
//
// A participant that has already ended has no pane to read, and that is an
// answer rather than an error: the screener is not asked, and the screen says
// it is not readable.
//
// It writes nothing, and nothing it reads is kept.
func (r *Registrar) Screen(ctx context.Context, coordinatorSession string, id ParticipantID) (PaneScreen, error) {
	if r.screener == nil {
		return PaneScreen{}, errors.New("worker: this backend cannot read a participant's pane")
	}
	del, err := r.store.Delegation(ctx, id)
	if err != nil {
		return PaneScreen{}, err
	}
	if del.ControllerSession != coordinatorSession {
		return PaneScreen{}, fmt.Errorf("worker: participant %q is held by another session: %w", id, ErrNotHeld)
	}
	p, err := r.store.Participant(ctx, id)
	if err != nil {
		return PaneScreen{}, err
	}
	if p.State.Terminal() {
		return PaneScreen{}, nil
	}
	if !del.Permits(EffectObserve) {
		return PaneScreen{}, fmt.Errorf("worker: participant %q, delegation is %s: %w", id, del.State, ErrNotDelegated)
	}
	return r.screener.ReadScreen(ctx, p)
}

// Answer answers a participant's menu by naming one of its options
// (nocx-f545a.4, ADR-0064 §1).
//
// The authority questions are Close's and Screen's, in their order — is it
// this session's (ErrNotHeld), does the delegation still permit the act
// (ErrNotDelegated) — and the act is EffectSendInput: answering a menu is
// putting keys into the pane, and that is exactly what a human takeover
// suspends. A person at the worker's keyboard is answering it themselves.
//
// A participant that has ended has no menu, and unlike a screen that is not an
// answer: a coordinator that tried to answer something is told there was
// nothing there, with ErrTerminal.
//
// It writes nothing into the record. What the answer causes reaches the record
// the way any fact does.
func (r *Registrar) Answer(ctx context.Context, coordinatorSession string, id ParticipantID, option string) (PaneAnswer, error) {
	if r.answerer == nil {
		return PaneAnswer{}, errors.New("worker: this backend cannot answer a participant's menu")
	}
	del, err := r.store.Delegation(ctx, id)
	if err != nil {
		return PaneAnswer{}, err
	}
	if del.ControllerSession != coordinatorSession {
		return PaneAnswer{}, fmt.Errorf("worker: participant %q is held by another session: %w", id, ErrNotHeld)
	}
	p, err := r.store.Participant(ctx, id)
	if err != nil {
		return PaneAnswer{}, err
	}
	if p.State.Terminal() {
		return PaneAnswer{}, fmt.Errorf("worker: participant %q has ended, so there is no menu to answer: %w", id, ErrTerminal)
	}
	if !del.Permits(EffectSendInput) {
		return PaneAnswer{}, fmt.Errorf("worker: participant %q, delegation is %s: %w", id, del.State, ErrNotDelegated)
	}
	return r.answerer.Answer(ctx, p, option)
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
		return cur, fmt.Errorf("worker: participant %q: %w", id, ErrStaleEvidence)
	}
	// A record that is already terminal takes no more facts, with exactly one
	// exception: abandoned is the half-answered state, and the declaration
	// that completes the conjunction may still arrive. Interrupted is not
	// that — it was written by our own compensation or by the restart sweep,
	// so a fact arriving against it is evidence about a process we already
	// established is not ours to judge.
	if cur.State.Terminal() && cur.State != StateAbandoned {
		return cur, fmt.Errorf("worker: participant %q is %s: %w", id, cur.State, ErrTerminal)
	}
	after, err := record(ctx)
	if err != nil {
		return Participant{}, err
	}
	want := reduce(after)
	if want != after.State {
		if err := r.store.Terminalize(ctx, id, want); err != nil {
			return after, fmt.Errorf("worker: terminalize %q: %w", want, err)
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
// situation a coordinator exists to handle, and holding it until the worker is
// finished would report a crash after the work that depended on it.
//
// A fact whose participant succeeded needs judgement only when NOTHING ELSE
// IS RUNNING. "A worker finished with two still running" is routine — the
// coordinator is waiting on all of them and has nothing to decide yet — and
// waking it costs a turn to be told what it already expects. When the last
// one lands, the worker is finished and nobody has read it, and that is the
// moment the coordinator is for.
//
// # Why the worker's remaining work and not the fact alone
//
// The same fact means different things at different moments, and the record
// is the only thing that knows which moment it is. A design that classified
// facts in isolation would have to choose between waking on every completion
// (which is the poll this whole mechanism replaced) and waking on none
// (which loses the end of the worker). Nothing on a screen takes part in this:
// what is read is the participant rows.
func (r *Registrar) route(ctx context.Context, p Participant, kind FactKind) {
	f := Fact{
		Participant: p.ID,
		Group:       p.Group,
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
	// the worker may hold nothing non-terminal.
	coordinator, err := r.store.CoordinatorSession(ctx, p.Group)
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
	open, err := r.store.NonTerminal(ctx, p.Group)
	if err != nil {
		// A read that failed is not evidence that the worker is finished, and
		// it is not evidence that it is not. Judgement is the fail-closed
		// direction: a fact the coordinator did not need costs it one turn,
		// and a fact it never learns about costs it the workers.
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
// depends on the rest of the worker rather than on it. Abandoned, failed and
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
// behind "a worker with no undispatched facts wakes nobody": an empty answer
// and no armed alarm are the same statement.
func (r *Registrar) Undispatched() []Fact { return r.attention.Open() }

// ── the mailbox (nocx-dkawo.11) ───────────────────────────────────────────

// Say commits one message into a participant's mailbox.
//
// The sender is stamped by the caller's authenticated identity and never
// carried in the call, because a sender a caller could name is a sender a
// caller could forge. What is checked is MEMBERSHIP and nothing else: both
// ends must be in the worker. A delegation is deliberately not consulted —
// membership makes a participant addressable and delegation makes it
// controllable, and a human takeover that suspends control must not also stop
// the coordinator writing to its own worker.
func (r *Registrar) Say(ctx context.Context, worker ID, from, to ReaderID, body string) (Message, error) {
	switch {
	case body == "":
		return Message{}, ErrEmptyMessage
	case len(body) > MaxMessageBytes:
		return Message{}, fmt.Errorf("worker: %d bytes exceeds %d: %w",
			len(body), MaxMessageBytes, ErrMessageTooLarge)
	}
	if err := r.member(ctx, worker, to); err != nil {
		return Message{}, fmt.Errorf("worker: recipient %q: %w", to, err)
	}
	if err := r.member(ctx, worker, from); err != nil {
		return Message{}, fmt.Errorf("worker: sender %q: %w", from, err)
	}
	return r.store.Commit(ctx, Message{
		Group: worker, Recipient: to, Sender: from,
		Body: body, CommittedAt: r.now(),
	})
}

// member reports whether an id may take part in this worker's mail.
//
// Two ways in, because the worker has two kinds of node and they are named
// differently on purpose: a worker is its PARTICIPANT id, which outlives every
// run it makes, and a coordinator is its SESSION (AD-7), which is what makes a
// restarted coordinator the same reader — the property D3 already rests on.
func (r *Registrar) member(ctx context.Context, worker ID, who ReaderID) error {
	coordinator, err := r.store.CoordinatorSession(ctx, worker)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	if ReaderID(coordinator) == who {
		return nil
	}
	p, err := r.store.Participant(ctx, ParticipantID(who))
	if err != nil {
		if errors.Is(err, ErrNoSuchParticipant) {
			return ErrNotAMember
		}
		return err
	}
	if p.Group != worker {
		return ErrNotAMember
	}
	return nil
}

// ParticipantOf names the participant running in one session, for the caller
// that has a session and needs the participant: the endpoint authorizer, which
// establishes a peer's session from its process tree and must then decide
// whether that session is a worker's rather than a coordinator's.
//
// It is a passthrough and deliberately not more. Nothing is decided here about
// what that participant may do — that is the grant the authorizer mints and
// the capability the declaration narrows to — because a record that also
// answered "and may it" would be the second owner of an authority question
// this design keeps in one place.
func (r *Registrar) ParticipantOf(ctx context.Context, sessionID string) (Participant, error) {
	return r.store.ParticipantBySession(ctx, sessionID)
}

// Inbox hands a reader the next page of a mailbox and advances ITS OWN
// fetched mark, and nobody else's.
//
// This is the whole of "a read takes nothing": the messages stay where they
// are, and what moved is one row belonging to one reader. A second reader
// asking the same question is answered the same way from its own position.
func (r *Registrar) Inbox(ctx context.Context, mailbox, reader ReaderID, limit int) (Fetch, error) {
	if limit <= 0 || limit > MaxFetch {
		limit = MaxFetch
	}
	cur, err := r.store.Cursor(ctx, mailbox, reader)
	if err != nil {
		return Fetch{}, fmt.Errorf("worker: cursor: %w", err)
	}
	// One more than the page, so "is there more" is answered by what the
	// store returned rather than by a second count that could disagree with
	// it between the two reads.
	page, err := r.store.Since(ctx, mailbox, cur.Fetched, limit+1)
	if err != nil {
		return Fetch{}, fmt.Errorf("worker: read mailbox: %w", err)
	}
	out := Fetch{Cursor: cur}
	if len(page) > limit {
		out.More = true
		page = page[:limit]
	}
	out.Messages = page
	if len(page) == 0 {
		return out, nil
	}
	cur.Fetched = page[len(page)-1].Seq
	cur.UpdatedAt = r.now()
	if err := r.store.AdvanceCursor(ctx, cur); err != nil {
		// The messages were handed out and the mark did not move. Say so
		// rather than returning them: a reader that believed it had a
		// position it does not have would acknowledge past mail it never
		// saw, and losing a fetch costs one repeat while losing a message
		// costs the worker.
		return Fetch{}, fmt.Errorf("worker: advance cursor: %w", err)
	}
	out.Cursor = cur
	return out, nil
}

// Acknowledge records that a reader finished committing the effects of
// everything through a sequence.
//
// It is the mark that makes a retry safe. "Read consumes nothing" prevents
// loss and does not prevent duplication: a reader handed a message twice
// would spawn twice, and this is what tells the record it need not be.
//
// It never moves backwards, and it never moves past what was fetched — an
// acknowledgement of mail nobody was handed is a claim about something that
// did not happen.
func (r *Registrar) Acknowledge(ctx context.Context, mailbox, reader ReaderID, through int64) error {
	cur, err := r.store.Cursor(ctx, mailbox, reader)
	if err != nil {
		return fmt.Errorf("worker: cursor: %w", err)
	}
	if through > cur.Fetched {
		return fmt.Errorf("worker: cannot acknowledge %d of mailbox %q: only %d has been fetched",
			through, mailbox, cur.Fetched)
	}
	if through <= cur.Acted {
		return nil
	}
	cur.Acted = through
	cur.UpdatedAt = r.now()
	return r.store.AdvanceCursor(ctx, cur)
}

// Undelivered is what a worker's mailboxes hold that their own recipients have
// not taken. It is reported as itself and never as a delivery.
func (r *Registrar) Undelivered(ctx context.Context, worker ID) ([]Message, error) {
	return r.store.Undelivered(ctx, worker)
}

// ── waiting, and ending (nocx-dkawo.13) ───────────────────────────────────

// Wait blocks until something about this session's participants changes, then
// answers exactly what HeldBy answers.
//
// It is a CONVENIENCE OVER THE RECORD and nothing rests on it (§7.2). The
// backend watches the workers whether or not this is ever called; a
// coordinator that never waits loses its own promptness and nothing else.
// That is the whole difference from the blocking call and then the lease this
// design started with, and it has to stay true — a wait anything depended on
// would be the lease back under a friendlier name.
//
// It answers with HeldBy's own answer rather than a delta of its own. "Which
// one settled" is read from the states, and a second shape for it would be a
// second account of what a session holds.
//
// The channel is taken BEFORE the first read, so a fact admitted between the
// read and the select has already closed the channel this is holding. Without
// that order the wait would miss exactly the event it was opened for.
func (r *Registrar) Wait(ctx context.Context, coordinatorSession string, worker ID) ([]Participant, error) {
	if worker == "" {
		worker = ID(coordinatorSession)
	}
	for {
		changed := r.attention.Changed(worker)
		// Owed is read BEFORE the fetch, because the fetch is what clears
		// it: asking afterwards would always answer nothing, and the wait
		// would sit through the one thing it exists to catch.
		owed := r.attention.Owed(worker)
		held, err := r.HeldBy(ctx, coordinatorSession)
		if err != nil {
			return nil, err
		}
		// Nothing to wait FOR is not a reason to wait: a session that holds
		// nothing, one whose worker has settled, or one that already owes
		// judgement is answered at once. Blocking on the last of those would
		// be two mechanisms disagreeing about one moment — the routing table
		// has already decided the coordinator is needed and woken it.
		if answerable(held, owed) {
			return held, nil
		}
		select {
		case <-changed:
			// Round again rather than answering from here: what closed the
			// channel is one fact, and the caller asked what its session
			// HOLDS, which is a read of the record and not of that fact.
		case <-ctx.Done():
			// An expired wait is an ANSWER and not a failure. The
			// coordinator asked to be told promptly and was not; what it
			// holds is still true, and returning it is more useful than an
			// error it would have to translate back into the same read.
			return held, nil
		}
	}
}

// answerable reports whether the wait has something to answer with.
//
// Three ways, and the third is the one that is easy to leave out. A session
// that holds nothing has nothing to wait for. A participant that has SETTLED
// is what the criterion names. And a fact that needs JUDGEMENT is a
// coordinator that the routing table has already decided is needed — a worker
// that reported failure and is still running is exactly that, and a wait that
// sat through it while the wake fired would be two mechanisms disagreeing
// about one moment.
func answerable(held []Participant, owed int) bool {
	if len(held) == 0 || owed > 0 {
		return true
	}
	for _, p := range held {
		if p.State.Terminal() {
			return true
		}
	}
	return false
}

// Close ends a participant, and it is the first operation that reads a
// DELEGATION rather than membership.
//
// Membership makes a worker addressable — that is what mail is checked
// against — and delegation makes it controllable. Until this, EffectClose sat
// in DefaultBundle and nothing had ever consulted it, which made "membership
// is not delegation" a comment rather than a mechanism. A human takeover
// suspends send-input and leaves close alone, and DelegationState.Permits
// already says so; this is where that stops being theoretical.
//
// It writes NO state. Ending the session produces a process exit, and that
// exit reaches the record by the ordinary path and reduces the participant the
// way any exit does. A close that also terminalized would be a second author
// of a participant's state, and the two would disagree the first time a
// worker declared between the kill and the write.
func (r *Registrar) Close(ctx context.Context, coordinatorSession string, id ParticipantID) error {
	if r.closer == nil {
		return errors.New("worker: this backend cannot end a participant")
	}
	del, err := r.store.Delegation(ctx, id)
	if err != nil {
		return err
	}
	if del.ControllerSession != coordinatorSession {
		return fmt.Errorf("worker: participant %q is held by another session: %w", id, ErrNotHeld)
	}
	p, err := r.store.Participant(ctx, id)
	if err != nil {
		return err
	}
	// OWNERSHIP IS THE AUTHORITY QUESTION, AND IT IS ANSWERED ABOVE. The
	// delegation's STATE is not a second authority to end something that is
	// already over: this check used to run here and refuse first, so the
	// branch below — "already finished, not an error, a coordinator tidying
	// up" — could never be reached for the case it was written for, and a
	// worker that ended badly stayed in holdings for ever with no way to
	// close it (nocx-e5e8q).
	//
	// So the state gates only a LIVE participant. Ending one that is already
	// terminal needs no live delegation, because there is nothing left to
	// act on.
	if !p.State.Terminal() && !del.Permits(EffectClose) {
		return fmt.Errorf("worker: participant %q, delegation is %s: %w", id, del.State, ErrNotDelegated)
	}
	if p.State.Terminal() {
		// Already finished. Not an error: a coordinator tidying up should
		// not have to have raced the record to be allowed to.
		return nil
	}
	return r.closer.Close(ctx, p)
}
