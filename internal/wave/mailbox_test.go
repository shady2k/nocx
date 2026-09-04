package wave

// The mailbox (nocx-dkawo.11).
//
// The two assertions the design wrote for this are §11.10 and §11.11, and
// both are about what a read does NOT do: it does not take, and it does not
// make a message look delivered. Every test here is one of those two read
// through a different door.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	workerBox = ReaderID("p-1")
	coordBox  = ReaderID(coordSession)
)

// sayTo commits one message and fails the test if it did not land.
func sayTo(t *testing.T, h *harness, from, to ReaderID, body string) Message {
	t.Helper()
	m, err := h.reg.Say(context.Background(), testWave, from, to, body)
	if err != nil {
		t.Fatalf("say %q → %q: %v", from, to, err)
	}
	return m
}

// ── §11.10: two readers, and neither takes the other's mail ───────────────

// THE CRITERION, and the failure it was bought by: two readers read the same
// mailbox, neither loses a message, and the cursor of one does not move
// because the other read.
func TestTwoReadersReadTheSameMailboxAndNeitherTakesFromTheOther(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)

	first := sayTo(t, h, coordBox, workerBox, "start with AGENTS.md")
	second := sayTo(t, h, coordBox, workerBox, "then the architecture")

	// The recipient reads its own mailbox.
	byWorker, err := h.reg.Inbox(ctx, workerBox, workerBox, 0)
	if err != nil {
		t.Fatalf("worker inbox: %v", err)
	}
	if len(byWorker.Messages) != 2 {
		t.Fatalf("the worker was handed %d messages, want 2", len(byWorker.Messages))
	}
	if byWorker.Cursor.Fetched != second.Seq {
		t.Fatalf("the worker's cursor is at %d, want %d", byWorker.Cursor.Fetched, second.Seq)
	}

	// A SECOND reader looks into the same mailbox and is handed the same
	// two. Nothing was taken, because reading is not taking.
	const observer = ReaderID("sess-observer")
	byObserver, err := h.reg.Inbox(ctx, workerBox, observer, 0)
	if err != nil {
		t.Fatalf("observer inbox: %v", err)
	}
	if len(byObserver.Messages) != 2 {
		t.Fatalf("the second reader was handed %d messages, want the same 2", len(byObserver.Messages))
	}
	if byObserver.Messages[0].ID != first.ID || byObserver.Messages[1].ID != second.ID {
		t.Fatalf("the second reader got different messages: %+v", byObserver.Messages)
	}

	// And the first reader's position did not move because the second read.
	again, err := h.reg.Inbox(ctx, workerBox, workerBox, 0)
	if err != nil {
		t.Fatalf("worker inbox again: %v", err)
	}
	if len(again.Messages) != 0 {
		t.Fatalf("the worker was handed %d messages a second time, want 0", len(again.Messages))
	}
	if again.Cursor.Fetched != second.Seq {
		t.Fatalf("the worker's cursor moved to %d because somebody else read", again.Cursor.Fetched)
	}
}

// Each reader's position is its own from the start: a reader that has never
// looked sees everything, however much everyone else has read.
func TestAReaderThatHasNeverLookedSeesEverything(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)
	sayTo(t, h, coordBox, workerBox, "one")
	sayTo(t, h, coordBox, workerBox, "two")

	if _, err := h.reg.Inbox(ctx, workerBox, workerBox, 0); err != nil {
		t.Fatalf("worker inbox: %v", err)
	}
	late, err := h.reg.Inbox(ctx, workerBox, "sess-arrived-late", 0)
	if err != nil {
		t.Fatalf("late reader inbox: %v", err)
	}
	if len(late.Messages) != 2 {
		t.Fatalf("a reader that never looked was handed %d, want both", len(late.Messages))
	}
}

// ── §11.11: committed-not-fetched is reported as itself ───────────────────

// THE OTHER CRITERION. A message nobody took is not a delivery, and the
// record says which of the two it is rather than collapsing them into one
// "delivered" — which is how consumed mail became invisible in the failure
// this whole mailbox was written from.
func TestAMessageNobodyFetchedIsReportedAsCommittedAndNotAsDelivered(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)
	sent := sayTo(t, h, coordBox, workerBox, "nobody will read this")

	owed, err := h.reg.Undelivered(ctx, testWave)
	if err != nil {
		t.Fatalf("undelivered: %v", err)
	}
	if len(owed) != 1 || owed[0].ID != sent.ID {
		t.Fatalf("undelivered = %+v, want the one message", owed)
	}
	cur, err := h.reg.Inbox(ctx, workerBox, workerBox, 0)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	// After the recipient took it, it is HANDED OVER and not acted upon: the
	// backend can witness that it handed it over and cannot witness what the
	// reader did with it. The two marks are what say so.
	if cur.Cursor.Fetched != sent.Seq {
		t.Fatalf("fetched = %d after the recipient took it, want %d", cur.Cursor.Fetched, sent.Seq)
	}
	if cur.Cursor.Acted != 0 {
		t.Fatalf("acted = %d; the backend cannot witness what the reader did", cur.Cursor.Acted)
	}
	owed, err = h.reg.Undelivered(ctx, testWave)
	if err != nil {
		t.Fatalf("undelivered after the fetch: %v", err)
	}
	if len(owed) != 0 {
		t.Fatalf("a fetched message is still reported as undelivered: %+v", owed)
	}

	// And acted-upon is the reader's own claim, arriving separately.
	if ackErr := h.reg.Acknowledge(ctx, workerBox, workerBox, sent.Seq); ackErr != nil {
		t.Fatalf("acknowledge: %v", ackErr)
	}
	after, err := h.reg.Inbox(ctx, workerBox, workerBox, 0)
	if err != nil {
		t.Fatalf("inbox after ack: %v", err)
	}
	if after.Cursor.Acted != sent.Seq {
		t.Fatalf("acted = %d after the acknowledgement, want %d", after.Cursor.Acted, sent.Seq)
	}
}

// A SECOND reader having seen a message says nothing about whether it was
// delivered. Delivery is the recipient's own cursor, because a message is
// delivered when the participant it was addressed to has taken it — not when
// somebody has looked.
func TestAnotherReaderLookingDoesNotDeliverAnything(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)
	sayTo(t, h, coordBox, workerBox, "for the worker")

	if _, err := h.reg.Inbox(ctx, workerBox, "sess-observer", 0); err != nil {
		t.Fatalf("observer inbox: %v", err)
	}
	owed, err := h.reg.Undelivered(ctx, testWave)
	if err != nil {
		t.Fatalf("undelivered: %v", err)
	}
	if len(owed) != 1 {
		t.Fatalf("somebody else's read marked the message delivered: %+v", owed)
	}
}

// ── the acknowledgement mark, which is what makes a retry safe ────────────

// "Read consumes nothing" prevents loss and does not prevent duplication. The
// second mark is what tells the record a response has already been acted on,
// so a retry of it does not commit the same spawn twice.
func TestAcknowledgementNeverRunsAheadOfWhatWasHandedOver(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)
	sayTo(t, h, coordBox, workerBox, "one")
	sayTo(t, h, coordBox, workerBox, "two")

	// Nothing has been fetched, so nothing can have been acted on.
	if err := h.reg.Acknowledge(ctx, workerBox, workerBox, 1); err == nil {
		t.Fatalf("an acknowledgement of mail nobody was handed was accepted")
	}

	got, err := h.reg.Inbox(ctx, workerBox, workerBox, 1)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !got.More {
		t.Fatalf("a page that left a message behind did not say so")
	}
	if pastErr := h.reg.Acknowledge(ctx, workerBox, workerBox, 2); pastErr == nil {
		t.Fatalf("an acknowledgement past the page that was handed over was accepted")
	}
	if ackErr := h.reg.Acknowledge(ctx, workerBox, workerBox, 1); ackErr != nil {
		t.Fatalf("acknowledge 1: %v", ackErr)
	}
	// And it never goes backwards.
	if backErr := h.reg.Acknowledge(ctx, workerBox, workerBox, 0); backErr != nil {
		t.Fatalf("a backwards acknowledgement should be a no-op, not an error: %v", backErr)
	}
	cur, err := h.reg.Inbox(ctx, workerBox, workerBox, 0)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if cur.Cursor.Acted != 1 {
		t.Fatalf("acted = %d after a backwards acknowledgement, want 1", cur.Cursor.Acted)
	}
}

// ── membership, and what is deliberately not checked ──────────────────────

// MEMBERSHIP is what mail is checked against. A stranger cannot write into a
// wave's mailbox and cannot be written to.
func TestMailIsCheckedAgainstMembershipAndNothingElse(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)

	if _, err := h.reg.Say(ctx, testWave, coordBox, "p-not-in-this-wave", "hello"); !errors.Is(err, ErrNotAMember) {
		t.Fatalf("writing to a stranger returned %v, want ErrNotAMember", err)
	}
	if _, err := h.reg.Say(ctx, testWave, "sess-stranger", workerBox, "hello"); !errors.Is(err, ErrNotAMember) {
		t.Fatalf("a stranger writing in returned %v, want ErrNotAMember", err)
	}
	// The coordinator is named by its SESSION and the worker by its
	// participant id, and both are members.
	sayTo(t, h, coordBox, workerBox, "down")
	sayTo(t, h, workerBox, coordBox, "up")
}

// A human takeover suspends the coordinator's send-input and must not stop it
// writing to its own worker: membership makes a participant addressable,
// delegation makes it controllable, and mail is addressed and not controlled.
func TestASuspendedDelegationDoesNotStopMail(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	p := mustRegister(t, h)

	if err := h.store.PutDelegation(ctx, Delegation{
		ControllerSession: coordSession, Participant: p.ID,
		Effects: DefaultBundle(), State: DelegationInputSuspended,
	}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	sayTo(t, h, coordBox, ReaderID(p.ID), "a person is helping you; carry on")
}

// ── the bounds ────────────────────────────────────────────────────────────

func TestAMessageIsBoundedAndAnEmptyOneIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)

	if _, err := h.reg.Say(ctx, testWave, coordBox, workerBox, ""); !errors.Is(err, ErrEmptyMessage) {
		t.Fatalf("an empty message returned %v, want ErrEmptyMessage", err)
	}
	big := strings.Repeat("x", MaxMessageBytes+1)
	if _, err := h.reg.Say(ctx, testWave, coordBox, workerBox, big); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("an oversized message returned %v, want ErrMessageTooLarge", err)
	}
	// And the bound is a bound, not an ambition: exactly at it is accepted.
	sayTo(t, h, coordBox, workerBox, strings.Repeat("x", MaxMessageBytes))
}

// A fetch is bounded too, and a reader that has fallen behind catches up over
// several calls rather than being handed a page it cannot fit.
func TestAFetchIsBoundedAndSaysWhenMoreRemains(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)
	for i := 0; i < 3; i++ {
		sayTo(t, h, coordBox, workerBox, "message")
	}

	first, err := h.reg.Inbox(ctx, workerBox, workerBox, 2)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(first.Messages) != 2 || !first.More {
		t.Fatalf("first page = %d messages, more=%v; want 2 and more", len(first.Messages), first.More)
	}
	second, err := h.reg.Inbox(ctx, workerBox, workerBox, 2)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(second.Messages) != 1 || second.More {
		t.Fatalf("second page = %d messages, more=%v; want 1 and no more", len(second.Messages), second.More)
	}
}

// ── the failure path the fetch has ────────────────────────────────────────

// A cursor that could not be written is a fetch that did not happen. The
// messages are NOT returned: a reader that believed it had a position it does
// not have would acknowledge past mail it never saw, and losing a fetch costs
// one repeat while losing a message costs the wave.
func TestAFetchWhoseCursorCannotBeWrittenHandsNothingOver(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	mustRegister(t, h)
	sayTo(t, h, coordBox, workerBox, "one")

	h.store.setFault("advancecursor", 1)
	h.store.resetCounts()
	if _, err := h.reg.Inbox(ctx, workerBox, workerBox, 0); err == nil {
		t.Fatalf("a fetch whose cursor could not be written returned messages")
	}
	// And the message is still there for the next attempt, because nothing
	// was consumed.
	owed, err := h.reg.Undelivered(ctx, testWave)
	if err != nil {
		t.Fatalf("undelivered: %v", err)
	}
	if len(owed) != 1 {
		t.Fatalf("undelivered = %d, want the message still owed", len(owed))
	}
}
