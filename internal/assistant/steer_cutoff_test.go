package assistant

import (
	"context"
	"testing"
)

// I2 — A MESSAGE'S DECLINE COVERS EXACTLY THE CALLS THAT HAD ARRIVED WHEN THE
// ANSWER WAS SETTLED (nocx-a62cl.6.2).
//
// The record is one fact about one moment, so the moment travels with it: a
// cutoff read late is a cutoff that has moved, and every call that arrived in
// between would be refused for something the person had not yet seen.

// The cutoff carried on the answer is what the order honours — not how many
// calls exist by the time the pipeline reads it.
func TestADeclineCoversTheArrivalsItCarriedAndNoLaterCall(t *testing.T) {
	ctx := context.Background()
	order := newTurnOrder()

	asking, finishAsking := order.reserve()
	releaseAsking, declined, err := asking.enter(ctx)
	if err != nil || declined != nil {
		t.Fatalf("the first call: %v %+v", err, declined)
	}
	// The moment the person's answer settles: one call had arrived.
	cutoff := order.ArrivalCutoff()
	if cutoff != 1 {
		t.Fatalf("cutoff = %d, want the one call that had arrived", cutoff)
	}
	// The answered call settles — the cutoff above is that moment — and the
	// pipeline has NOT yet read the answer when the next call arrives. That is
	// the window the review found: a cutoff read at the record would have
	// moved by now.
	releaseAsking()
	finishAsking()
	late, finishLate := order.reserve()
	defer finishLate()
	order.declinedBy(Declined{Reason: "use the other log", Through: cutoff})

	lateRelease, lateDeclined, err := late.enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lateRelease()
	if lateDeclined != nil {
		t.Fatalf("a call that arrived after the answer settled was refused by it: %+v", lateDeclined)
	}
}

// A second answer refreshes the refusal AND the cutoff: a call queued behind
// the second declined call is refused with the second message, not the first.
//
// The shape the review named: one message declines a batch, the turn goes on,
// another call asks its own question, a second message answers THAT one — and
// the calls queued behind it must carry the second message. The first batch's
// reason is history by then.
func TestASecondDeclineRefreshesTheRefusalAndItsCutoff(t *testing.T) {
	ctx := context.Background()
	order := newTurnOrder()

	// The first call asks; a second is already queued behind it.
	asking, finishAsking := order.reserve()
	releaseAsking, declined, err := asking.enter(ctx)
	if err != nil || declined != nil {
		t.Fatalf("the first call: %v %+v", err, declined)
	}
	queuedFirst, finishQueuedFirst := order.reserve()

	// The person answers the first call: both it and the call queued behind it
	// carry the first message.
	order.declinedBy(Declined{Reason: "the first message", Through: order.ArrivalCutoff()})
	releaseAsking()
	finishAsking()
	queuedRelease, queuedDeclined, err := queuedFirst.enter(ctx)
	if err != nil || queuedDeclined == nil || queuedDeclined.Reason != "the first message" {
		t.Fatalf("the call behind the first decline = %+v, %v", queuedDeclined, err)
	}
	queuedRelease()
	finishQueuedFirst()

	// The batch drained. A NEW call is admitted and asks its own question.
	next, finishNext := order.reserve()
	nextRelease, nextDeclined, err := next.enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if nextDeclined != nil {
		t.Fatalf("a call that arrived after the first batch was refused by it: %+v", nextDeclined)
	}
	// A second message answers THAT call, with a call already queued behind it.
	behind, finishBehind := order.reserve()
	order.declinedBy(Declined{Reason: "the second message", Through: order.ArrivalCutoff()})
	nextRelease()
	finishNext()
	behindRelease, behindDeclined, err := behind.enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer behindRelease()
	defer finishBehind()
	if behindDeclined == nil || behindDeclined.Reason != "the second message" {
		t.Fatalf("the call behind the second decline = %+v, want the second message", behindDeclined)
	}
}
