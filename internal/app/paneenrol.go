package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/session"
)

// paneEnroller answers the agent_enrol / agent_withdraw pair by opening and
// closing a pane's backend grid (protocol doc §15, and the AD-6 amendment's
// INTERVAL constraint). It is the composition root's, because it is the only
// place that holds both halves of the answer: the lane→session map the child
// grant builder already uses, and the grid store the transport feeds.
//
// It decides nothing about what is on the screen and could not: what it hands
// out is a grid, and what a grid answers is a Frame. The two decisions the
// amendment permits — may nocx type here, what does the indicator show — are
// made by the callers that read one.
type paneEnroller struct {
	log      log.Logger
	sessions *sessionRegistry
	grid     panegrid.Observer
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
	grid panegrid.Observer,
	watch paneWatcher,
	approval agentApproval,
) (*paneEnroller, error) {
	if approval == nil {
		return nil, errors.New("pane enroller: no agent approval")
	}
	return &paneEnroller{log: lg, sessions: sessions, grid: grid, watch: watch, approval: approval}, nil
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
		e.log.Warn("agent enrolment refused: human approval was not granted",
			"lane", string(lane), "session_id", sid, "agent", agent, "error", err)
		return err
	}
	if err := e.grid.Enrol(sid, cols, rows); err != nil {
		switch {
		case errors.Is(err, panegrid.ErrAlreadyEnrolled):
			// Re-enrolling would discard the grid built so far, and with it
			// the byte-zero guarantee that is the only reason to trust a
			// frame. So it is refused, and the caller says so.
			e.log.Warn("agent enrolment refused: the pane is already watched",
				"lane", string(lane), "session_id", sid, "agent", agent)
			return errors.New("this pane is already being watched")
		case errors.Is(err, panegrid.ErrTooManyEnrolled):
			e.log.Warn("agent enrolment refused: the watch bound is reached",
				"lane", string(lane), "session_id", sid, "agent", agent, "bound", panegrid.MaxEnrolled)
			return fmt.Errorf("nocx is already watching %d panes", panegrid.MaxEnrolled)
		default:
			e.log.Warn("agent enrolment refused",
				"lane", string(lane), "session_id", sid, "agent", agent, "error", err)
			return errors.New("nocx could not start watching this pane")
		}
	}
	// Only now, and only for an enrolment that actually opened a grid.
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
	e.grid.Withdraw(sid)
	// The agent this pane approved is no longer enrolled, so what it was
	// approved AS goes with the interval. The durable answer in the store is
	// untouched: the person permitted an agent, not this one run of it.
	e.approval.Forget(session.ID(sid))
	e.log.Info("agent withdrawn", "lane", string(lane), "session_id", sid)
}
