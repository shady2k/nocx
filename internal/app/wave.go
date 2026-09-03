package app

// The composition root's half of the wave record (nocx-dkawo.6, nocx-dkawo.7).
//
// internal/wave owns the semantics and names four seams it cannot satisfy
// itself: what a session IS, what a pane IS, when an enrolment arrived and
// when a process is gone. All four are answers this file already holds,
// because the composition root is the one place the layout chain, the session
// opener, the lifecycle enroller and the session registry meet.
//
// THE TWO FACTS AND NOTHING ELSE. What decides a participant's state here is
// its process exit and its own declaration, exactly as D9 says. The grid is in
// this same file's neighbourhood and is never consulted: it decides whether
// nocx may type into a pane and what the indicator shows, and a wave state
// derived from a screen is the self-matching sentinel this design exists to
// kill.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/wave"
)

// sessionOpenerSeam is the app's narrow view of the one session-open path
// (nocx-dkawo.6). It is an interface and not the server because what a spawn
// needs is one sentence — open a session for this pane — and a handler, a
// broker and forty other methods are not part of it.
type sessionOpenerSeam interface {
	OpenSession(ctx context.Context, spec transport.OpenSpec) (transport.OpenedSession, error)
}

// paneMinter is the spawner's narrow view of the layout chain (AD-8): mint a
// tab and its first pane. A participant needs exactly that one call, and the
// twenty-odd other things a layout repository can do — reordering strips,
// recolouring workspaces, clearing the window — are not things a spawn may
// reach for.
type paneMinter interface {
	CreateTab(ctx context.Context, tab content.Tab, firstPane content.Pane) (content.Created[content.NewTab], error)
}

// sessionCloser ends a session by id. The registry's own Close, named as the
// one thing a compensation needs.
type sessionCloser interface {
	Get(id session.ID) (session.Session, error)
	Close(id session.ID) error
}

// participantGeometry is the size a participant's pane opens at.
//
// It is a constant and not a setting because nobody is looking at this pane
// when it opens: a wave participant is spawned by the backend, and the first
// client to attach reports its own geometry and the session resizes. What this
// number has to be is big enough that an agent TUI's first repaint is not
// wrapped into nonsense before anyone sees it.
const (
	participantCols = 120
	participantRows = 40
)

// waveSpawner mints the pane a participant lives in and opens its session.
//
// The pane is a TAB of its own rather than a split, because a participant is a
// thing a person switches to and closes, and because CreateTab is the one call
// that mints a container together with its first member — a pane created
// without one would be a member of nothing.
type waveSpawner struct {
	layout   paneMinter
	opener   sessionOpenerSeam
	sessions sessionCloser
	// enrolments is told the participant → session mapping at spawn, because
	// an enrolment arrives naming a SESSION and the record is keyed by
	// participant. Telling it here rather than deriving it later keeps one
	// owner of that mapping and keeps it correct before the first frame.
	enrolments *waveEnrolments
	// workspace is where a participant's tab is minted. The wave's own
	// workspace, resolved by the caller, never guessed here.
	workspace string
	log       log.Logger
}

// spawnedParticipant is a launcher that has been started. It is not yet a
// participant: nothing may be addressed until its enrolment arrives.
type spawnedParticipant struct {
	sess     session.Session
	sessions sessionCloser
}

func (s spawnedParticipant) Liveness() wave.Liveness {
	ident := s.sess.Identity()
	return wave.Liveness{
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

// Spawn mints the pane and opens the session, in that order.
//
// The order is the rollback here too: the pane row is written first, so a
// session that fails to open leaves a pane a person can see and close, while a
// session opened for a pane that was never recorded would be a live shell with
// no durable identity to anchor its blocks to.
//
// The agent's command line is written into the session's own input queue —
// the same queue a person's keystrokes take — and NOT through
// internal/agenttyping. That package exists to refuse a mistimed keystroke
// into a running agent TUI, where the modal on screen answers Yes; here there
// is no TUI and no modal, because the session was opened microseconds ago and
// holds nothing but a shell that has not drawn its first prompt.
//
// What makes this safe is not the timing but the INTERVAL: a command line that
// never runs produces no enrolment, and a registration whose enrolment never
// arrives is terminalized rather than left as a participant nobody can reach.
// The enrolment is the proof the agent started; the write is only the attempt.
func (s *waveSpawner) Spawn(ctx context.Context, req wave.SpawnRequest) (wave.Spawned, error) {
	tabID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("wave spawn: minting a tab id: %w", err)
	}
	paneID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("wave spawn: minting a pane id: %w", err)
	}
	if _, tabErr := s.layout.CreateTab(ctx,
		content.Tab{ID: tabID.String(), WorkspaceID: s.workspace, Layout: content.LayoutRow},
		content.Pane{ID: paneID.String(), TabID: tabID.String(), Kind: content.PaneLocal, SizeShare: 1},
	); tabErr != nil {
		return nil, fmt.Errorf("wave spawn: minting the participant's tab: %w", tabErr)
	}

	opened, err := s.opener.OpenSession(ctx, transport.OpenSpec{
		PaneID: paneID.String(),
		Cols:   participantCols,
		Rows:   participantRows,
	})
	if err != nil {
		return nil, fmt.Errorf("wave spawn: opening the participant's session: %w", err)
	}
	spawned := spawnedParticipant{sess: opened.Session, sessions: s.sessions}

	// Told BEFORE the command is written, or an enrolment that arrives
	// promptly would find nobody waiting for it.
	s.enrolments.expect(req.Participant, opened.Session.ID())

	if req.Command != "" && !opened.Session.EnqueueWrite([]byte(req.Command+"\n")) {
		// A queue that refused is a session that is already going away.
		// Compensate here rather than letting the enrolment deadline do it:
		// the failure is known now, and waiting would spend the deadline
		// learning what we already know.
		if err := spawned.Kill(ctx); err != nil {
			s.log.Warn("wave spawn: could not close a session whose input queue refused",
				"session_id", string(opened.Session.ID()), "error", err)
		}
		return nil, errors.New("wave spawn: the participant's session refused its first line")
	}
	s.log.Info("wave participant spawned",
		"participant", string(req.Participant), "wave", string(req.Wave),
		"session_id", string(opened.Session.ID()), "pane_id", paneID.String())
	return spawned, nil
}

// waveEnrolments turns "an enrolment arrived on this session" into "this
// participant is live".
//
// It is a rendezvous and not a poll: the register procedure blocks on Await
// while the launcher does its work, and the pane enroller hands the answer
// across when it answers agent_enrol. Nothing here reads a screen, and nothing
// times anything — the deadline belongs to the caller's context, so the bound
// is stated once, by whoever owns the interval.
type waveEnrolments struct {
	mu       sync.Mutex
	bySess   map[session.ID]wave.ParticipantID
	waiters  map[wave.ParticipantID]chan wave.Liveness
	arrived  map[wave.ParticipantID]wave.Liveness
	sessions sessionCloser
	log      log.Logger
}

func newWaveEnrolments(lg log.Logger, sessions sessionCloser) *waveEnrolments {
	return &waveEnrolments{
		bySess:   make(map[session.ID]wave.ParticipantID),
		waiters:  make(map[wave.ParticipantID]chan wave.Liveness),
		arrived:  make(map[wave.ParticipantID]wave.Liveness),
		sessions: sessions,
		log:      lg,
	}
}

// expect records which participant a session's enrolment will speak for.
func (e *waveEnrolments) expect(p wave.ParticipantID, sid session.ID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bySess[sid] = p
	e.waiters[p] = make(chan wave.Liveness, 1)
}

// enrolled is called by the pane enroller when an agent_enrol is answered. It
// is deliberately tolerant of a session nobody is waiting for: most enrolments
// are a person running an agent in their own tab, and those are not
// participants.
func (e *waveEnrolments) enrolled(sid session.ID, lane string) {
	e.mu.Lock()
	p, ok := e.bySess[sid]
	if !ok {
		e.mu.Unlock()
		return
	}
	live := wave.Liveness{SessionID: string(sid), Lane: lane, Attempt: 1}
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
func (e *waveEnrolments) participantFor(sid session.ID) (wave.ParticipantID, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.bySess[sid]
	return p, ok
}

// livenessOf returns the incarnation the participant's enrolment arrived on,
// which is what a later fact must match to be admitted.
func (e *waveEnrolments) livenessOf(p wave.ParticipantID) (wave.Liveness, bool) {
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
func (e *waveEnrolments) Await(ctx context.Context, p wave.ParticipantID) (wave.Liveness, error) {
	e.mu.Lock()
	if live, ok := e.arrived[p]; ok {
		e.mu.Unlock()
		return live, nil
	}
	ch, ok := e.waiters[p]
	e.mu.Unlock()
	if !ok {
		return wave.Liveness{}, fmt.Errorf("wave: nothing is expecting an enrolment for %q", p)
	}
	select {
	case live := <-ch:
		return live, nil
	case <-ctx.Done():
		return wave.Liveness{}, wave.ErrEnrolmentNeverArrived
	}
}

// Withdraw forgets the participant. It undoes what expect recorded, so a
// compensated registration leaves no rendezvous behind for a later enrolment
// to satisfy.
func (e *waveEnrolments) Withdraw(_ context.Context, p wave.ParticipantID) error {
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

// waveSupervisor is the watch that outlives the coordinator's turn.
//
// It watches the SESSION, which is where nocx owns the process: Done closes
// when the pty is gone and ExitOutcome says how. That is the whole of the
// observed evidence class — process exit only — and it is why nothing here
// imports the grid.
type waveSupervisor struct {
	sessions sessionCloser
	// exited is bound after the registrar exists, because the registrar
	// needs a supervisor to be constructed and the supervisor needs the
	// registrar to report to. Two-phase wiring at the composition root, which
	// is the ordinary shape for a cycle between two things the root owns.
	exited func(ctx context.Context, id wave.ParticipantID, l wave.Liveness, e wave.Exit)
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
func (s *waveSupervisor) Attach(ctx context.Context, p wave.Participant) error {
	sess, err := s.sessions.Get(session.ID(p.Liveness.SessionID))
	if err != nil {
		s.log.Info("wave supervision: the participant's session is already gone",
			"participant", string(p.ID), "session_id", p.Liveness.SessionID)
		// ExitInterrupted and not ExitExited: the session is not in the
		// registry, so the backend cannot assert HOW it ended, and inventing
		// an exit status for it is the one thing this vocabulary refuses.
		s.report(ctx, p, wave.Exit{Cause: string(session.ExitInterrupted)})
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
		s.report(context.WithoutCancel(ctx), p, wave.Exit{Cause: string(cause), Code: code})
	}()
	return nil
}

func (s *waveSupervisor) report(ctx context.Context, p wave.Participant, e wave.Exit) {
	if s.exited == nil {
		// Unwired supervision is a wave nothing watches, which is the one
		// state this whole record exists to make impossible. Say so loudly
		// rather than dropping the fact.
		s.log.Error("wave supervision has no destination; a participant's exit was observed and not recorded",
			"participant", string(p.ID))
		return
	}
	s.exited(ctx, p.ID, p.Liveness, e)
}

// hookInto binds the rendezvous to the enroller and returns the enroller, so
// the composition root reads as one expression: the enroller is what
// lifecyclepub is given, and the wave is what it also tells.
func (e *waveEnrolments) hookInto(p *paneEnroller) *paneEnroller {
	p.onEnrol = func(sessionID, lane string) { e.enrolled(session.ID(sessionID), lane) }
	return p
}

// waveReporter records what a participant says its own work produced.
//
// It is the second of the two facts, and it arrives on the authenticated
// lifecycle channel rather than being read off a screen. The lane is what the
// kernel authenticated; everything else is derived from it here, because the
// composition root is the only place that holds all three maps — lane to
// session, session to participant, participant to record.
//
// A report from a pane that is not a participant is REFUSED and says why. It
// is not an error in the product: a person's own agent may well be integrated
// and enrolled, and telling it plainly that there is no wave record to declare
// into is better than accepting a declaration into nowhere.
type waveReporter struct {
	lanes   *sessionRegistry
	enrol   *waveEnrolments
	declare func(ctx context.Context, id wave.ParticipantID, l wave.Liveness, d wave.Declaration) error
	now     func() time.Time
	log     log.Logger
}

func (r *waveReporter) Report(lane lifecycle.LaneID, ok bool, summary string) error {
	sid, found := r.lanes.lookup(lane)
	if !found || sid == "" {
		return errors.New("nocx does not know which pane this shell is")
	}
	participant, isParticipant := r.enrol.participantFor(session.ID(sid))
	if !isParticipant {
		return errors.New("this pane is not part of a wave, so there is nothing to report to")
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
		wave.Declaration{OK: ok, Summary: summary, At: r.now()}); err != nil {
		r.log.Warn("wave: a participant's declaration was not recorded",
			"participant", string(participant), "error", err)
		return errors.New("nocx could not record what you reported")
	}
	r.log.Info("wave participant reported",
		"participant", string(participant), "ok", ok)
	return nil
}
