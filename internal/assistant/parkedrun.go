package assistant

// PARKING ACROSS THE ASK BOUNDARY (nocx-d6gn4.8, completing .6).
//
// A carrier that owns its own control flow — a program, a plan walker — holds
// its continuation on a goroutine. Inside one Ask that is enough: the
// goroutine parks inside the effect that needs a person's answer and the host
// waits on the same channel it woke up on.
//
// It is not enough ACROSS an Ask, and that is the boundary a real approval
// crosses. The transport does not hold a suspended run open: it moves the run
// to awaiting_approval, returns, sends the question, and when the person
// answers it calls Ask AGAIN with the original messages. The model re-rolls,
// proposes the program a second time, and without this file the program would
// run from the top — a replay with no journal, in which every effect before
// the question happens twice. Under the declared-call carrier the same resume
// is harmless because the framework's message history already holds each
// earlier result; a program's earlier results are locals, and locals are not
// in any history.
//
// So the goroutine stays parked BETWEEN Asks, keyed by the run id, and the
// next Ask for that run continues it. Which makes the run id load-bearing:
// with none (a probe, a test that passes no run) there is nothing to key on
// and nothing can be continued, so the program is cancelled and the question
// is returned honestly rather than parked where nobody could ever reach it.
//
// STILL PROCESS-LIFETIME, deliberately. A parked goroutine dies with the
// backend, exactly as ADR-0028 says a checkpoint does. Durable suspension
// would need the replay machinery this carrier exists without, and it is out
// of scope for the epic.

import (
	"context"
	"sync"
)

// parkedRun is one self-driving carrier that stopped on a person's question.
// The goroutine is blocked inside the effect that asked and holds every local
// the program had; held is the question it is blocked on.
type parkedRun struct {
	suspensions <-chan *Suspension
	held        *Suspension
	done        chan struct{}
	answer      string
	err         error
	cancel      context.CancelFunc
}

// parkedRuns is the client's registry of them, keyed by run id. One per
// client, like the checkpoint store, and for the same reason: a suspension
// belongs to a run, and a run is driven by one client.
type parkedRuns struct {
	mu sync.Mutex
	m  map[string]*parkedRun
}

func newParkedRuns() *parkedRuns { return &parkedRuns{m: map[string]*parkedRun{}} }

// take removes and returns the parked run for an id, or nil.
func (r *parkedRuns) take(runID string) *parkedRun {
	if runID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.m[runID]
	delete(r.m, runID)
	return p
}

func (r *parkedRuns) put(runID string, p *parkedRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[runID] = p
}

// discard cancels and forgets a run's parked program. Called from the one
// place that already knows a run is over — Client.Discard — so the program
// dies with the run rather than outliving it.
func (r *parkedRuns) discard(runID string) {
	if p := r.take(runID); p != nil {
		p.cancel()
	}
}

// drive starts a self-driving carrier for one run, or continues the one that
// is already parked, and returns whichever comes first: what it answered, or
// the question it stopped on.
//
// mk BUILDS the carrier, and it is a factory rather than a built one because
// a run with a parked program must not get a second: the model re-proposed
// its program on the re-roll, and building that proposal's carrier here would
// leave two carriers for one run with only one of them ever running.
//
// The context mk's runner receives OUTLIVES this Ask — it is the caller's
// values without the caller's cancellation, plus a cancel of our own. That is
// the whole trick: the program must survive the return of the Ask that
// started it, and must still die when the run does.
func (r *parkedRuns) drive(ctx context.Context, runID string, mk func() (<-chan *Suspension, func(context.Context) (string, error))) (string, error) {
	p := r.take(runID)
	if p == nil {
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		suspensions, start := mk()
		p = &parkedRun{suspensions: suspensions, done: make(chan struct{}), cancel: cancel}
		go func() {
			p.answer, p.err = start(runCtx)
			close(p.done)
		}()
	} else {
		// The person answered. Releasing the parked effect makes the SAME
		// call again — same id, same arguments — which is what the approval
		// is bound to (invokeParking says why).
		p.held.Resume()
		p.held = nil
	}

	select {
	case s := <-p.suspensions:
		if runID == "" {
			// Nothing to key a continuation on. Say so by ending the program
			// rather than parking it somewhere no later Ask can find it.
			p.cancel()
			return "", &ApprovalRequestedError{Request: s.Request}
		}
		p.held = s
		r.put(runID, p)
		return "", &ApprovalRequestedError{Request: s.Request}
	case <-p.done:
		p.cancel()
		return p.answer, p.err
	case <-ctx.Done():
		p.cancel()
		return "", ctx.Err()
	}
}
