package assistant

// The order of one turn's calls (ADR-anntnxdj §2, "Calls of one turn run one at
// a time, in order").
//
// It replaces eino's batch latch. The latch lived in the run context and only
// the framework's tool wrapper could read it, so "every later call in this
// model response stops" was a statement about a model response — a shape only a
// batch-proposing model has. The order is the pipeline's own state instead: one
// call runs at a time, in the order the calls arrived, and a call behind one a
// person declined is refused with that same reason rather than started. A
// caller that is not eino gets it too, which is the point — the tool endpoint
// accepts concurrent calls and would otherwise have no order at all.
//
// One turn is one run: the kernel builds one of these per run, and the pipeline
// is what holds it. At most one question is open at a time because at most one
// call is in the pipeline at a time, and a run cannot be terminal while a call
// it accepted is still unsettled — the call is on the turn's own stack.

import (
	"context"
	"sync"
)

// TurnOrder is one turn's calls, serialized.
type TurnOrder struct {
	// slot holds the one token a call needs to run. A channel rather than a
	// mutex because a queued call must stop when its OWN context ends: a
	// person who cancels the turn while a call waits for its turn cancels
	// that call, and a mutex would leave it waiting for a lock nobody will
	// release for it.
	slot chan struct{}

	mu sync.Mutex
	// declined is the first decline of this turn, and the sentence every
	// call behind it is refused with. The first one stands: the later calls
	// are refused because of it, and their own refusals must not overwrite
	// what actually happened.
	declined *Declined
}

func newTurnOrder() *TurnOrder {
	return &TurnOrder{slot: make(chan struct{}, 1)}
}

// enter takes the turn's one slot, waiting for the call before it to settle.
// It returns the release the caller must defer, and the decline this call
// inherits when the call before it was refused by a person. A context that
// ends while waiting ends the wait: a queued call does not outlive the turn it
// was queued in.
func (o *TurnOrder) enter(ctx context.Context) (func(), *Declined, error) {
	// An already-ended context whose turn is free takes the slot: the
	// pipeline leaves such a call to its caller (the kernel's run lease ends
	// the run context as an OUTCOME to record), and refusing it here would
	// stop that record from being written. A context that ends while the
	// call is QUEUED does stop the wait.
	select {
	case o.slot <- struct{}{}:
	default:
		select {
		case o.slot <- struct{}{}:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	release := func() { <-o.slot }
	o.mu.Lock()
	declined := o.declined
	o.mu.Unlock()
	return release, declined, nil
}

// declinedBy records a person's no as this turn's answer for the calls behind
// the one they declined.
func (o *TurnOrder) declinedBy(answer Declined) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.declined == nil {
		refusal := answer
		o.declined = &refusal
	}
}
