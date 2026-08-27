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
	"errors"
	"sync"

	"github.com/shady2k/nocx/internal/log"
)

// WHY A CANCELLATION HAS A NAME HERE (nocx-d6gn4.8.1).
//
// A parked program is killed by cancelling its context, and an anonymous
// context.CancelFunc leaves nothing behind: the program wakes, returns
// context.Canceled, and every layer above reads that as "the socket went
// away" — the transport's classifier maps context.Canceled to
// transport-gone, so a run this process killed on purpose told a person the
// connection was lost. That is the failure this file was measured against.
//
// So every cancel carries a CAUSE, the goroutine reports context.Cause
// rather than ctx.Err, and each cause wraps context.Canceled — the layers
// that check errors.Is(err, context.Canceled) go on working, and the ones
// that print an error now print WHO ended the program and WHY.
type cancelCause struct {
	why string
	// sentence is what a person is told when this cause ends their run.
	// Empty means the cause is not a person-facing outcome.
	sentence string
}

func (c *cancelCause) Error() string { return c.why }

// Unwrap keeps every existing context.Canceled check true: this IS a
// cancellation, with a name attached.
func (c *cancelCause) Unwrap() error { return context.Canceled }

// Sentence is what the transport says about a run this cause ended.
func (c *cancelCause) Sentence() string { return c.sentence }

var (
	// errRunDiscarded is Client.Discard: the run is over — terminalized by
	// the transport, or ended by the Ask that owned it — so its
	// continuation must not outlive it.
	errRunDiscarded = &cancelCause{
		why:      "the program was ended because its run was discarded",
		sentence: "the run ended while its program was waiting, so the program was stopped",
	}
	// errProgramUnkeyed is a suspension with no run id to key a
	// continuation on (a probe, a direct call): nothing could ever resume
	// it, so it is ended rather than parked where nobody can reach it.
	errProgramUnkeyed = &cancelCause{
		why: "the program was ended: its run has no id, so no later ask could continue it",
	}
	// errProgramFinished releases the context of a program that returned.
	// Nothing observes it — the goroutine is gone — and it exists so that
	// "cancel with a cause" is the only way to cancel in this file.
	errProgramFinished = &cancelCause{why: "the program finished"}
	// errDriveGone is the ask that was driving the program dying under it.
	errDriveGone = &cancelCause{
		why:      "the program was ended because the ask driving it was cancelled",
		sentence: "the answer was cancelled while its program was running",
	}
)

// ProgramEndedSentence is what a person is told when a cause ended their
// program, and empty for an error that is not one. The transport asks this
// rather than matching context.Canceled, which cannot tell "this process
// killed it" from "the socket went away".
func ProgramEndedSentence(err error) (string, bool) {
	var c *cancelCause
	if errors.As(err, &c) && c.sentence != "" {
		return c.sentence, true
	}
	return "", false
}

// parkedRun is one self-driving carrier that stopped on a person's question.
// The goroutine is blocked inside the effect that asked and holds every local
// the program had; held is the question it is blocked on.
type parkedRun struct {
	// trace is the exchange the program belongs to, captured when it was
	// parked. It is held rather than read from a context because the thing
	// that ends a program most often has none: Client.Discard is given a
	// run id and nothing else, and a line about a killed program that
	// cannot be joined to the ask that started it is half a record.
	trace       string
	suspensions <-chan *Suspension
	// rebind hands the carrier the kernel of the drive that is resuming
	// it. The program's locals must survive an approval; the ask-scoped
	// seams of the kernel it parked with must NOT — they belong to a
	// stream that has already returned (starlarkCarrier.setKernel says
	// what that cost).
	rebind func(invoker)
	held   *Suspension
	done   chan struct{}
	answer string
	err    error
	cancel context.CancelCauseFunc
}

// parkedRuns is the client's registry of them, keyed by run id. One per
// client, like the checkpoint store, and for the same reason: a suspension
// belongs to a run, and a run is driven by one client.
type parkedRuns struct {
	log log.Logger
	mu  sync.Mutex
	m   map[string]*parkedRun
}

func newParkedRuns(logger log.Logger) *parkedRuns {
	return &parkedRuns{log: logger, m: map[string]*parkedRun{}}
}

// end cancels one parked program and SAYS SO — which run, which cause, and,
// for the causes that arrive from elsewhere in the process, the call path
// that decided it. The stack is cheap because this is rare: it runs once per
// program, at the moment the program dies.
func (r *parkedRuns) end(runID string, p *parkedRun, cause *cancelCause, blame bool) {
	p.cancel(cause)
	if r.log == nil {
		return
	}
	args := []any{"run", runID, "trace", p.trace, "cause", cause.why}
	if blame {
		args = append(args, "from", log.CallPath(2))
	}
	r.log.Warn("agent program: the parked program was ended", args...)
}

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
		// blame=true: this is the cancel that arrives from OUTSIDE the
		// program's own drive — the transport terminalizing, an Ask ending
		// — and the caller is the fact nobody could otherwise recover.
		r.end(runID, p, errRunDiscarded, true)
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
func (r *parkedRuns) drive(ctx context.Context, runID string, current invoker, mk func() (<-chan *Suspension, func(context.Context) (string, error), func(invoker))) (string, error) {
	p := r.take(runID)
	if p == nil {
		runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
		suspensions, start, rebind := mk()
		p = &parkedRun{trace: log.TraceID(ctx), suspensions: suspensions, rebind: rebind, done: make(chan struct{}), cancel: cancel}
		go func() {
			p.answer, p.err = start(runCtx)
			close(p.done)
		}()
	} else {
		// The person answered. Releasing the parked effect makes the SAME
		// call again — same id, same arguments — which is what the approval
		// is bound to (invokeParking says why).
		//
		// Logged because the pair "parked" / "continued" is what says a
		// resume happened AT ALL: without it, a run that restarted its
		// program from the top and a run that continued it read identically
		// in the log.
		if r.log != nil {
			r.log.Info("agent program: continuing where it stopped", "run", runID,
				"trace", p.trace, "tool", p.held.Request.Tool, "call", p.held.Request.CallID)
		}
		// THE KERNEL OF THIS DRIVE, before the effect is released. The
		// released call announces itself into the stream a person is
		// watching NOW and records against the ask that is running now;
		// through the parked one it announced into a stream that had
		// returned, whose durable writes went through a dead context and
		// came back as a bare cancellation the transport read as a lost
		// connection (nocx-d6gn4.8.1).
		if p.rebind != nil {
			p.rebind(current)
		}
		p.held.Resume()
		p.held = nil
	}

	select {
	case s := <-p.suspensions:
		if runID == "" {
			// Nothing to key a continuation on. Say so by ending the program
			// rather than parking it somewhere no later Ask can find it.
			r.end(runID, p, errProgramUnkeyed, false)
			return "", &ApprovalRequestedError{Request: s.Request}
		}
		p.held = s
		r.put(runID, p)
		if r.log != nil {
			r.log.Info("agent program: parked on a question", "run", runID,
				"trace", p.trace, "tool", s.Request.Tool, "call", s.Request.CallID)
		}
		return "", &ApprovalRequestedError{Request: s.Request}
	case <-p.done:
		p.cancel(errProgramFinished)
		return p.answer, p.err
	case <-ctx.Done():
		// The drive died under the program. The CAUSE of the parent's death
		// is carried through rather than flattened to context.Canceled, so
		// the run says which thing ended and not "the connection was lost".
		r.end(runID, p, errDriveGone, true)
		return "", errDriveGone
	}
}
