package workers

// The undispatched fact set (nocx-luqz9.3 keeps it; design §5 replaced the two
// routes out of it).
//
// What is left here is the SET and the one event that closes it — the
// coordinator's own fetch. The wake, the retry and the human are wake_test.go's,
// and the per-fact deadline that used to sit beside them is deleted: this file
// no longer asserts anything about a timer, because there is none.
//
// Every test drives the REAL registrar, because the set's whole input is what
// else the worker is holding and a table asked about one participant in
// isolation cannot be wrong in the way this one can.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/session"
)

// ── the stand ─────────────────────────────────────────────────────────────

// backstopHarness is the harness plus the set itself, so a test about the set's
// own rules can drive it directly rather than through an admission.
type backstopHarness struct {
	*harness
	b *Backstop
}

func newBackstopHarness(t *testing.T) *backstopHarness {
	t.Helper()
	h := newHarness(t)
	return &backstopHarness{harness: h, b: h.reg.attention}
}

func testFact() Fact {
	return Fact{
		Participant:        "p-1",
		Group:              testGroup,
		CoordinatorSession: coordSession,
		Kind:               FactExited,
		State:              StateExited,
		Task:               "read AGENTS.md and report",
	}
}

// ── the set is empty until a fact needs judgement ─────────────────────────

// A worker with nothing to judge owes nothing, and that is the whole claim the
// set makes about an idle effort.
func TestAGroupWithNothingUndispatchedHoldsNoFacts(t *testing.T) {
	h := newBackstopHarness(t)

	if got := len(h.b.Open()); got != 0 {
		t.Fatalf("open facts = %d, want 0", got)
	}
	// And nothing was typed at anybody for it. The wake's own arms are asserted
	// in wake_test.go; what is asserted here is that the SET is what triggers
	// nothing yet.
	if got := len(h.wake.seen()); got != 0 {
		t.Fatalf("wakes = %d, want 0", got)
	}
}

// A fetch stops the interval: what a coordinator has been told it holds is not
// something it still owes judgement on.
func TestDispatchLeavesNothingUndispatched(t *testing.T) {
	h := newBackstopHarness(t)
	h.b.Entered(context.Background(), testFact())

	if got := len(h.b.Open()); got != 1 {
		t.Fatalf("open facts after one fact = %d, want 1", got)
	}
	h.b.Dispatched("p-1")

	if got := len(h.b.Open()); got != 0 {
		t.Fatalf("open facts after the fetch = %d, want 0", got)
	}
}

// ── what enters, and what a fetch closes ──────────────────────────────────

// The end of a worker needs judgement when it is the last one, and its state
// is the record's reduction rather than the carrier's opinion of it.
func TestAnExitEntersTheSetCarryingTheStateTheRecordReducedTo(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p := mustRegister(t, h)

	if _, err := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: "exited"}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	open := h.reg.Undispatched()
	if len(open) != 1 || open[0].Kind != FactExited {
		t.Fatalf("undispatched = %+v, want one exit fact", open)
	}
	if open[0].State != StateExited {
		t.Fatalf("the fact says %q; the record reduced to %q", open[0].State, StateExited)
	}
	// The coordinator is carried ON the fact rather than looked up later: by
	// the time anybody acts on it the worker may hold nothing non-terminal, and
	// the answer would be gone exactly when it is needed.
	if open[0].CoordinatorSession != coordSession {
		t.Fatalf("the fact names coordinator %q, want %q", open[0].CoordinatorSession, coordSession)
	}
}

// THE COORDINATOR'S OWN CALL IS WHAT CLOSES IT (D8: the cursor advances on the
// fetch). Nothing else does — a wake is unacknowledged, which is why the set
// survived the mechanism that used to type it.
func TestAskingWhatTheSessionHoldsDispatchesTheFacts(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p := mustRegister(t, h)

	if _, err := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: string(session.ExitExited)}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got := len(h.reg.Undispatched()); got != 1 {
		t.Fatalf("undispatched before the fetch = %d, want 1", got)
	}

	if _, err := h.reg.HeldBy(ctx, coordSession); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if got := len(h.reg.Undispatched()); got != 0 {
		t.Fatalf("undispatched after the coordinator was told = %d, want 0", got)
	}
}

// A refused admission changed nothing anybody must judge, so it enters nothing.
// Stale evidence from a replaced incarnation is the case this protects: a
// coordinator sent to read a fact the record never took would be looking at a
// state that did not change.
func TestARefusedAdmissionEntersNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("evidence from another incarnation", func(t *testing.T) {
		h := newHarness(t)
		p := mustRegister(t, h)
		stale := testLiveness()
		stale.Attempt = 2
		if _, err := h.reg.Exited(ctx, p.ID, stale, Exit{Cause: "exited"}); err == nil {
			t.Fatalf("stale evidence was admitted")
		}
		if got := len(h.reg.Undispatched()); got != 0 {
			t.Fatalf("undispatched = %d, want 0", got)
		}
	})

	t.Run("a fact against a record that is already closed", func(t *testing.T) {
		h := newHarness(t)
		p := mustRegister(t, h)
		if err := h.store.Terminalize(ctx, p.ID, StateInterrupted); err != nil {
			t.Fatalf("terminalize: %v", err)
		}
		if _, err := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: "exited"}); err == nil {
			t.Fatalf("a fact was admitted against an interrupted record")
		}
		if got := len(h.reg.Undispatched()); got != 0 {
			t.Fatalf("undispatched = %d, want 0", got)
		}
	})
}

// ── coalescing ────────────────────────────────────────────────────────────

// One participant owes one of each fact. The same one arriving twice is the
// same fact: counting it twice would put a second entry in the denominator §12
// judges the mechanism by, for one thing to judge.
func TestTheSameFactTwiceIsOneFact(t *testing.T) {
	h := newBackstopHarness(t)
	h.b.Entered(context.Background(), testFact())
	h.b.Entered(context.Background(), testFact())

	if got := len(h.b.Open()); got != 1 {
		t.Fatalf("open facts = %d, want 1", got)
	}
	if got := h.b.Stats().Judgement; got != 1 {
		t.Fatalf("judgement = %d, want one fact counted once", got)
	}
}

// A fetch that returned somebody else's participants closes nothing here.
func TestAFetchByAnotherCoordinatorClosesNothing(t *testing.T) {
	h := newBackstopHarness(t)
	h.b.Entered(context.Background(), testFact())

	h.b.Dispatched("p-somebody-else")
	if got := len(h.b.Open()); got != 1 {
		t.Fatalf("open facts = %d, want the fact still owed", got)
	}
	h.b.Dispatched()
	if got := len(h.b.Open()); got != 1 {
		t.Fatalf("an empty fetch closed a fact: open = %d", got)
	}
}

// mustRegister runs one registration through the harness and fails the test if
// it did not reach live.
func mustRegister(t *testing.T, h *harness) Participant {
	t.Helper()
	reg, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Task: "read AGENTS.md and report", Command: "claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg.Participant
}

// A store that cannot say who coordinates the worker still records the fact.
//
// The failure is real — the worker row could have been deleted, or the store
// could be failing — and the honest half of the answer survives it: the fact is
// recorded with no coordinator on it, rather than dropped for want of a lookup.
func TestAFactWhoseCoordinatorCannotBeLookedUpIsStillRecorded(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p := mustRegister(t, h)

	h.store.setFault("coordinatorsession", 1)
	if _, err := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: "exited"}); err != nil {
		t.Fatalf("exit: %v", err)
	}

	open := h.reg.Undispatched()
	if len(open) != 1 {
		t.Fatalf("undispatched = %d, want the fact recorded anyway", len(open))
	}
	if open[0].CoordinatorSession != "" {
		t.Fatalf("a coordinator was invented for a lookup that failed: %q", open[0].CoordinatorSession)
	}
}
