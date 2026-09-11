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
	"strings"
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
	"github.com/shady2k/nocx/internal/panegrid"
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
//
// THIS BYPASSES capability.LayoutOperation — a person's tabs.create goes
// through it (internal/transport/ws_layout_handlers.go) and this does not —
// and that is deliberate rather than a hole nobody closed (nocx-ui8q6.3
// asked the question explicitly; this is the answer, with the evidence).
// capability/layout.go's own package doc names what that operation's guard
// actually is: a control.Admission gate composed for the JSON-RPC dispatch
// layer, there to (a) serialize a logical sequence that reads the chain and
// then writes it — a reorder checked against a snapshot, a rename racing
// another rename — against other RENDERER-INITIATED requests on the same
// domain, and (b) refuse into the control.saturated wire contract when that
// queue is full, which is a statement about *dispatch backpressure on one
// WebSocket's request stream*. Neither reads as an authorization check: the
// guard's check() call trips only on a captured service handle escaping its
// own Run, never on who is asking. The actual data-race protection for the
// underlying SQLite tables is a layer BELOW capability entirely — every
// content-domain mutation, called through the gate or not, is serialized by
// the same single writer goroutine content's own docs describe
// (internal/content/sqlite.go's writeCh, layout_sqlite.go's "every mutation
// goes through the single writer goroutine") — so a spawn's CreateTab and a
// person's concurrent tabs.rename cannot corrupt one row between them
// whether or not the spawn also holds capability's admission gate.
//
// What capability's gate would add here is only backpressure participation:
// letting a worker spawn queue and wait behind a saturated content-domain
// dispatch queue, or be refused with control.saturated, exactly as a
// person's own tabs.create would be. That is the wrong shape for THIS
// caller. workers.spawn is already an authorized tool call by the time
// Spawn runs — the delegate effect over the resource environment is checked
// at the tool endpoint, before any pane is minted — and that check is a
// different resource entirely (the run's own fence, not the JSON-RPC
// dispatch lane a renderer's socket occupies). Routing a backend-internal
// mint through capability.LayoutOperation would make a coordinator's spawn
// compete for, and be throttled by, the queue depth a PERSON'S layout
// clicks are bounded by — coupling two backpressure domains that have no
// reason to share a budget, for a guard whose actual job (the escaped-handle
// check) has nothing to do with any of this. So the two seams stay two
// seams: tabs.create goes through the operation because it answers untrusted,
// connection-scoped requests that need dispatch admission; workers.go calls
// the repository directly because it is one already-authorized, backend-
// internal write with no request to admit and no handle that could escape
// anywhere capability's guard would catch it.
// PaneCwd joined them at nocx-ty5ks, for the same reason and under the same
// rule: a participant's pane opens where its coordinator is standing, and the
// directory a pane is standing in has ONE owner already — the layout row the
// renderer writes from a verified OSC 7 (content.Layout.SetPaneCwd). Reading
// it here is asking that owner; deriving one would be a second answer to a
// question already answered (AD-8).
type paneMinter interface {
	CreateTab(ctx context.Context, tab content.Tab, firstPane content.Pane) (content.Created[content.NewTab], error)
	DeleteTab(ctx context.Context, id string, next content.Replacement) error
	PaneCwd(ctx context.Context, paneID string) (string, error)
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

// tabAnnouncer is the spawner's narrow view of the transport's push to a
// connected renderer (nocx-ui8q6.3): tell every connected client a
// participant's tab exists. Nil is treated exactly like integration's and
// readiness's absence elsewhere in this file — a spawner nobody wired one
// into cannot announce a tab any more than it could await an axis, so the
// spawn proceeds without telling anybody rather than panicking on a seam a
// test double never needed. Production always wires the real one.
type tabAnnouncer interface {
	AnnounceWorkerTab(tab content.Tab, pane content.Pane, sess session.Session)
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
	// announce tells a connected renderer the tab exists, once Spawn has
	// committed to succeeding (nocx-ui8q6.3). Nil is the absence case
	// tabAnnouncer's own doc names.
	announce tabAnnouncer
	// owed is marked when deliverTask ends on a question rather than typing
	// the task (nocx-f545a.7): the debt workerAnswerer pays once the
	// coordinator's own answer to that question is confirmed. A nil owed set
	// is the same absence case as every other optional seam here — nothing
	// is tracked, which only matters to a caller that never wires one in.
	owed *owedTasks
	log  log.Logger
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

// paneWaitState is what awaitFreeText has learned about a pane's own
// classification by the time it gives up: whether the watcher ever answered
// for it at all, and what it last held and for how long.
//
// A refusal built from anything less collapses three different failures into
// one silence (nocx-4gj5w): the grid not being fed at all, the driver
// settling on `working` or `unknown`, and there being no observation for this
// pane whatsoever. "The pane never became typable inside the spawn's own
// budget" was true in all three cases and told a reader nothing about which
// one they were looking at — on the trace this bead was filed from, thirty
// seconds of silence between "agent enrolled" and that one sentence.
type paneWaitState struct {
	observed bool
	state    agentdriver.State
	// since is when `state` was FIRST seen on this wait, so held (below)
	// answers "how long has the pane held this reading", not "how long has
	// this wait itself been running" — the two differ the moment a pane
	// passes through more than one state before the budget runs out.
	since time.Time
}

// held is how long the pane has shown `state`, as of now. Zero for a pane
// never observed at all: there is no state to have held anything.
func (w paneWaitState) held(now time.Time) time.Duration {
	if !w.observed {
		return 0
	}
	return now.Sub(w.since)
}

// note updates w for one fresh Snapshot reading, resetting the clock only on
// a transition — a pane read as the SAME state on every poll must not look
// like it just started holding it.
func (w *paneWaitState) note(o paneobserve.Observation) {
	if !w.observed || w.state != o.State {
		w.observed = true
		w.state = o.State
		w.since = time.Now()
	}
}

// awaitFreeText blocks until paneID's last-classified observation is
// free_text, until it is a QUESTION the pane's agent is asking of its own
// (permission_choice or modal_choice, nocx-f545a.3), until ctx ends, or until
// the pane's agent is seen to have exited. It answers the state that ended the
// wait; a question is an answer, not a failure.
//
// It asks the WATCHER rather than retrying agenttyping.Submit in a loop: a
// refused Submit call would read the live screen and log a warning on every
// attempt, where the watcher already tracks exactly this state for free, off
// the same sweep the product's own indicator uses. Submit is still called
// exactly once, after this returns, and it RE-VERIFIES the screen itself
// (agenttyping's own documented guarantee) — this function only decides when
// that one attempt is worth making, never whether it is allowed to write.
//
// lg is the ONE thing this function adds beyond its own return value
// (nocx-4gj5w): the error it returns is for the coordinator, worded as a
// sentence about what to do next; the log line at the same moment is for
// whoever is reading this backend's log, and carries the same facts as
// structured fields rather than prose, because the two readers are not
// looking for the same thing.
//
// label names the CALLER in that log line — "worker spawn" or "worker
// answer" — because this same wait now runs from two places (nocx-f545a.7):
// a spawn typing a task for the first time, and an answerer paying a task it
// owes once a menu is confirmed. A hardcoded "worker spawn:" in the answer's
// own log line would describe a call that never happened.
//
// r MUST ALREADY BE A LIVE READING for a caller that has just written into
// the pane. A question is trusted the moment this function sees it — the
// FIRST check included, exactly as before nocx-f545a.7 — which is correct
// for a fresh classification (a spawn's first observation has nothing to be
// stale relative to) and would be wrong for the watcher's own cache read
// immediately after a menu confirm: Touch fires only on the session's own
// READ side, nothing about WRITING into a pane touches it, so that cache is,
// with certainty, still the reading of the menu that was just confirmed. An
// earlier version of this function tried to paper over that by distrusting
// the very first check and delaying one deliveryPoll — which only narrowed
// the race by one tick rather than closing it, and AGENTS.md's own rule is
// that a test may not depend on timing to be right eventually. The actual
// fix lives one layer up, in workerAnswerer.typeOwedTask: it does not call
// this with the watcher's cache at all. It waits, on the GRID, for the
// confirmed menu to leave the screen first (awaitMenuLeftScreen), and only
// then classifies the CURRENT frame (paneobserve.Watcher.Classify, adapted
// by classifyingReadiness) and hands THAT live reading here. So by the time
// r.Snapshot is ever asked, the answer to "is this pane still showing what I
// just confirmed" is already known to be no — freshness is established by
// two facts, not by waiting a little and hoping.
func awaitFreeText(ctx context.Context, r paneReadiness, paneID string, lg log.Logger, label string) (agentdriver.State, error) {
	var last paneWaitState
	// check answers the state that ENDS the wait, and "" while nothing has.
	check := func() (agentdriver.State, error) {
		o, ok := r.Snapshot(paneID)
		if !ok {
			return "", nil
		}
		last.note(o)
		switch o.State {
		case agentdriver.StateFreeText:
			return o.State, nil
		case agentdriver.StatePermissionChoice, agentdriver.StateModalChoice:
			// A QUESTION ENDS THE WAIT, AND IT IS NOT A FAILURE (ADR-0064).
			// The pane was positively identified as asking something: the
			// agent is not busy, and nocx did not fail to read it. No budget
			// changes that without somebody answering, and waiting one out
			// and then deleting the pane is what threw away the only screen
			// that said so (nocx-ty5ks).
			return o.State, nil
		case agentdriver.StateExited:
			return "", errors.New("the participant's agent exited before its pane ever became typable")
		default:
			return "", nil
		}
	}
	if state, err := check(); state != "" || err != nil {
		return state, err
	}
	ticker := time.NewTicker(deliveryPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", refusePaneNeverTypable(lg, paneID, last, label)
		case <-ticker.C:
			if state, err := check(); state != "" || err != nil {
				return state, err
			}
		}
	}
}

// refusePaneNeverTypable builds the sentence a coordinator reads and the log
// line a person reads, from the SAME facts — what the wait above saw of
// paneID before its budget ran out. They serve different readers (the error
// wraps workers.ErrPaneNeverTypable and is meant to be read once, out of
// context; the log line is meant to be grepped, beside every other line this
// spawn wrote) and so they say the same thing twice rather than one being
// derived from the other.
//
// UNKNOWN AND WORKING ARE NOT THE SAME REFUSAL (nocx-qddv8, nocx-4gj5w):
// `unknown` means the driver could not identify what was on screen at all —
// a rule that does not recognise this agent's chrome, which will not resolve
// itself no matter how long the budget runs — where every other state that
// can still be held here (`working`, `error`) means the driver read the
// screen just fine and the agent was doing something else. Retrying the
// first is retrying a bug; retrying the second may just need a longer wait
// or a busier machine. A question (a permission or a menu) never reaches
// this function: it ends the wait as an answer (nocx-f545a.3). The state
// rides the error as *workers.PaneNeverTypable, so a caller can choose its
// sentence from a value rather than from this prose.
//
// label is the same word awaitFreeText was given, and it names the caller in
// both the log line and the error's own Detail — no longer hardcoded to
// "spawn" — which is what makes both readings true of a call that came from
// workerAnswerer's paid debt as much as from a spawn (see awaitFreeText's
// doc). Neither existing caller's test asserts on this prose, only on the
// structured fields beside it and on the state named inside it.
func refusePaneNeverTypable(lg log.Logger, paneID string, last paneWaitState, label string) error {
	if !last.observed {
		lg.Warn(label+": the pane's budget expired with no observation ever recorded for it",
			"pane_id", paneID)
		return &workers.PaneNeverTypable{Detail: "nocx never observed this pane at all inside its own budget; " +
			"there is no reading to say why it did not become typable"}
	}
	held := last.held(time.Now())
	lg.Warn(label+": the pane's budget expired before it became typable",
		"pane_id", paneID, "last_state", string(last.state), "held_ms", held.Milliseconds())
	if last.state == agentdriver.StateUnknown {
		return &workers.PaneNeverTypable{State: string(last.state), Detail: fmt.Sprintf(
			"the pane's screen held state %q for %s: nocx's driver did not recognise what was on screen, and a pane it cannot read will never become typable on its own",
			last.state, held.Round(time.Millisecond))}
	}
	return &workers.PaneNeverTypable{State: string(last.state), Detail: fmt.Sprintf(
		"the pane held state %q for %s without becoming typable",
		last.state, held.Round(time.Millisecond))}
}

// owedTasks is the in-memory set of participants whose spawn left a task
// untyped because the pane asked a question first (nocx-f545a.3, nocx-f545a.7).
//
// THE INTERVAL. A session id is owed from the moment deliverTask's own wait
// ends on a question — WaitingOn != "" — until one of: (a) the task is
// actually submitted (workerAnswerer's own take, below, is the check-and-clear
// that makes this happen at most once even under two concurrent answers); (b)
// the participant's session ends, dropped from workerSupervisor.report AND
// from spawnedParticipant.Kill — Kill's own doc already establishes why a
// registration that fails after Spawn compensates through it before any
// supervisor is ever attached, and this debt must not outlive either path; or
// (c) this backend restarts, which needs no code at all: the set lives only in
// this process's memory, and the restart sweep that finds such a participant
// abandoned is exactly the interruption ADR-0064 already treats a human
// takeover as orthogonal to — see the omission note below.
//
// A HUMAN TAKEOVER DOES NOT CLOSE IT. Suspending EffectSendInput stops a
// coordinator's own answer from reaching the pane (Registrar.Answer refuses
// before workerAnswerer.Answer is ever called), but it does not un-owe the
// task: the person is not nocx, and nothing here reads a screen to decide
// whether they typed it themselves. The debt closes only through (a), (b) or
// (c) above.
//
// It is deliberately NOT part of the workers.Store record: ADR-0064 §4 says a
// screen reading assigns no status to a participant and a menu answer moves no
// record, and "this participant's task is still owed" is exactly such a
// status — derived from what nocx did at spawn, not from anything the
// participant declared. So it lives here, at the composition root, beside the
// other two maps (workerEnrolments) this layer already owns for the same
// reason.
type owedTasks struct {
	mu   sync.Mutex
	sids map[session.ID]struct{}
}

func newOwedTasks() *owedTasks {
	return &owedTasks{sids: make(map[session.ID]struct{})}
}

// mark records sid as owed. A nil *owedTasks is a spawner nobody wired one
// into, treated exactly like every other optional seam in this file: the
// spawn proceeds and nothing is tracked, rather than panicking on a debt
// nobody asked it to keep.
func (o *owedTasks) mark(sid session.ID) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sids[sid] = struct{}{}
}

// take is the check-and-clear: it reports whether sid was owed, and if so
// clears the debt in the same locked section. This is what makes two
// concurrent Answer calls on one owed participant type the task at most
// once — the second call's take finds nothing and does nothing, rather than
// both racing to submit.
func (o *owedTasks) take(sid session.ID) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.sids[sid]; !ok {
		return false
	}
	delete(o.sids, sid)
	return true
}

// restore re-marks sid as owed, for an answer that took the debt but could
// not pay it — the pane asked a different question, or the gate refused the
// submission. A later answer takes it again.
func (o *owedTasks) restore(sid session.ID) {
	o.mark(sid)
}

// drop ends the debt without paying it, for a participant whose session is
// gone — see the interval doc above for both callers.
func (o *owedTasks) drop(sid session.ID) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.sids, sid)
}

// spawnedParticipant is a launcher that has been started. It is not yet a
// participant: nothing may be addressed until its enrolment arrives.
//
// It carries the TAB alongside the session (nocx-ui8q6.7), which is what lets
// Kill undo the whole of what Spawn built rather than half of it. Before this
// the tab lived only in Spawn's own locals and compensateSpawn's undo ran
// beside Kill's rather than through it: Kill, reached by
// internal/workers.Registrar.compensate() for every failure AFTER enrolment
// arrives, closed the session and left the tab standing — a person was left
// with a tab that had nothing behind it, for exactly the failures that had
// built the MOST (delegation, mark-live, attach-supervision), because those
// are the ones internal/workers reaches through the Spawned interface rather
// than through workerSpawner's own locals.
type spawnedParticipant struct {
	tabID    string
	sess     session.Session
	sessions sessionCloser
	// layout is asked for tabID's removal. It is the same paneMinter Spawn
	// itself was given — one owner of "undo a spawn's tab" rather than two,
	// which is why compensateSpawn below no longer calls DeleteTab itself.
	layout paneMinter
	// delivery is what became of the task (nocx-f545a.3), set by Spawn and
	// read once by the registration through workers.TaskDeliverer.
	delivery workers.TaskDelivery
	// owed is dropped for this participant's session in Kill, below, so a
	// spawn that never becomes a supervised participant — compensated before
	// workerSupervisor.Attach ever runs — does not leave a debt nothing will
	// ever clear (nocx-f545a.7, owedTasks' own doc).
	owed *owedTasks
}

// TaskDelivery is how the registration that started this participant learns
// what became of its task, without anybody writing it into the record.
func (s spawnedParticipant) TaskDelivery() workers.TaskDelivery { return s.delivery }

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
// journal. It undoes BOTH things Spawn may have built: the session, if one is
// still open, and the tab, always — the two-step undo compensateSpawn used to
// own alone, now the single implementation both callers reach (see the type
// doc).
//
// IDEMPOTENT, because it has two callers that can both reach the same
// participant: compensateSpawn already runs it (through this same method,
// below) for a failure Spawn catches itself, before returning a Spawned to
// internal/workers.Registrar at all — and a compensation that itself fails
// is, by the package's own documented asymmetry, left non-terminal and
// RETRIED, which calls Kill again over what the first attempt already
// removed. So each half asks before it acts rather than reporting a second
// party's tidying as a failure of its own: a session already gone from the
// registry is not re-closed, and DeleteTab on a tab already out of the
// window is already a no-op on the content side (sqliteContent.DeleteTab
// finds no row and returns nil) — this only needs to add the matching
// tolerance for the session half.
//
// THE CTX IT RUNS ON IS ITS OWN, NOT THE CALLER'S (nocx-4gj5w). Both callers
// reach Kill only after their own budget for making the spawn succeed is
// already spent: compensateSpawn below hands it Spawn's own ctx, and
// awaitFreeText spends the whole of that budget waiting before returning —
// so by the time a failed task delivery compensates, the ctx it is handed
// can already be context.DeadlineExceeded, and DeleteTab used to fail with
// exactly that, leaving the tab and its session behind for a person to find
// and a coordinator to close by hand. internal/workers.Registrar.compensate
// is no different: it hands Kill the OUTER ctx Register was called with,
// which a caller may have cancelled (or which may have expired for the same
// reason) by the time a LATE failure — at delegation, mark-live or
// attach-supervision — is discovered. Either way, undoing what Spawn built
// is the one thing that must still run to completion once the decision to
// undo it is made, so killContext below carries the parent's VALUES but
// never its cancellation or deadline, for the identical reason
// detachedFinishContext does one layer up
// (internal/assistant/attempt_dispatch.go, nocx-uhii1): "inherits nothing"
// and "inherits the deadline" are both wrong here.
func (s spawnedParticipant) Kill(ctx context.Context) error {
	ctx, cancel := killContext(ctx)
	defer cancel()
	if s.sess != nil {
		// Dropped unconditionally and first: a session that is about to be
		// killed owes nothing more, whether or not this call's own session
		// close succeeds below — a debt left standing over a session already
		// gone would never be cleared by anything else.
		s.owed.drop(s.sess.ID())
	}
	var errs []error
	if s.sess != nil {
		if _, getErr := s.sessions.Get(s.sess.ID()); getErr == nil {
			if closeErr := s.sessions.Close(s.sess.ID()); closeErr != nil {
				errs = append(errs, fmt.Errorf("close session: %w", closeErr))
			}
		}
		// Already gone: the registry has no row to close, and asking it to
		// close one anyway would report as a failure of THIS call something
		// another close (ours, on a retry, or the ordinary exit path) already
		// did.
	}
	if s.tabID != "" && s.layout != nil {
		if delErr := s.layout.DeleteTab(ctx, s.tabID, killReplacement()); delErr != nil {
			errs = append(errs, fmt.Errorf("delete tab: %w", delErr))
		}
	}
	return errors.Join(errs...)
}

// killTimeout bounds Kill's own cleanup calls, once it has stopped trusting
// the caller's context for anything but its VALUES (see killContext). A
// package var and not a const, exactly like
// internal/assistant/attempt_dispatch.go's finishAttemptTimeout, so a test
// can shorten it and prove the bound actually applies without waiting out
// the production value.
var killTimeout = 10 * time.Second

// killContext derives the context Kill's own cleanup calls run under, from
// whatever context a caller hands it. See Kill's own doc for why neither
// caller's ctx may be trusted here, and detachedFinishContext
// (internal/assistant/attempt_dispatch.go) for the identical derivation one
// layer up in the stack, over the identical reasoning: it carries the
// parent's VALUES (context.WithoutCancel) but never its cancellation or
// deadline, and is bounded instead by ITS OWN short timeout, so a wedged
// store cannot hang Kill in the caller's place now that the caller's own ctx
// can no longer do that job either.
func killContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), killTimeout)
}

// killReplacement mints the identity content.Replacement wants in hand for
// the case where the tab Kill is deleting turns out to be the last one left
// in the whole window.
//
// content.Replacement's own doc names the pattern: "a caller that always
// passes one is not asking for a tab it will not get" — mintReplacementIfEmpty
// consults it ONLY when the delete would otherwise leave the window with no
// tab anywhere, and ignores it on every other close. Kill cannot know in
// advance which this is: the ordinary case is that it is not (the coordinator
// that spawned this participant is running in a tab of its own, which the
// delete leaves standing), but a compensation that assumed so and skipped the
// replacement would fail with content's own ErrNoReplacement on the rare
// window where it is wrong — and a failed DeleteTab here is not a warning
// like the rest of Kill's failures are permitted to be: for the early
// failures compensateSpawn covers it is logged and swallowed, but for the
// late ones Registrar.compensate reaches through Kill it is joined into the
// registration's own error and the record is left NON-TERMINAL, exactly the
// state nocx-4l2a5.4's asymmetry says a failed compensation must be (see
// Kill's own doc for why that is retried rather than papered over). This was
// found, not designed in from the start: unifying Kill with compensateSpawn
// exposed it, because the tab this method now always deletes was previously
// only ever deleted by compensateSpawn's own early-failure paths, where
// production topology (a coordinator tab already open) had never let the
// case arise in a test.
//
// Minting can fail for the same reason Spawn's own minting can — an
// exhausted or broken randomness source — and Kill must still make its best
// effort rather than abandoning the whole undo over it: an empty Replacement
// on that double failure costs nothing unless this tab really is the last
// one AND minting failed, which is the same rare case squared.
func killReplacement() content.Replacement {
	tabID, tabErr := uuid.NewV7()
	paneID, paneErr := uuid.NewV7()
	if tabErr != nil || paneErr != nil {
		return content.Replacement{}
	}
	return content.Replacement{TabID: tabID.String(), PaneID: paneID.String()}
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
	// WHERE THE PARTICIPANT STANDS, resolved ONCE and used twice (nocx-ty5ks):
	// the pane's row records it, so a restore reopens the tab where it was,
	// and the open below starts the program there. Two writes of one answer,
	// never two answers — reading it a second time at the open could differ
	// from what the row says, and the row is what a person sees afterwards.
	cwd := s.coordinatorCwd(ctx, req.CoordinatorSession, lg)
	madeTab, tabErr := s.layout.CreateTab(ctx,
		content.Tab{ID: tabID.String(), WorkspaceID: s.workspace, Layout: content.LayoutRow},
		content.Pane{ID: paneID.String(), TabID: tabID.String(), Cwd: cwd, Kind: content.PaneLocal, SizeShare: 1},
	)
	if tabErr != nil {
		return nil, fmt.Errorf("worker spawn: minting the participant's tab: %w", tabErr)
	}
	lg.Debug("worker spawn: the participant's tab exists",
		"tab_id", tabID.String(), "pane_id", paneID.String(), "workspace", s.workspace)

	opened, err := s.opener.OpenSession(ctx, transport.OpenSpec{
		PaneID: paneID.String(),
		Cols:   participantCols,
		Rows:   participantRows,
		Cwd:    cwd,
	})
	if err != nil {
		s.compensateSpawn(ctx, tabID.String(), nil)
		return nil, fmt.Errorf("worker spawn: opening the participant's session: %w", err)
	}
	lg = lg.With("session_id", string(opened.Session.ID()), "pane_id", paneID.String())
	lg.Debug("worker spawn: the participant's session is open",
		"cols", participantCols, "rows", participantRows)
	spawned := spawnedParticipant{
		tabID: tabID.String(), sess: opened.Session, sessions: s.sessions, layout: s.layout, owed: s.owed,
	}

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
		delivery, deliverErr := s.deliverTask(ctx, string(opened.Session.ID()), req.Task)
		if deliverErr != nil {
			s.compensateSpawn(ctx, tabID.String(), opened.Session)
			return nil, fmt.Errorf("worker spawn: %w", deliverErr)
		}
		// A pane that asked a question keeps its tab, its session and its
		// place in the registration: the participant goes live with its task
		// untyped, and the caller is told so (nocx-f545a.3).
		spawned.delivery = delivery
		if delivery.Typed {
			lg.Info("worker participant given its task", "bytes", len(req.Task))
		}
	}
	// TOLD LAST, after every compensation-worthy step has passed. A
	// notification sent any earlier could announce a tab that the next
	// failure deletes moments later (nocx-ui8q6.3) — the same ordering
	// discipline the doc above states for the pane row itself.
	if s.announce != nil {
		s.announce.AnnounceWorkerTab(madeTab.Object.Tab, madeTab.Object.FirstPane, opened.Session)
	}
	return spawned, nil
}

// coordinatorCwd is the directory a participant's pane opens in: the one the
// coordinator's own pane is standing in (nocx-ty5ks).
//
// It walks session -> pane -> the layout row, and every rung of that walk can
// legitimately be empty, so every one of them answers "" rather than failing
// the spawn. Empty means the session registry's own fallback, which is the
// user's home — what every participant got before this existed, and a worse
// answer than the coordinator's directory but not a wrong one. A spawn is not
// worth refusing over a directory: an agent that starts in the wrong place can
// be told to move, and one that never started cannot.
//
// WHAT IT DOES NOT DO is derive a cwd of its own. The renderer owns the
// verified answer (AD-5) and writes it through SetPaneCwd; this reads that row
// and carries it. A pane nobody has reported a cwd for has no cwd here either,
// and inventing one — the session's own opening directory, the process's,
// $PWD — would be the second owner AGENTS.md's "look for the existing answer"
// rule is about.
func (s *workerSpawner) coordinatorCwd(ctx context.Context, coordinator string, lg log.Logger) string {
	if coordinator == "" || s.sessions == nil || s.layout == nil {
		return ""
	}
	sess, err := s.sessions.Get(session.ID(coordinator))
	if err != nil {
		lg.Debug("worker spawn: the coordinator's session is not held here, so its directory is unknown",
			"coordinator_session", coordinator, "error", err)
		return ""
	}
	paneID := sess.PaneID()
	if paneID == "" {
		lg.Debug("worker spawn: the coordinator's session belongs to no pane, so its directory is unknown",
			"coordinator_session", coordinator)
		return ""
	}
	cwd, err := s.layout.PaneCwd(ctx, paneID)
	if err != nil {
		lg.Debug("worker spawn: the coordinator's pane has no recorded directory",
			"coordinator_session", coordinator, "pane_id", paneID, "error", err)
		return ""
	}
	if cwd == "" {
		lg.Debug("worker spawn: the coordinator's pane has never reported a directory",
			"coordinator_session", coordinator, "pane_id", paneID)
		return ""
	}
	lg.Debug("worker spawn: the participant opens where its coordinator is",
		"coordinator_session", coordinator, "pane_id", paneID, "cwd", cwd)
	return cwd
}

// deliverTask waits for paneID to become typable and submits task into it.
//
// Nil readiness or nil typist is the ABSENCE case, exactly like
// integrationAwaiterSeam's above: a spawner nobody wired an observation or a
// typist into cannot ask whether a pane is ready any more than it could ask
// whether a domain accepted, so the spawn proceeds rather than hanging or
// refusing a question it was never asked. Production always wires both.
func (s *workerSpawner) deliverTask(ctx context.Context, paneID, task string) (workers.TaskDelivery, error) {
	if s.readiness == nil || s.typist == nil {
		s.log.Debug("worker spawn: no pane-typing seam is wired; the task will not be typed at spawn",
			"pane_id", paneID)
		return workers.TaskDelivery{}, nil
	}
	state, waitErr := awaitFreeText(ctx, s.readiness, paneID, s.log, "worker spawn")
	if waitErr != nil {
		var never *workers.PaneNeverTypable
		if errors.As(waitErr, &never) {
			return workers.TaskDelivery{}, waitErr
		}
		return workers.TaskDelivery{}, fmt.Errorf("%w: %s", workers.ErrPaneNeverTypable, waitErr)
	}
	if state != agentdriver.StateFreeText {
		// The pane is asking something. Nothing is typed into a question —
		// the gate would refuse it anyway, and answering it is the caller's
		// choice under ADR-0064, never nocx's. The debt is owed from here
		// until workerAnswerer pays it, the session ends, or the backend
		// restarts (owedTasks' own doc).
		s.log.Info("worker spawn: the participant's pane is asking a question, so its task was not typed",
			"pane_id", paneID, "state", string(state))
		s.owed.mark(session.ID(paneID))
		return workers.TaskDelivery{WaitingOn: string(state)}, nil
	}
	res := s.typist.Submit(paneID, task)
	if res.Outcome == agenttyping.OutcomeSubmitted {
		return workers.TaskDelivery{Typed: true}, nil
	}
	// OutcomeTyped is a refusal here too, not a delivery: the text reached
	// the input region and the submit key did not, so no turn started and the
	// coordinator's task never reached the agent — exactly the distinction
	// workerWaker's own doc draws for the identical outcome on a wake.
	reason := res.Reason
	if reason == "" {
		reason = fmt.Sprintf("nocx refused to submit the task (%s)", res.Outcome)
	}
	return workers.TaskDelivery{}, fmt.Errorf("%w: %s", workers.ErrTaskSubmitRefused, reason)
}

// compensateSpawn undoes what Spawn built so far, for a failure Spawn catches
// itself — before a Spawned is ever handed back for
// internal/workers.Registrar.compensate() to reach through Kill instead.
//
// It is now a THIN CALLER OF Kill (nocx-ui8q6.7) rather than a second
// implementation of "undo a spawn" beside it: the two used to disagree about
// what that meant — this one undid the session and the tab, Kill undid only
// the session — and the disagreement fell on exactly the failures that had
// built the most, because those are the ones reaching Kill rather than this
// helper. sess may be nil (OpenSession itself never returned one); Kill
// already treats that as "nothing to close" rather than a nil dereference.
//
// The failure is only logged. Spawn's own error already says what to report
// to the caller, and a compensation that also fails is not a second verdict
// on top of it — it is a warning worth having, the same asymmetry Kill's
// other caller (Registrar.compensate) resolves differently, by joining the
// two errors instead: that caller has no Spawn error of its own to prefer,
// this one does.
func (s *workerSpawner) compensateSpawn(ctx context.Context, tabID string, sess session.Session) {
	sp := spawnedParticipant{tabID: tabID, sess: sess, sessions: s.sessions, layout: s.layout, owed: s.owed}
	if err := sp.Kill(ctx); err != nil {
		s.log.Warn("worker spawn: could not fully compensate a failed spawn",
			"tab_id", tabID, "error", err)
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
	// owed is dropped in report, below, for the participant whose exit is
	// being reported (nocx-f545a.7): a process that is gone owes nobody a
	// task, and this is the closing event for a debt that survives even a
	// human takeover — see owedTasks' own doc for the whole interval.
	owed *owedTasks
	log  log.Logger
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
	// Dropped first and unconditionally: the process this participant was is
	// gone whether or not anything downstream is wired to hear about it, and
	// a debt over a gone process is a debt nothing will ever pay.
	s.owed.drop(session.ID(p.Liveness.SessionID))
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

// workerScreener is the composition root's half of workers.screen
// (nocx-f545a.6): the grid a participant's pane is kept in, and the watcher
// that says what nocx reads it as.
//
// The rows are panegrid.Frame.Text, right-trimmed — the one row renderer the
// grid already offers and the one a rule's predicates read rows through — so
// a coordinator is shown the rows nocx itself reasons about, not a second
// rendering of them (ADR-0064 §2).
type workerScreener struct {
	grid  panegrid.Observer
	watch paneReadiness
}

func (s *workerScreener) ReadScreen(_ context.Context, p workers.Participant) (workers.PaneScreen, error) {
	sid := p.Liveness.SessionID
	if sid == "" || s.grid == nil {
		return workers.PaneScreen{}, nil
	}
	f, err := s.grid.Frame(sid)
	if err != nil {
		// The observation closed, or never opened: no reading, and an answer
		// that says so rather than an error, exactly as agent.emitting
		// answers the same race.
		return workers.PaneScreen{}, nil //nolint:nilerr // an absent grid is an answer, not a failure
	}
	rows := make([]string, 0, len(f.Lines))
	for y := range f.Lines {
		rows = append(rows, strings.TrimRight(f.Text(y), " "))
	}
	out := workers.PaneScreen{Readable: true, Rows: rows}
	if s.watch != nil {
		if o, ok := s.watch.Snapshot(sid); ok {
			out.State = string(o.State)
		}
	}
	return out, nil
}

// paneChooser is the app's narrow view of the menu half of the typing gate
// (nocx-f545a.4). It is a second one-method interface beside paneTypist rather
// than a second method on it, so every double that only ever submitted text
// goes on satisfying what it satisfied.
type paneChooser interface {
	Choose(paneID, option string) agenttyping.Result
}

// workerAnswerer is the composition root's half of workers.answer
// (nocx-f545a.4, ADR-0064 §1): the typing gate that decides every key, and the
// grid it waits on between a movement and its confirm.
//
// THE WAIT IS HERE AND NOT IN THE GATE. A TUI repaints after it reads its
// input, so the frame read straight after a movement key can still show the
// old selection. agenttyping.Choose therefore moves OR confirms, never both on
// one belief; this waits for the screen to show the selection on the named
// option — asked through agenttyping.ReadMenu, the same reading Choose
// confirms against — and only then chooses again, which confirms from that
// frame. Choosing again on a stale frame would move a second time and overshoot.
type workerAnswerer struct {
	grid   panegrid.Observer
	typist paneChooser
	// owed and typing are what pays a task this participant's spawn left
	// owed, once THIS answer is the one that confirms the question it was
	// waiting on (nocx-f545a.7). classify is the third: nil on all of them
	// is the ordinary absence case every optional seam in this file already
	// uses — nothing is paid, and Answer behaves exactly as it did before
	// this bead.
	owed *owedTasks
	// classify is a LIVE reading, never the watcher's cache — see
	// paneClassifier's own doc for why typeOwedTask cannot use the same
	// paneReadiness deliverTask reads at spawn.
	classify paneClassifier
	// typing is the same *agenttyping.Typist as typist above, reached
	// through Submit rather than Choose: the one gate, never a second door
	// onto this pane's input queue, exactly as workerWaker and deliverTask
	// already share it.
	typing paneTypist
	log    log.Logger
}

func (a *workerAnswerer) Answer(ctx context.Context, p workers.Participant, option string) (workers.PaneAnswer, error) {
	sid := p.Liveness.SessionID
	if sid == "" || a.grid == nil || a.typist == nil {
		return workers.PaneAnswer{}, errors.New("worker answer: this participant has no pane nocx can answer")
	}
	res := a.typist.Choose(sid, option)
	if res.Outcome != agenttyping.OutcomeTyped {
		return a.withOwedTask(ctx, p, option, res), nil
	}
	if err := awaitSelectionOn(ctx, a.grid, sid, option); err != nil {
		res.Reason = "the selection was moved and the menu never showed it on that option, so nothing was confirmed (" + err.Error() + ")"
		return paneAnswerOf(res), nil
	}
	return a.withOwedTask(ctx, p, option, a.typist.Choose(sid, option)), nil
}

// withOwedTask turns the gate's own result into the answer, and — only when
// that result just CONFIRMED a selection — pays a task this participant's
// pane was left owing, if one still is (nocx-f545a.7).
//
// ADR-0064 §4 is why this decides it here rather than reading it off the
// record: a screen reading assigns no status to a participant and a menu
// answer moves no record, so "this task is still owed" was never something
// workers.Store could be asked — it lives in owed, this call's own in-memory
// debt, and take is what makes paying it happen at most once even under two
// concurrent Answer calls racing the same participant.
func (a *workerAnswerer) withOwedTask(ctx context.Context, p workers.Participant, option string, res agenttyping.Result) workers.PaneAnswer {
	ans := paneAnswerOf(res)
	if res.Outcome != agenttyping.OutcomeSubmitted || a.classify == nil || a.typing == nil {
		// No seam to pay a debt with is the same absence case as everywhere
		// else in this file — never a reason to dereference one.
		return ans
	}
	sid := session.ID(p.Liveness.SessionID)
	if !a.owed.take(sid) {
		return ans
	}
	ans.Task = a.typeOwedTask(ctx, sid, option, p.Task)
	return ans
}

// answerTaskBudget bounds how long typeOwedTask waits — both for the
// confirmed menu to leave the screen and, once it has, for the pane to reach
// free_text — after THIS answer confirmed, once the confirmation itself is
// already committed (nocx-f545a.7).
//
// It must stay under workers.answer's own declared Deadline
// (internal/agenttools/registry.go, 30s) so a pane that never becomes
// typable inside it is answered as itself — "waiting", with the state the
// wait ended on — rather than the whole tool call timing out and reporting a
// failure for an answer nocx actually confirmed. A package var and not a
// const, exactly like killTimeout above, so a test can shorten it and prove
// the bound applies without waiting out the production value — the test
// still asserts on the STATE the answer reports, never on how long it took.
var answerTaskBudget = 20 * time.Second

// typeOwedTask pays sid's debt, under answerTaskBudget, and submits task
// through the SAME gate a spawn's own delivery and a wake both reach.
//
// FRESHNESS IS TWO FACTS, NOT A DELAY (nocx-f545a.7, a review of an earlier
// version of this bead that tried the delay and left the race open — see
// awaitFreeText's own doc for the full account). First: the menu THIS answer
// just confirmed has actually left the screen — awaitMenuLeftScreen polls
// the grid for the SAME reading Choose's own confirm decided against, so
// "the menu is still showing" is asked once, the mirror of
// awaitSelectionOn's own wait rather than a second answer to it. Second:
// once it has, the pane's state is read LIVE (paneClassifier.Classify),
// never from the watcher's cache — Snapshot's own cache is exactly what the
// first fact already proved cannot be trusted here. Only past both does
// awaitFreeText run, over classifyingReadiness's adapter onto that live
// reading.
//
// Every branch below either pays the debt (Delivery: "typed") or restores it
// for a later answer to try again — except the one case where restoring
// would be wrong: the participant's agent has exited, and there will be no
// later answer to pay it with.
func (a *workerAnswerer) typeOwedTask(ctx context.Context, sid session.ID, option, task string) *workers.TaskOutcome {
	subCtx, cancel := context.WithTimeout(ctx, answerTaskBudget)
	defer cancel()

	if err := awaitMenuLeftScreen(subCtx, a.grid, string(sid), option); err != nil {
		// The sub-budget ran out with the confirmed menu still on screen —
		// a slow repaint, or a screen nocx cannot read at all. Either way
		// this is an answer, not a failure: whatever Classify says right now
		// is what a coordinator would see by looking, so that is what is
		// reported, and the debt is restored for a later answer to try again.
		a.owed.restore(sid)
		o, _ := a.classify.Classify(string(sid))
		return &workers.TaskOutcome{Delivery: "waiting", State: string(o.State)}
	}

	state, waitErr := awaitFreeText(subCtx, classifyingReadiness{classify: a.classify}, string(sid), a.log, "worker answer")
	if waitErr != nil {
		var never *workers.PaneNeverTypable
		if errors.As(waitErr, &never) {
			a.owed.restore(sid)
			return &workers.TaskOutcome{Delivery: "waiting", State: never.State, Reason: never.Detail}
		}
		// The agent exited: the session is ending, so the debt is not
		// restored (owedTasks' own doc, closing event (b)) — nothing will
		// ever answer for this participant again.
		return &workers.TaskOutcome{Delivery: "refused", Reason: waitErr.Error()}
	}
	if state != agentdriver.StateFreeText {
		// The pane is asking something ELSE now. Answering THAT question is
		// what pays this debt next — see workers.answer's own description.
		a.owed.restore(sid)
		return &workers.TaskOutcome{Delivery: "waiting", State: string(state)}
	}
	res := a.typing.Submit(string(sid), task)
	if res.Outcome == agenttyping.OutcomeSubmitted {
		return &workers.TaskOutcome{Delivery: "typed"}
	}
	a.owed.restore(sid)
	reason := res.Reason
	if reason == "" {
		reason = fmt.Sprintf("nocx refused to submit the task (%s)", res.Outcome)
	}
	return &workers.TaskOutcome{Delivery: "refused", State: string(res.State), Reason: reason}
}

// menuSelectedOn reads paneID's CURRENT frame and reports whether it shows a
// menu with its selection on option — live, off the grid, never the
// watcher's cache: a TUI repaints after it reads input, so what decides a
// movement, a confirm, or (nocx-f545a.7) whether a just-confirmed menu is
// still showing must be what the screen shows right now. Shared by
// awaitSelectionOn and awaitMenuLeftScreen so "is the menu on this option"
// is decided once rather than twice.
func menuSelectedOn(grid panegrid.Observer, paneID, option string) bool {
	f, err := grid.Frame(paneID)
	if err != nil {
		return false
	}
	m := agenttyping.ReadMenu(f)
	i := m.Index(option)
	return i >= 0 && m.Selected == i
}

// awaitSelectionOn blocks until paneID's screen shows a menu whose selection is
// on option, or until ctx ends.
func awaitSelectionOn(ctx context.Context, grid panegrid.Observer, paneID, option string) error {
	if menuSelectedOn(grid, paneID, option) {
		return nil
	}
	ticker := time.NewTicker(deliveryPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if menuSelectedOn(grid, paneID, option) {
				return nil
			}
		}
	}
}

// awaitMenuLeftScreen blocks until paneID's screen NO LONGER shows a menu
// with its selection on option, or until ctx ends. It is the mirror of
// awaitSelectionOn, over the identical predicate: that one waits for a
// reading to become true, this one waits for the SAME reading to become
// false, which is the first of the two facts typeOwedTask establishes
// freshness from (nocx-f545a.7) — the confirmed menu has actually left the
// screen, rather than the wait merely having let some time pass.
func awaitMenuLeftScreen(ctx context.Context, grid panegrid.Observer, paneID, option string) error {
	if !menuSelectedOn(grid, paneID, option) {
		return nil
	}
	ticker := time.NewTicker(deliveryPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !menuSelectedOn(grid, paneID, option) {
				return nil
			}
		}
	}
}

func paneAnswerOf(r agenttyping.Result) workers.PaneAnswer {
	return workers.PaneAnswer{Outcome: string(r.Outcome), State: string(r.State), Reason: r.Reason}
}

// paneClassifier is the answerer's narrow view of a LIVE classification
// (nocx-f545a.7, a review of 1ffd3a56): read paneID's CURRENT frame and
// classify it now, never from a cache. It is satisfied by
// *paneobserve.Watcher's Classify method, whose own doc has the full
// argument; the short version is that paneReadiness.Snapshot (deliverTask's
// own seam, above) answers from the watcher's cache, which only changes on
// a Sweep, and nothing about writing a confirm key into a pane marks it
// dirty — so the instant after workerAnswerer confirms a menu, Snapshot is
// guaranteed to still describe the menu that was just confirmed. Only the
// answer path needs this: deliverTask's first observation at spawn is never
// stale, because nothing preceded it.
type paneClassifier interface {
	Classify(paneID string) (paneobserve.Observation, bool)
}

// classifyingReadiness adapts a paneClassifier into the paneReadiness seam
// awaitFreeText already takes, so the answer path reuses that ONE wait
// rather than writing a second one — only the reading it is handed differs
// from deliverTask's.
type classifyingReadiness struct {
	classify paneClassifier
}

func (c classifyingReadiness) Snapshot(paneID string) (paneobserve.Observation, bool) {
	return c.classify.Classify(paneID)
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
