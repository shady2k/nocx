package transport

// The parked run (nocx-6dzxq): a command the quiet bound asked the model
// about, still running, with nobody currently waiting on its answer.
//
// WHY THE QUESTION TRAVELS AS A TOOL RESULT. The model learns everything
// through tool results, and a tool call is the only moment the backend holds
// the model's attention. The alternative considered and refused was a new
// mid-run push — a backend→model channel that would interrupt the stream
// with a question — and it was refused for two reasons: it would be a second
// way for the backend to speak to the model beside the tool loop that
// already exists (AD-8), and it would have to be built into every carrier
// separately, while a tool result is carrier-neutral by construction. So the
// attempt ENDS, saying the command is still running and naming the handle to
// continue with, and the continuation is an ordinary tool call
// (session.wait).
//
// WHAT "KEEP WAITING" IS, MECHANICALLY. It is a second call that RE-ATTACHES
// to the same broker request, never a re-submission: the renderer was never
// told to cancel, the pending request keeps its recipients, and the command
// in the pane has been running unbroken the whole time. A renewed lease over
// a re-run command would be a different command with the same text, which is
// exactly what the bead forbids — `df` would be started twice against the
// stuck mount.
//
// WHERE THE CEILING IS ENFORCED, IN ONE PLACE. The lease's own wall timer,
// armed once in supervise at the first submission and never re-armed by any
// continuation. A park does not disarm it (run_lease.go's supervise), and a
// resumption only re-opens the QUIET interval (resumeQuiet). So a model that
// answers "keep waiting" a hundred times still meets the person's number at
// the same instant it would have met it if it had never been asked, and
// nothing here counts renewals in order to stop them — there is no number to
// get wrong.
//
// AND A PARKED RUN IS STILL BOUNDED WHEN NOBODY COMES BACK. A model that is
// asked and simply never calls again leaves the run parked; the wall timer
// is still armed, so the ceiling terminalizes it exactly as it would have
// terminalized a run somebody was waiting on. That is the whole reason the
// lease is not torn down at the park.

import (
	"context"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

// parkedRun is one run the model was asked about and has not yet answered
// for. It holds what a continuation needs and nothing else: the lease (which
// owns the bounds, including the still-running ceiling), the kind (to
// withdraw with the right cancel notification) and the session.
type parkedRun struct {
	sid       session.ID
	requestID string
	kind      RequestKind
	lease     *runLease
}

// parkedRunRegistry is the WSServer's set of parked runs. Keyed by the
// broker request id, which is the handle the model is given: it is already
// minted, already bounded (maxIDRunes), and already the identity the
// renderer and the lifecycle attempt bridge correlate on, so there is no
// second id to invent or to keep in step.
type parkedRunRegistry struct {
	mu   sync.Mutex
	runs map[string]*parkedRun
}

func (r *parkedRunRegistry) put(p *parkedRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = make(map[string]*parkedRun)
	}
	r.runs[p.requestID] = p
}

func (r *parkedRunRegistry) take(requestID string) (*parkedRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.runs[requestID]
	if ok {
		delete(r.runs, requestID)
	}
	return p, ok
}

func (r *parkedRunRegistry) remove(requestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, requestID)
}

// parkRun records a run whose quiet bound asked the model, and installs the
// lease's parked termination path. From here until a continuation takes the
// run back, a bound that fires does not merely cancel a context nobody is
// selecting on: it withdraws the request and runs the escalation itself.
func (s *WSServer) parkRun(sid session.ID, requestID string, kind RequestKind, lease *runLease) {
	p := &parkedRun{sid: sid, requestID: requestID, kind: kind, lease: lease}
	s.parkedRuns.put(p)
	// The closure holds the LEASE, not a registry key, so an ending never
	// depends on a lookup that a concurrent continuation may already have
	// consumed. A ceiling that fires in the instant a "keep waiting" is
	// taking the run back still withdraws the request and still kills the
	// command; the continuation then finds the lease already fired and says
	// so, which is the only outcome either party can honestly report.
	lease.setParkedEnd(func(reason content.TerminationReason) foregroundOutcome {
		return s.finishParkedRun(requestID, lease, reason)
	})
}

// finishParkedRun is the ONE ending of a parked run: withdraw the broker
// request (so a late resolution cannot report the run completed), close the
// attempt bridge, disarm the lease and run the established INT → TERM → KILL
// ladder against the execution's own process group.
//
// It takes the lease rather than looking one up, so it is correct whether or
// not the registry still holds the run.
func (s *WSServer) finishParkedRun(requestID string, lease *runLease, reason content.TerminationReason) foregroundOutcome {
	_, outcome := s.finishParkedRunDetailed(requestID, lease, reason)
	return outcome
}

func (s *WSServer) finishParkedRunDetailed(requestID string, lease *runLease, reason content.TerminationReason) (*assistant.RunLeaseError, foregroundOutcome) {
	s.parkedRuns.remove(requestID)
	lease.setParkedEnd(nil)
	leaseErr := &assistant.RunLeaseError{Reason: reason, Err: context.Canceled}
	if attempt, ok := s.broker.runAttemptForLease(lease); ok {
		leaseErr.EntryID = attempt
	}
	s.broker.withdrawParked(requestID, leaseErr)
	s.broker.unregisterRunLease(lease)
	lease.disarm()
	return leaseErr, lease.escalate()
}

// endParkedRun terminalizes a parked run: the broker request is withdrawn
// (the renderer is told to cancel), the lease is disarmed, and the
// established INT → TERM → KILL ladder runs against the execution's own
// process group. It is the ONE ending for every reason a parked run can have
// one — the wall-clock ceiling firing, the model answering "stop", the
// person cancelling the turn — so those three cannot drift into three
// policies.
//
// The withdrawal precedes the escalation for the reason supervise's does: a
// late resolution must not win the race and report the run completed.
func (s *WSServer) endParkedRun(requestID string, reason content.TerminationReason) (*assistant.RunLeaseError, foregroundOutcome, bool) {
	p, ok := s.parkedRuns.take(requestID)
	if !ok {
		return nil, foregroundNothingRunning, false
	}
	leaseErr, outcome := s.finishParkedRunDetailed(requestID, p.lease, reason)
	return leaseErr, outcome, true
}

// resumeParkedRun is the "keep waiting" half of session.wait: the lease's
// quiet interval re-opens under the bound this continuation asked for, the
// parked termination path is handed back to the ordinary caller-waiting one,
// and the SAME broker request is waited on again.
//
// The wall clock is deliberately absent from every line of this function.
func (s *WSServer) resumeParkedRun(requestID string, asked time.Duration) (*parkedRun, RunLeaseConfig, bool, chan struct{}, bool) {
	p, ok := s.parkedRuns.take(requestID)
	if !ok {
		return nil, RunLeaseConfig{}, false, nil, false
	}
	// The person's CURRENT quiet bound, read again: a continuation is a new
	// run of the wait, and the next wait is bound by whatever the setting
	// says now. The ceiling is not re-read — it is already ticking, and
	// re-reading it is the one thing that would let a renewal outlive it.
	cfg, clamped := s.effectiveRunLease().withAskedQuiet(asked)
	if cfg.needsShellIntegration() && !s.runLeaseIntegrationAvailable(p.sid) {
		// The integration that authenticates the output the quiet bound
		// watches is gone. There is no quiet bound to re-open, so this
		// continuation simply waits to the ceiling — which is still armed.
		cfg.Inactivity = 0
	}
	parkC := make(chan struct{})
	// The parked termination path is NOT cleared here. superviseResume
	// clears it in the same critical section that installs the new caller's
	// cancellation (runLease.takeBack), so a bound firing between the two
	// can never find neither.
	p.lease.resumeQuiet(cfg.Inactivity, parkC)
	return p, cfg, clamped, parkC, true
}
