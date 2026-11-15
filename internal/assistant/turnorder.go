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

	mu   sync.Mutex
	tail <-chan struct{}
	// declined is the first decline of this turn, and the sentence the calls
	// QUEUED BEHIND IT AT THAT MOMENT are refused with. The first one stands:
	// those calls are refused because of it, and their own refusals must not
	// overwrite what actually happened.
	declined *Declined
	// declinedThrough is the arrival number of the last call that existed
	// when declined was recorded. A call that arrives LATER is not covered:
	// a person's message during a pending approval answered the calls they
	// could see, and the model that reads the refusal and proposes again is
	// asking a new question (ADR-anntnxdj decision 4).
	declinedThrough uint64
	// arrivals numbers this turn's calls in arrival order.
	arrivals uint64
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
	reservation, finish := o.reserve()
	release, declined, err := reservation.enter(ctx)
	if err != nil {
		finish()
		return nil, nil, err
	}
	return func() { release(); finish() }, declined, nil
}

// turnReservation fixes arrival order without blocking the endpoint read loop.
type turnReservation struct {
	order    *TurnOrder
	previous <-chan struct{}
	// arrival is where this call stands in the turn's order of arrivals,
	// which is what a decline recorded before it decides.
	arrival uint64
}

func (o *TurnOrder) reserve() (*turnReservation, func()) {
	o.mu.Lock()
	o.arrivals++
	arrival := o.arrivals
	previous := o.tail
	done := make(chan struct{})
	o.tail = done
	o.mu.Unlock()
	var once sync.Once
	finish := func() {
		once.Do(func() {
			// A cancelled queued call cannot release the call behind it past
			// the still-running predecessor.
			select {
			case <-previous:
				close(done)
			default:
				if previous == nil {
					close(done)
				} else {
					go func() { <-previous; close(done) }()
				}
			}
		})
	}
	return &turnReservation{order: o, previous: previous, arrival: arrival}, finish
}

func (r *turnReservation) enter(ctx context.Context) (func(), *Declined, error) {
	if r.previous != nil {
		select {
		case <-r.previous:
		default:
			select {
			case <-r.previous:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
	}
	r.order.slot <- struct{}{}
	r.order.mu.Lock()
	declined := r.order.declined
	if r.arrival > r.order.declinedThrough {
		// This call arrived after the person answered: their message is not
		// its answer.
		declined = nil
	}
	r.order.mu.Unlock()
	return func() { <-r.order.slot }, declined, nil
}

// declinedBy records a person's no as this turn's answer for the calls queued
// behind the one they declined. It covers exactly the calls that exist at this
// moment: the ones already waiting for their turn. A call the agent makes after
// it reads the refusal arrives later and is asked about like any other.
func (o *TurnOrder) declinedBy(answer Declined) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.declined == nil {
		refusal := answer
		o.declined = &refusal
		o.declinedThrough = o.arrivals
	}
}
