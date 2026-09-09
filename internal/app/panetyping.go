package app

import (
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
)

// The two halves of the typing seam the composition root owns, because it is
// the only place that holds both: the observation, which is where the agent a
// pane was enrolled under comes from, and the session registry, which is where
// a pane's input queue is (nocx-dkawo.1).
//
// Neither decides anything. What one hands out is an agent name and what the
// other hands out is "the queue took it"; both decisions the AD-6 amendment
// permits are made in internal/agenttyping, on frames it reads itself.

// paneAgents answers which agent a pane was enrolled under, from the
// enrolment act rather than from anything a caller said.
//
// It reads the WATCH list rather than a classification, so it is true from the
// instant a pane is enrolled — a settled screen may go a long time without a
// sweep having anything to say, and a typist that waited for one would refuse
// every pane that has not moved since it started.
type paneAgents struct{ watch *paneobserve.Watcher }

func (p paneAgents) AgentOn(paneID string) (string, bool) {
	for _, w := range p.watch.Watching() {
		if w.PaneID == paneID {
			return w.Agent, true
		}
	}
	return "", false
}

// paneInput puts bytes on a pane's own input queue — the same one every
// keystroke travels.
//
// EnqueueWrite and deliberately not Write: Write is the backend's path PAST
// the bootstrap window's input quarantine (internal/session/bootstrap_window.go),
// and a byte that behaves like a keystroke must be subject to what keystrokes
// are subject to — the quarantine, the queue's bound, this session and no
// other. It is the same choice session.signal's protected-group interrupt
// makes, for the same reason.
//
// False means the queue refused it and nothing was written. True means it was
// ACCEPTED, which is not written: the channel write happens later on the write
// loop and can still fail.
type paneInput struct{ registry session.Registry }

func (p paneInput) Accept(paneID string, b []byte) bool {
	sess, err := p.registry.Get(session.ID(paneID))
	if err != nil {
		return false
	}
	return sess.EnqueueWrite(b)
}

// newPaneTypist assembles the typing primitive from the seams above and the
// two the grid already owns.
func newPaneTypist(
	lg log.Logger, screens agenttyping.Screens, rules agenttyping.Rules,
	calib agenttyping.Authority, watch *paneobserve.Watcher, registry session.Registry,
) *agenttyping.Typist {
	return agenttyping.New(lg, screens, rules, calib,
		paneAgents{watch: watch}, paneInput{registry: registry})
}
