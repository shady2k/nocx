package app

// The composition root's half of the worker record (nocx-dkawo.6, nocx-dkawo.7).
//
// internal/worker owns the semantics and names four seams it cannot satisfy
// itself: what a session IS, what a pane IS, when an enrolment arrived and
// when a process is gone. All four are answers this file already holds,
// because the composition root is the one place the layout chain, the session
// opener, the lifecycle enroller and the session registry meet.
//
// THE TWO FACTS AND NOTHING ELSE. What decides a participant's state here is
// its process exit and its own declaration, exactly as D9 says. The grid is in
// this same file's neighbourhood and is never consulted: it decides whether
// nocx may type into a pane and what the indicator shows, and a worker state
// derived from a screen is the self-matching sentinel this design exists to
// kill.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
)

// sessionOpenerSeam is the app's narrow view of the one session-open path
// (nocx-dkawo.6). It is an interface and not the server because what a spawn
// needs is one sentence — open a session for this pane — and a handler, a
// broker and forty other methods are not part of it.
type sessionOpenerSeam interface {
	OpenSession(ctx context.Context, spec transport.OpenSpec) (transport.OpenedSession, error)
}

// paneMinter is the spawner's narrow view of the layout chain (AD-8): mint a
// tab and its first pane, and undo that mint. A participant needs exactly
// those two calls, and the twenty-odd other things a layout repository can
// do — reordering strips, recolouring workspaces, clearing the window — are
// not things a spawn may reach for.
//
// DeleteTab joined CreateTab here at nocx-ui8q6.4, closing a hole that
// predated this bead: a session that failed to open, or a queue write that
// was refused, left the tab CreateTab had just minted standing in the store
// with nothing behind it — a person would see a tab and find it dead. Every
// failure after CreateTab now compensates through this same seam.
type paneMinter interface {
	CreateTab(ctx context.Context, tab content.Tab, firstPane content.Pane) (content.Created[content.NewTab], error)
	DeleteTab(ctx context.Context, id string, next content.Replacement) error
}

// sessionCloser ends a session by id. The registry's own Close, named as the
// one thing a compensation needs.
type sessionCloser interface {
	Get(id session.ID) (session.Session, error)
	Close(id session.ID) error
}

// integrationAwaiterSeam is the spawner's narrow view of the transport's
// shell-integration axis (nocx-ui8q6.4): tell me when this pane's shell has
// answered. It is a second sentence beside sessionOpenerSeam's, in the same
// discipline — one verb, no other method of *transport.WSServer reachable
// through it — because what a spawn needs to know is not "what is this
// session's status" in general (that question has readers of its own on the
// wire) but only "has it left `starting` yet, or should I keep waiting".
type integrationAwaiterSeam interface {
	AwaitIntegration(ctx context.Context, sid session.ID) (transport.IntegrationOutcome, error)
}

// participantGeometry is the size a participant's pane opens at.
//
// It is a constant and not a setting because nobody is looking at this pane
// when it opens: a worker participant is spawned by the backend, and the first
// client to attach reports its own geometry and the session resizes. What this
// number has to be is big enough that an agent TUI's first repaint is not
// wrapped into nonsense before anyone sees it.
const (
	participantCols = 120
	participantRows = 40
)

// workerSpawner mints the pane a participant lives in and opens its session.
//
// The pane is a TAB of its own rather than a split, because a participant is a
// thing a person switches to and closes, and because CreateTab is the one call
// that mints a container together with its first member — a pane created
// without one would be a member of nothing.
type workerSpawner struct {
	layout   paneMinter
	opener   sessionOpenerSeam
	sessions sessionCloser
	// integration is asked, once per spawn, whether the pane's shell ever
	// answered (nocx-ui8q6.4). Nil is treated the same as a session the axis
	// never registered — proceed, nothing to wait for — never as licence to
	// skip the question and guess the answer is yes: see the ABSENCE branch
	// in Spawn for why that reading is safe rather than a hole.
	integration integrationAwaiterSeam
	// enrolments is told the participant → session mapping at spawn, because
	// an enrolment arrives naming a SESSION and the record is keyed by
	// participant. Telling it here rather than deriving it later keeps one
	// owner of that mapping and keeps it correct before the first frame.
	enrolments *workerEnrolments
	// readiness answers what a pane was last classified as (nocx-66gd0): the
	// gate for KNOWING when the participant's TUI has a prompt up, so the
	// task is typed only once there is one to type into rather than blind.
	// Nil is treated exactly like integration's absence above — a caller with
	// no observation wired at all was never asking the question, so deliverTask
	// lets the spawn through rather than hanging on an answer that will never
	// come. Production always wires the real watcher; only a test double built
	// to exercise the axis gate or the tab bookkeeping alone leaves it nil.
	readiness paneReadiness
	// typist is what actually puts the task into the pane, once readiness
	// says it may. The SAME Typist agent.type and the coordinator's own wake
	// reach (workerWaker below) — a second one would be a second answer to
	// "may nocx write into this pane", decided against a second grid.
	typist paneTypist
	// workspace is where a participant's tab is minted. The worker's own
	// workspace, resolved by the caller, never guessed here.
	workspace string
	log       log.Logger
}

// paneReadiness is the spawner's narrow view of the pane-observation watcher
// (AD-8): what has this pane last been classified as. One method, because
// delivery needs to KNOW a state, never to classify one itself — that
// decision belongs to internal/paneobserve, on a sweep it already runs for
// every enrolled pane.
type paneReadiness interface {
	Snapshot(paneID string) (paneobserve.Observation, bool)
}

// deliveryPoll is how often deliverTask re-asks paneReadiness while it waits
// for a pane to become typable.
//
// It is not a business number, and no test in this repository depends on it:
// the watcher itself is fed by a 120ms coalescer
// (internal/transport/ws_paneobserve.go's paneObserverSweep), so asking more
// often than that buys nothing, and this is comfortably under it so the wait
// notices a transition within one or two sweeps rather than missing a whole
// one. A test drives the watcher's state directly and asserts on the change,
// never on this duration.
const deliveryPoll = 40 * time.Millisecond

// awaitFreeText blocks until paneID's last-classified observation is
// free_text, until ctx ends, or until the pane's agent is seen to have
// exited.
//
// It asks the WATCHER rather than retrying agenttyping.Submit in a loop: a
// refused Submit call would read the live screen and log a warning on every
// attempt, where the watcher already tracks exactly this state for free, off
// the same sweep the product's own indicator uses. Submit is still called
// exactly once, after this returns, and it RE-VERIFIES the screen itself
// (agenttyping's own documented guarantee) — this function only decides when
// that one attempt is worth making, never whether it is allowed to write.
func awaitFreeText(ctx context.Context, r paneReadiness, paneID string) error {
	check := func() (bool, error) {
		o, ok := r.Snapshot(paneID)
		if !ok {
			return false, nil
		}
		switch o.State {
		case agentdriver.StateFreeText:
			return true, nil
		case agentdriver.StateExited:
			return false, errors.New("the participant's agent exited before its pane ever became typable")
		default:
			return false, nil
		}
	}
	if ready, err := check(); ready || err != nil {
		return err
	}
	ticker := time.NewTicker(deliveryPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("the pane never became typable inside the spawn's own budget")
		case <-ticker.C:
			if ready, err := check(); ready || err != nil {
				return err
			}
		}
	}
}

// spawnedParticipant is a launcher that has been started. It is not yet a
// participant: nothing may be addressed until its enrolment arrives.
type spawnedParticipant struct {
	sess     session.Session
	sessions sessionCloser
}

func (s spawnedParticipant) Liveness() workers.Liveness {
	ident := s.sess.Identity()
	return workers.Liveness{
		BackendInstance: string(ident.InstanceID),
		SessionID:       string(s.sess.ID()),
		Epoch:           ident.Epoch,
		// Lane is filled by the ENROLMENT, which is what arrives on an
		// authenticated channel. Naming one here would be claiming the shell
		// spoke before it did.
		Attempt: 1,
	}
}

// Kill is the compensation for every failure after the fork, and it is
// available synchronously — which is why the register procedure needs no
// journal.
func (s spawnedParticipant) Kill(context.Context) error {
	return s.sessions.Close(s.sess.ID())
}

// Spawn mints the pane, opens the session, waits for its shell to answer,
// and only then writes the participant's command line.
//
// THE ORDER IS THE ROLLBACK, extended by one step at nocx-ui8q6.4. The pane
// row is written first, so a session that fails to open leaves a pane a
// person can see and close; the session is opened second, so nothing is
// spawned for a pane that was never recorded; and every failure from here on
// — the axis never resolving, resolving to `conventional`, or the queue
// write itself refusing — compensates BOTH of those in reverse, through
// compensateSpawn below. Before this bead only the session was undone: a
// session that failed to open, or a queue write that was refused, left the
// tab CreateTab had just minted standing in the store with nothing behind
// it. That was already true and is fixed here rather than filed separately,
// because this change is what turns it from a rare race into the common
// path a refused spawn now takes.
//
// THE RACE THIS CLOSES. The agent's command line is written into the
// session's own input queue — the same queue a person's keystrokes take —
// and NOT through internal/agenttyping, whose refusal exists for a
// mistimed keystroke into a running agent TUI with a modal already on
// screen; here there is no TUI yet. What used to make the write "safe" was
// only the INTERVAL argument — a command line that never runs produces no
// enrolment, and a registration whose enrolment never arrives is
// terminalized — and that argument covers the agent never starting, not the
// agent starting and being refused its own enrolment. The agent wrapper
// enrols over the SAME authenticated lifecycle channel the shell's own hello
// establishes, and the kernel refuses an enrolment while that domain's
// accept is still pending (lifecycle.kernel's requireActive, ErrDomainPending).
// nocx-ui8q6.2 made the backend flush that accept on its own authority, so
// the domain no longer stays pending forever — but flushing still takes on
// the order of tens of milliseconds, and the command used to be written
// microseconds after OpenSession returned. Waiting here for the axis to
// leave `starting` is waiting for exactly the fact that says the domain is
// past that window: `integrated` cannot be reported before the domain the
// axis borrows its truth from (AD-8) is established.
//
// THE BOUND. This call's ctx is expected to already carry the enrolment
// deadline (internal/workers.Registrar.Register wraps its call to Spawn and
// its later Await of the enrolment in one shared context.WithTimeout, using
// its own r.deadline — workerEnrolmentDeadline in production — precisely so
// this wait is INSIDE that budget and not a second one beside it). No smaller
// timeout is applied here: in the pathological case where a shell never
// answers at all, lifecycle.HelloTimeout bounds the shell's own handshake
// and the resulting loss report moves the axis to `conventional` well
// inside the enrolment deadline, so this wait resolves on its own long
// before ctx would. A third, invented bound would only be a second answer to
// a question the enrolment deadline already answers.
func (s *workerSpawner) Spawn(ctx context.Context, req workers.SpawnRequest) (_ workers.Spawned, err error) {
	ctx, lg, end := log.Start(ctx, s.log, "worker.spawn",
		"participant", string(req.Participant), "group", string(req.Group),
		"command", req.Command)
	defer func() { end(err) }()

	tabID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("worker spawn: minting a tab id: %w", err)
	}
	paneID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("worker spawn: minting a pane id: %w", err)
	}
	if _, tabErr := s.layout.CreateTab(ctx,
		content.Tab{ID: tabID.String(), WorkspaceID: s.workspace, Layout: content.LayoutRow},
		content.Pane{ID: paneID.String(), TabID: tabID.String(), Kind: content.PaneLocal, SizeShare: 1},
	); tabErr != nil {
		return nil, fmt.Errorf("worker spawn: minting the participant's tab: %w", tabErr)
	}
	lg.Debug("worker spawn: the participant's tab exists",
		"tab_id", tabID.String(), "pane_id", paneID.String(), "workspace", s.workspace)

	opened, err := s.opener.OpenSession(ctx, transport.OpenSpec{
		PaneID: paneID.String(),
		Cols:   participantCols,
		Rows:   participantRows,
	})
	if err != nil {
		s.compensateSpawn(ctx, tabID.String(), nil)
		return nil, fmt.Errorf("worker spawn: opening the participant's session: %w", err)
	}
	lg = lg.With("session_id", string(opened.Session.ID()), "pane_id", paneID.String())
	lg.Debug("worker spawn: the participant's session is open",
		"cols", participantCols, "rows", participantRows)
	spawned := spawnedParticipant{sess: opened.Session, sessions: s.sessions}

	// Told BEFORE the command is written, or an enrolment that arrives
	// promptly would find nobody waiting for it.
	s.enrolments.expect(req.Participant, opened.Session.ID())

	// THE GATE. Nothing is written into the session's queue — and so the
	// agent never execs and never races its own enrolment — until the axis
	// says the domain behind it either can, or never will, accept one.
	//
	// A nil seam is treated exactly like AwaitIntegration's own "absence"
	// answer below rather than as a third, special case: a spawner nobody
	// wired an axis into cannot ask the question any more than a session that
	// never registered on one can answer it, and the two must not diverge in
	// what they let through.
	var outcome transport.IntegrationOutcome
	var awaitErr error
	if s.integration != nil {
		outcome, awaitErr = s.integration.AwaitIntegration(ctx, opened.Session.ID())
	}
	switch {
	case awaitErr != nil:
		// The shell never answered before the bound above ran out — distinct
		// from answering `conventional`, because the two need different
		// fixes: this one is worth retrying (a slow machine, a loaded
		// helper), the other is not (the shell itself will never integrate).
		s.compensateSpawn(ctx, tabID.String(), opened.Session)
		return nil, fmt.Errorf(
			"worker spawn: the participant's shell never answered its integration handshake within the deadline; retrying may succeed if this was transient: %w",
			awaitErr)
	case !outcome.Registered:
		// ABSENCE IS NOT A REFUSAL. RegisterIntegration's own doc calls this
		// "conventional by design": a session that never asked to be tracked
		// on the axis was refused nothing and has nothing to answer. Every
		// worker pane on this machine DOES ask (a local pane's launch always
		// names a shell and a lane, internal/app/helper_local.go's
		// localIntegrationStatus), so this is not the path a real spawn
		// takes — it exists so a caller with no axis at all (a test double,
		// or a future session kind that never wires one) is let through
		// rather than hung or refused for a question it was never asked.
		lg.Debug("worker spawn: no shell-integration axis is tracking this session; proceeding without a gate",
			"session_id", string(opened.Session.ID()))
	case outcome.Status != transport.IntegrationIntegrated:
		// `conventional` (or the degenerate `lost`, if the shell answered and
		// then the channel dropped before this call returned): a working
		// terminal with no live domain behind it. No grid will ever open for
		// it, so no coordinator could read or answer it — a silent degrade
		// AGENTS.md's testing rules name as the failure to refuse rather than
		// ship. Do not retry the same command unmodified: the shell itself is
		// what did not integrate.
		s.compensateSpawn(ctx, tabID.String(), opened.Session)
		return nil, fmt.Errorf(
			"worker spawn: the participant's shell answered %q (%s): this pane cannot be watched, so it cannot be a participant; do not retry the same command until the shell-integration failure is fixed",
			outcome.Status, outcome.Reason)
	}

	if req.Command != "" && !opened.Session.EnqueueWrite([]byte(req.Command+"\n")) {
		// A queue that refused is a session that is already going away.
		// Compensate here rather than letting the enrolment deadline do it:
		// the failure is known now, and waiting would spend the deadline
		// learning what we already know.
		s.compensateSpawn(ctx, tabID.String(), opened.Session)
		return nil, errors.New("worker spawn: the participant's session refused its first line")
	}
	// THE WRITE IS AN ATTEMPT AND NOT A START. What follows it is the
	// launcher's own startup, over which this has no visibility at all, so the
	// line says what was written rather than that anything ran.
	lg.Debug("worker spawn: the participant's first line is queued", "bytes", len(req.Command)+1)
	lg.Info("worker participant spawned",
		"participant", string(req.Participant), "worker", string(req.Group))

	// THE TASK, AFTER THE COMMAND AND NEVER IN THE SAME WRITE (nocx-66gd0).
	// The schema promised delivery and the code only ever recorded the text;
	// this is the fix. It cannot happen any earlier: the command line above
	// is what starts the agent, and there is nothing running to type into
	// before it has. deliverTask waits for the pane to say it is ready and
	// then submits through the SAME gate agent.type and the coordinator's own
	// wake go through — never a second door onto this pane's input queue.
	if req.Task != "" {
		if err := s.deliverTask(ctx, string(opened.Session.ID()), req.Task); err != nil {
			s.compensateSpawn(ctx, tabID.String(), opened.Session)
			return nil, fmt.Errorf("worker spawn: %w", err)
		}
		lg.Info("worker participant given its task", "bytes", len(req.Task))
	}
	return spawned, nil
}

// deliverTask waits for paneID to become typable and submits task into it.
//
// Nil readiness or nil typist is the ABSENCE case, exactly like
// integrationAwaiterSeam's above: a spawner nobody wired an observation or a
// typist into cannot ask whether a pane is ready any more than it could ask
// whether a domain accepted, so the spawn proceeds rather than hanging or
// refusing a question it was never asked. Production always wires both.
func (s *workerSpawner) deliverTask(ctx context.Context, paneID, task string) error {
	if s.readiness == nil || s.typist == nil {
		s.log.Debug("worker spawn: no pane-typing seam is wired; the task will not be typed at spawn",
			"pane_id", paneID)
		return nil
	}
	if waitErr := awaitFreeText(ctx, s.readiness, paneID); waitErr != nil {
		return fmt.Errorf("%w: %s", workers.ErrPaneNeverTypable, waitErr)
	}
	res := s.typist.Submit(paneID, task)
	if res.Outcome == agenttyping.OutcomeSubmitted {
		return nil
	}
	// OutcomeTyped is a refusal here too, not a delivery: the text reached
	// the input region and the submit key did not, so no turn started and the
	// coordinator's task never reached the agent — exactly the distinction
	// workerWaker's own doc draws for the identical outcome on a wake.
	reason := res.Reason
	if reason == "" {
		reason = fmt.Sprintf("nocx refused to submit the task (%s)", res.Outcome)
	}
	return fmt.Errorf("%w: %s", workers.ErrTaskSubmitRefused, reason)
}

// compensateSpawn undoes what Spawn built so far, in the reverse order of
// building it: the session, if one was opened, then the tab (nocx-ui8q6.4).
//
// Both failures are only logged. Spawn's own error already says what to
// report to the caller, and a compensation that also fails is not a second
// verdict on top of it — it is a warning worth having, the same asymmetry
// Kill's callers already accepted before this helper existed.
func (s *workerSpawner) compensateSpawn(ctx context.Context, tabID string, sess session.Session) {
	if sess != nil {
		if closeErr := s.sessions.Close(sess.ID()); closeErr != nil {
			s.log.Warn("worker spawn: could not close a session left behind by a failed spawn",
				"session_id", string(sess.ID()), "error", closeErr)
		}
	}
	if delErr := s.layout.DeleteTab(ctx, tabID, content.Replacement{}); delErr != nil {
		s.log.Warn("worker spawn: could not delete a tab left behind by a failed spawn",
			"tab_id", tabID, "error", delErr)
	}
}

// workerEnrolments turns "an enrolment arrived on this session" into "this
// participant is live".
//
// It is a rendezvous and not a poll: the register procedure blocks on Await
// while the launcher does its work, and the pane enroller hands the answer
// across when it answers agent_enrol. Nothing here reads a screen, and nothing
// times anything — the deadline belongs to the caller's context, so the bound
// is stated once, by whoever owns the interval.
type workerEnrolments struct {
	mu       sync.Mutex
	bySess   map[session.ID]workers.ParticipantID
	waiters  map[workers.ParticipantID]chan workers.Liveness
	arrived  map[workers.ParticipantID]workers.Liveness
	sessions sessionCloser
	log      log.Logger
}

func newWorkerEnrolments(lg log.Logger, sessions sessionCloser) *workerEnrolments {
	return &workerEnrolments{
		bySess:   make(map[session.ID]workers.ParticipantID),
		waiters:  make(map[workers.ParticipantID]chan workers.Liveness),
		arrived:  make(map[workers.ParticipantID]workers.Liveness),
		sessions: sessions,
		log:      lg,
	}
}

// expect records which participant a session's enrolment will speak for.
func (e *workerEnrolments) expect(p workers.ParticipantID, sid session.ID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bySess[sid] = p
	e.waiters[p] = make(chan workers.Liveness, 1)
}

// enrolled is called by the pane enroller when an agent_enrol is answered. It
// is deliberately tolerant of a session nobody is waiting for: most enrolments
// are a person running an agent in their own tab, and those are not
// participants.
func (e *workerEnrolments) enrolled(sid session.ID, lane string) {
	e.mu.Lock()
	p, ok := e.bySess[sid]
	if !ok {
		e.mu.Unlock()
		return
	}
	live := workers.Liveness{SessionID: string(sid), Lane: lane, Attempt: 1}
	if sess, err := e.sessions.Get(sid); err == nil {
		ident := sess.Identity()
		live.BackendInstance = string(ident.InstanceID)
		live.Epoch = ident.Epoch
	}
	e.arrived[p] = live
	ch := e.waiters[p]
	e.mu.Unlock()
	if ch != nil {
		// Buffered by one and written once: an enrolment cannot arrive twice
		// for one participant, because the grid refuses a second enrolment
		// for a pane it already watches.
		select {
		case ch <- live:
		default:
		}
	}
}

// participantFor answers which participant a session speaks for, or false for
// a session that is not one. Most sessions are not: a person running an agent
// in their own tab enrols and never reports, and asking this is how the
// report path tells the two apart.
func (e *workerEnrolments) participantFor(sid session.ID) (workers.ParticipantID, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.bySess[sid]
	return p, ok
}

// livenessOf returns the incarnation the participant's enrolment arrived on,
// which is what a later fact must match to be admitted.
func (e *workerEnrolments) livenessOf(p workers.ParticipantID) (workers.Liveness, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.arrived[p]
	return l, ok
}

// Await blocks until the participant's enrolment arrives or ctx is done.
//
// The already-arrived case is checked FIRST and it is not an optimisation: an
// enrolment can land between expect and the caller reaching this line, and a
// rendezvous that only ever listened would then wait out its whole deadline
// for a fact it already had.
func (e *workerEnrolments) Await(ctx context.Context, p workers.ParticipantID) (workers.Liveness, error) {
	e.mu.Lock()
	if live, ok := e.arrived[p]; ok {
		e.mu.Unlock()
		return live, nil
	}
	ch, ok := e.waiters[p]
	e.mu.Unlock()
	if !ok {
		return workers.Liveness{}, fmt.Errorf("worker: nothing is expecting an enrolment for %q", p)
	}
	select {
	case live := <-ch:
		return live, nil
	case <-ctx.Done():
		return workers.Liveness{}, workers.ErrEnrolmentNeverArrived
	}
}

// Withdraw forgets the participant. It undoes what expect recorded, so a
// compensated registration leaves no rendezvous behind for a later enrolment
// to satisfy.
func (e *workerEnrolments) Withdraw(_ context.Context, p workers.ParticipantID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for sid, have := range e.bySess {
		if have == p {
			delete(e.bySess, sid)
		}
	}
	delete(e.waiters, p)
	delete(e.arrived, p)
	return nil
}

// workerSupervisor is the watch that outlives the coordinator's turn.
//
// It watches the SESSION, which is where nocx owns the process: Done closes
// when the pty is gone and ExitOutcome says how. That is the whole of the
// observed evidence class — process exit only — and it is why nothing here
// imports the grid.
type workerSupervisor struct {
	sessions sessionCloser
	// exited is bound after the registrar exists, because the registrar
	// needs a supervisor to be constructed and the supervisor needs the
	// registrar to report to. Two-phase wiring at the composition root, which
	// is the ordinary shape for a cycle between two things the root owns.
	exited func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, e workers.Exit)
	log    log.Logger
}

// Attach begins watching a participant that is already recorded live.
//
// A session that is ALREADY GONE is the case this method exists to get right,
// and it is not an error: the record was marked live and the process exited
// before this line ran, which is precisely the window the register procedure's
// ordering makes observable rather than lossy. It is reported as an exit
// immediately, so the watcher finding an already-terminal process behaves the
// same as one that watched the transition.
func (s *workerSupervisor) Attach(ctx context.Context, p workers.Participant) error {
	sess, err := s.sessions.Get(session.ID(p.Liveness.SessionID))
	if err != nil {
		s.log.Info("worker supervision: the participant's session is already gone",
			"participant", string(p.ID), "session_id", p.Liveness.SessionID)
		// ExitInterrupted and not ExitExited: the session is not in the
		// registry, so the backend cannot assert HOW it ended, and inventing
		// an exit status for it is the one thing this vocabulary refuses.
		s.report(ctx, p, workers.Exit{Cause: string(session.ExitInterrupted)})
		return nil
	}
	// Background, and deliberately: the owner is the session, which outlives
	// every WebSocket and every coordinator turn (AD-9). Closing event: the
	// session's own Done, which the registry closes on teardown — so this
	// goroutine ends exactly when the thing it watches does, and there is no
	// second way out to get wrong.
	go func() {
		<-sess.Done()
		cause, code := sess.ExitOutcome()
		s.report(context.WithoutCancel(ctx), p, workers.Exit{Cause: string(cause), Code: code})
	}()
	return nil
}

func (s *workerSupervisor) report(ctx context.Context, p workers.Participant, e workers.Exit) {
	if s.exited == nil {
		// Unwired supervision is a worker nothing watches, which is the one
		// state this whole record exists to make impossible. Say so loudly
		// rather than dropping the fact.
		s.log.Error("worker supervision has no destination; a participant's exit was observed and not recorded",
			"participant", string(p.ID))
		return
	}
	s.exited(ctx, p.ID, p.Liveness, e)
}

// hookInto binds the rendezvous to the enroller and returns the enroller, so
// the composition root reads as one expression: the enroller is what
// lifecyclepub is given, and the worker is what it also tells.
func (e *workerEnrolments) hookInto(p *paneEnroller) *paneEnroller {
	p.onEnrol = func(sessionID, lane string) { e.enrolled(session.ID(sessionID), lane) }
	return p
}

// workerReporter records what a participant says its own work produced.
//
// It is the second of the two facts, and it arrives on the authenticated
// lifecycle channel rather than being read off a screen. The lane is what the
// kernel authenticated; everything else is derived from it here, because the
// composition root is the only place that holds all three maps — lane to
// session, session to participant, participant to record.
//
// A report from a pane that is not a participant is REFUSED and says why. It
// is not an error in the product: a person's own agent may well be integrated
// and enrolled, and telling it plainly that there is no worker record to declare
// into is better than accepting a declaration into nowhere.
type workerReporter struct {
	lanes   *sessionRegistry
	enrol   *workerEnrolments
	declare func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, d workers.Declaration) error
	now     func() time.Time
	log     log.Logger
}

func (r *workerReporter) Report(lane lifecycle.LaneID, ok bool, summary string) error {
	sid, found := r.lanes.lookup(lane)
	if !found || sid == "" {
		return errors.New("nocx does not know which pane this shell is")
	}
	participant, isParticipant := r.enrol.participantFor(session.ID(sid))
	if !isParticipant {
		return errors.New("this pane is not part of a worker, so there is nothing to report to")
	}
	live, known := r.enrol.livenessOf(participant)
	if !known {
		// Enrolled but with no recorded incarnation is a state the ordering
		// makes unreachable — expect runs before the enrolment can arrive —
		// so saying so is better than inventing a liveness that would then
		// be compared against the record and refused for the wrong reason.
		return errors.New("this participant has no recorded incarnation yet")
	}
	if r.declare == nil {
		return errors.New("this backend is not wired to record what an agent produced")
	}
	// The time is the BACKEND's. There is no clock shared with a participant,
	// and one it supplied would be a value it could pick.
	if err := r.declare(context.Background(), participant, live,
		workers.Declaration{OK: ok, Summary: summary, At: r.now()}); err != nil {
		r.log.Warn("worker: a participant's declaration was not recorded",
			"participant", string(participant), "error", err)
		return errors.New("nocx could not record what you reported")
	}
	r.log.Info("worker participant reported",
		"participant", string(participant), "ok", ok)
	return nil
}

// workerCloser ends a participant by closing its session.
//
// It is the composition root's because what a participant's process IS is a
// session, and only this layer holds the registry. It writes nothing and
// reports no verdict: closing the session produces a real process exit, which
// the supervisor already watches and the record already reduces — so a close
// adds no second author of a participant's state.
type workerCloser struct {
	sessions sessionCloser
	log      log.Logger
}

func (c *workerCloser) Close(_ context.Context, p workers.Participant) error {
	sid := session.ID(p.Liveness.SessionID)
	// Asked FIRST, because the registry's Close reports a missing session as
	// an ordinary error and a session that is already gone is not a failure
	// to end one: the supervisor has already reported that exit or is about
	// to, and the record needs nothing from here. There is no sentinel to
	// match on, and matching on the sentence would be worse than asking.
	if _, err := c.sessions.Get(sid); err != nil {
		c.log.Info("worker close: the participant's session was already gone",
			"participant", string(p.ID), "session_id", string(sid))
		return nil
	}
	if err := c.sessions.Close(sid); err != nil {
		return fmt.Errorf("worker close: %w", err)
	}
	c.log.Info("worker participant closed", "participant", string(p.ID), "session_id", string(sid))
	return nil
}

// ── the two routes out of the undispatched set (nocx-dkawo.3) ─────────────
//
// internal/worker says WHO must be told and ABOUT WHAT. It says nothing about
// whether a screen permits typing, or which surfaces a notification may
// reach, because both of those already have owners — internal/agenttyping on
// frames it reads itself, and internal/notify's trust and routing table. What
// is here is the composition root's half: the two adapters, and the one fact
// only this layer knows, which is that a coordinator's pane id and its
// session id are the same string.

// paneTypist is the app's narrow view of the typing primitive (AD-8): submit
// text into a pane and be told what came of it. One method, because a wake is
// one act — and deliberately NOT Type, which leaves text in an input region
// without starting the turn the wake exists to start.
type paneTypist interface {
	Submit(paneID, text string) agenttyping.Result
}

// workerWaker types into the coordinator's pane.
//
// It reaches the SAME Typist the agent.type method reaches, and that is the
// point of it being here: a second one would be a second answer to "may nocx
// write into this pane", decided against a second grid. The gates are that
// package's and are not restated — this translates an outcome and nothing
// else.
type workerWaker struct {
	typist paneTypist
	log    log.Logger
}

// Wake starts a turn the coordinator did not ask for.
//
// The pane id IS the session id: the enroller opens a pane's grid under the
// session id it resolved the lane to, and the typist's Screens, Enrolment and
// Input seams are all keyed by that same string. The composition root is
// where that is known, which is why the translation lives here rather than in
// the record.
//
// ONLY OutcomeSubmitted is a delivery. OutcomeTyped means the text reached the
// input region and the submit key did not — the coordinator is looking at an
// unsent line, which starts no turn — so it is reported as a refusal carrying
// the reason the submit failed. Calling that a delivery is exactly the
// "reported as sent" the bead refuses.
func (w *workerWaker) Wake(_ context.Context, coordinatorSession, text string) workers.WakeOutcome {
	if coordinatorSession == "" {
		return workers.WakeOutcome{Reason: "this worker records no coordinator session to type into"}
	}
	if w.typist == nil {
		return workers.WakeOutcome{Reason: "this backend has no way to type into a pane"}
	}
	res := w.typist.Submit(coordinatorSession, text)
	switch res.Outcome {
	case agenttyping.OutcomeSubmitted:
		return workers.WakeOutcome{Delivered: true}
	case agenttyping.OutcomeTyped:
		reason := res.Reason
		if reason == "" {
			reason = "the text reached the coordinator's input region and the submit key did not"
		}
		return workers.WakeOutcome{Reason: reason}
	default:
		reason := res.Reason
		if reason == "" {
			// A refusal with no sentence would be indistinguishable from a
			// delivery in the record, which is the one thing this outcome
			// must never be.
			reason = fmt.Sprintf("nocx refused to type into that pane (%s)", res.State)
		}
		return workers.WakeOutcome{Reason: reason}
	}
}

// workerEscalation tells the person about a fact nobody dispatched.
//
// It raises an ordinary notification and decides nothing about where it goes:
// trust and routing are internal/notify's, enforced default-deny against a
// table the person owns (§6.1, and where this design and Trust disagree,
// Trust wins because Trust is enforced in code).
type workerEscalation struct {
	raise workerNotifier
	log   log.Logger
}

// workerNotifier is the escalation's narrow view of the notification pipeline
// (AD-8): raise one event. Declared here rather than borrowed from the
// transport, because a notification is internal/notify's concept and the worker
// does not reach it through the wire.
type workerNotifier interface {
	Raise(ctx context.Context, ev notify.Event) notify.Outcome
}

// Escalate stamps the event and hands it to ingress.
//
// The SessionID is the coordinator's, because that is the pane a person
// clicking the notification wants to be taken to: the fact is about a worker
// and the decision is the coordinator's, and a notification that opened the
// worker's pane would be showing the screen that is NOT waiting for anybody.
//
// The body says whether the coordinator was reached and why not, because
// "your worker finished and nobody has looked at it" and "your worker
// finished, we told the coordinator, and it has not acted in five minutes"
// ask the person for different things.
func (e *workerEscalation) Escalate(ctx context.Context, f workers.Fact) {
	if e.raise == nil {
		e.log.Error("worker: a fact went undispatched and this backend has no notification pipeline",
			"participant", string(f.Participant), "worker", string(f.Group))
		return
	}
	body := "The coordinator was told and has not acted."
	if !f.Wake.Delivered {
		body = "nocx could not reach the coordinator: " + f.Wake.Reason
	}
	// One card per worker, and the card says how many. Escalation coalesces
	// (nocx-dkawo.4), so this is the whole situation rather than the first
	// fact of it, and a person who reads "and 4 others" knows not to go
	// looking for four more cards that were deliberately not raised.
	if f.AlsoOwed == 1 {
		body += " One other worker in this worker is also waiting."
	} else if f.AlsoOwed > 1 {
		body += fmt.Sprintf(" %d other workers in this worker are also waiting.", f.AlsoOwed)
	}
	title := fmt.Sprintf("A worker is waiting: %s", f.Task)
	if f.Task == "" {
		title = "A worker is waiting for its coordinator"
	}
	e.raise.Raise(ctx, notify.Event{
		SessionID: f.CoordinatorSession,
		Title:     title,
		Body:      body,
		Kind:      notify.KindWorkersUndispatched,
		// Attested: this is nocx's own record reducing a process exit off a
		// PTY it holds and a declaration over an authenticated channel.
		// Nothing on a screen took part.
		Trust: notify.TrustAttested,
		Level: notify.LevelWarning,
		Attribution: notify.Attribution{
			Backend: commandnames.LocalRoute,
			Session: f.CoordinatorSession,
		},
	})
}
