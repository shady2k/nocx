package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
)

// paneEnroller answers the agent_enrol / agent_withdraw pair by opening and
// opening and closing the observation of a pane (protocol doc §15, and the
// AD-6 amendment's
// INTERVAL constraint). It is the composition root's, because it is the only
// place that holds both halves of the answer: the lane→session map the child
// grant builder already uses, and the store every frame read goes through.
//
// It decides nothing about what is on the screen and could not: what it hands
// out is a grid, and what a grid answers is a Frame. The two decisions the
// amendment permits — may nocx type here, what does the indicator show — are
// made by the callers that read one.
type paneEnroller struct {
	log      log.Logger
	sessions *sessionRegistry
	screens  paneWatchStore
	// watch is the OBSERVATION's end of the same act. It opens and closes
	// with the grid and never before or after it: a pane nocx reports a
	// state for but declined to watch would be a claim with no evidence
	// behind it, and one it watches without reporting is the silent degrade.
	watch    paneWatcher
	approval agentApproval
	// onEnrol is told when an enrolment actually opened a grid, so a worker
	// registration blocked on that enrolment can proceed (nocx-dkawo.7). It
	// is a NOTIFICATION and not a second enroller: it is called after the act
	// succeeded, it cannot refuse one, and a nil hook is the ordinary case —
	// most enrolments are a person running an agent in their own tab and
	// belong to no workers.
	onEnrol func(sessionID, lane string)
}

// paneWatchStore is the enroller's narrow view of the pane store (AD-8): it
// opens an interval and closes one, and may do nothing else with it.
//
// It is an interface and not the store itself because opening an interval is
// the ONE thing this file does with a pane's screen. The store is the
// composition root's object — it is also what the observation sweeps, what the
// typing gate reads and what the authorizer asks about — and an enroller that
// held it could reach every one of those, which is how one behaviour acquires
// a second owner.
type paneWatchStore interface {
	Enrol(paneID string) error
	Withdraw(paneID string)
}

// paneWatcher is the enroller's narrow view of the observation (AD-8): open
// one, close one. It classifies nothing here and could not — what it is handed
// is a pane id and an agent name.
type paneWatcher interface {
	Watch(paneID, agent string)
	Exited(paneID string)
}

type agentApproval interface {
	Approve(context.Context, session.ID, string) error
	// Forget releases what the enrolment approved when the interval closes,
	// so what admits a peer follows the live enrolments (nocx-opiq5).
	Forget(session.ID)
}

func newPaneEnroller(
	lg log.Logger,
	sessions *sessionRegistry,
	screens paneWatchStore,
	watch paneWatcher,
	approval agentApproval,
) (*paneEnroller, error) {
	if approval == nil {
		return nil, errors.New("pane enroller: no agent approval")
	}
	return &paneEnroller{log: lg, sessions: sessions, screens: screens, watch: watch, approval: approval}, nil
}

// Enrol opens the interval for the pane the lane belongs to.
//
// Every refusal returns a sentence rather than a code, because the sentence is
// what the caller prints in the user's own pane. "No enrolment, no
// orchestration, and the pane says so" (D4) is not satisfied by a log line the
// person never sees — that is the silent-degrade shape AGENTS.md names, where
// a feature that does not exist survives a release behind a slog.Warn.
func (e *paneEnroller) Enrol(lane lifecycle.LaneID, agent string, cols, rows int) error {
	sid, ok := e.sessions.lookup(lane)
	if !ok || sid == "" {
		// The lane authenticated but nothing maps it to a session. That is a
		// real state — a domain established before its lane was registered,
		// or a lane whose session has already gone — and it is a refusal
		// rather than a silent no-op.
		e.log.Warn("agent enrolment refused: the lane maps to no session",
			"lane", string(lane), "agent", agent)
		return errors.New("nocx does not know which pane this shell is")
	}
	if err := e.approval.Approve(context.Background(), session.ID(sid), agent); err != nil {
		var pending *lifecyclepub.EnrolmentPending
		if errors.As(err, &pending) {
			// Not a refusal: the shell waits for the answer and enrols again.
			e.log.Info("agent enrolment waits on a person",
				"lane", string(lane), "session_id", sid, "agent", agent, "question", pending.Reason)
			return err
		}
		e.log.Warn("agent enrolment refused: human approval was not granted",
			"lane", string(lane), "session_id", sid, "agent", agent, "error", err)
		return err
	}
	// The size the shell reported is NOT passed on: the runtime beside the PTY
	// already holds the geometry the program is running at, and a second number
	// taken from a shell's report would be a claim about a terminal nobody
	// asked (nocx-ygxjv.3). cols and rows stay in the signature because the
	// authenticated channel carries them and the log line states what was
	// asked for.
	if err := e.screens.Enrol(sid); err != nil {
		switch {
		case errors.Is(err, paneview.ErrAlreadyWatched):
			// A second enrolment is a caller that has lost track of the first.
			// It is refused rather than folded in, and the caller says so:
			// one withdrawal must close the interval, which it cannot do if a
			// second open was silently absorbed.
			e.log.Warn("agent enrolment refused: the pane is already watched",
				"lane", string(lane), "session_id", sid, "agent", agent)
			return errors.New("this pane is already being watched")
		case errors.Is(err, paneview.ErrTooManyWatched):
			e.log.Warn("agent enrolment refused: the watch bound is reached",
				"lane", string(lane), "session_id", sid, "agent", agent, "bound", paneview.MaxWatched)
			return fmt.Errorf("nocx is already watching %d panes", paneview.MaxWatched)
		case errors.Is(err, errNoPaneRuntime):
			// The pane's PTY is not a helper's, so there is no runtime to read
			// and nothing to watch. It is the sentence a person reads in the
			// pane that asked to be enrolled, which is what D4 requires — and
			// nocx-ygxjv.13 is what ends the state that produces it.
			e.log.Info("agent enrolment refused: no helper holds the pane's terminal",
				"lane", string(lane), "session_id", sid, "agent", agent)
			return err
		default:
			e.log.Warn("agent enrolment refused",
				"lane", string(lane), "session_id", sid, "agent", agent, "error", err)
			return errors.New("nocx could not start watching this pane")
		}
	}
	// Only now, and only for an enrolment that actually opened a watch.
	e.watch.Watch(sid, agent)
	if e.onEnrol != nil {
		e.onEnrol(sid, string(lane))
	}
	e.log.Info("agent enrolled", "lane", string(lane), "session_id", sid,
		"agent", agent, "cols", cols, "rows", rows)
	return nil
}

// Withdraw closes the interval. It cannot fail and says nothing about whether
// there was anything to close: a caller racing a session teardown should not
// have to care who won, and this is not the only end of the interval — the
// transport withdraws the same grid when the session's output ends, which is
// the end that covers a caller that was killed rather than returning.
func (e *paneEnroller) Withdraw(lane lifecycle.LaneID) {
	sid, ok := e.sessions.lookup(lane)
	if !ok || sid == "" {
		return
	}
	// The agent withdrawing IS the agent finishing, and it is the one moment
	// nocx knows a process is gone rather than inferring it from a screen.
	// Before the grid closes: what it reports is the pane's last state, and
	// a client attaching afterwards is answered with it.
	e.watch.Exited(sid)
	e.screens.Withdraw(sid)
	// The agent this pane approved is no longer enrolled, so what it was
	// approved AS goes with the interval. The durable answer in the store is
	// untouched: the person permitted an agent, not this one run of it.
	e.approval.Forget(session.ID(sid))
	e.log.Info("agent withdrawn", "lane", string(lane), "session_id", sid)
}
