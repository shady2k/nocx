package wave

// The routing table and what coalesces (nocx-dkawo.4).
//
// The design calls this "the question that decides whether any of this is
// useful": which facts are routine, which wake the coordinator, and which go
// to the human. Get it wrong in one direction and the coordinator is woken
// for every completion, which is the poll this mechanism replaced; get it
// wrong in the other and the end of the wave reaches nobody.
//
// Every test here runs N workers through the REAL registrar, because the
// table's whole input is what else the wave is holding, and a table asked
// about one participant in isolation cannot be wrong in the way this one can.

import (
	"context"
	"fmt"
	"testing"
)

// fanout registers n workers and returns them.
func fanout(t *testing.T, h *harness, n int) []Participant {
	t.Helper()
	ctx := context.Background()
	out := make([]Participant, 0, n)
	for i := 0; i < n; i++ {
		p, err := h.reg.Register(ctx, RegisterRequest{
			Wave: testWave, CoordinatorSession: coordSession, Role: RoleWorker,
			Task: fmt.Sprintf("task %d", i+1), Command: "claude",
		})
		if err != nil {
			t.Fatalf("register worker %d: %v", i+1, err)
		}
		out = append(out, p)
	}
	return out
}

// finish declares success and exits, which is what an ordinary worker does.
func finish(t *testing.T, h *harness, p Participant) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.reg.Declared(ctx, p.ID, testLiveness(), Declaration{OK: true, Summary: "done"}); err != nil {
		t.Fatalf("declare %s: %v", p.ID, err)
	}
	if _, err := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: "exited"}); err != nil {
		t.Fatalf("exit %s: %v", p.ID, err)
	}
}

// ── the table ─────────────────────────────────────────────────────────────

// THE CRITERION: three workers run, a routine completion wakes nobody, and
// the end of the wave does.
//
// The two halves are one rule read at two moments. A worker finishing with
// two still running tells the coordinator nothing it did not expect, and
// waking it would spend a turn on "yes, one of three is done". The LAST one
// finishing is the wave arriving, which is the moment the coordinator exists
// for.
func TestThreeWorkersRunAndOnlyTheEndOfTheWaveWakesTheCoordinator(t *testing.T) {
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	finish(t, h, workers[0])
	if got := len(h.wake.seen()); got != 0 {
		t.Fatalf("wakes after one of three finished = %d, want 0", got)
	}
	if got := len(h.reg.Undispatched()); got != 0 {
		t.Fatalf("undispatched after a routine completion = %d, want 0", got)
	}
	if got := h.alarms.running(); got != 0 {
		t.Fatalf("armed alarms after a routine completion = %d, want 0", got)
	}

	finish(t, h, workers[1])
	if got := len(h.wake.seen()); got != 0 {
		t.Fatalf("wakes after two of three finished = %d, want 0", got)
	}

	finish(t, h, workers[2])
	if got := len(h.wake.seen()); got == 0 {
		t.Fatalf("the wave finished and the coordinator was never woken")
	}
	if got := len(h.reg.Undispatched()); got == 0 {
		t.Fatalf("the wave finished and the record owes nobody judgement")
	}
}

// A worker that did NOT succeed wakes the coordinator whatever else is
// running. Holding a crash until the wave finishes would report it after the
// work that depended on it, which is the one ordering that cannot be undone.
func TestAWorkerThatDidNotSucceedWakesTheCoordinatorWhileOthersRun(t *testing.T) {
	ctx := context.Background()

	t.Run("it says it failed", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		workers := fanout(t, h, 3)
		if _, err := h.reg.Declared(ctx, workers[0].ID, testLiveness(),
			Declaration{OK: false, Summary: "could not build"}); err != nil {
			t.Fatalf("declare: %v", err)
		}
		if got := len(h.wake.seen()); got != 1 {
			t.Fatalf("wakes = %d, want 1 for a worker that reported failure", got)
		}
	})

	t.Run("it is gone and never said anything", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		workers := fanout(t, h, 3)
		if _, err := h.reg.Exited(ctx, workers[0].ID, testLiveness(),
			Exit{Cause: "signalled"}); err != nil {
			t.Fatalf("exit: %v", err)
		}
		if got := len(h.wake.seen()); got != 1 {
			t.Fatalf("wakes = %d, want 1 for an abandoned worker", got)
		}
		open := h.reg.Undispatched()
		if len(open) != 1 || open[0].State != StateAbandoned {
			t.Fatalf("undispatched = %+v, want the abandoned worker", open)
		}
	})
}

// A read that failed is not evidence the wave is finished, and it is not
// evidence that it is not. Judgement is the fail-closed direction: a fact the
// coordinator did not need costs it one turn, and a fact it never learns
// about costs it the wave.
func TestAStoreThatCannotSayWhatElseIsRunningWakesTheCoordinator(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	h.store.setFault("nonterminal", 1)
	h.store.resetCounts()
	if _, err := h.reg.Declared(ctx, workers[0].ID, testLiveness(),
		Declaration{OK: true, Summary: "done"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if got := len(h.wake.seen()); got != 1 {
		t.Fatalf("wakes = %d, want 1: a table that cannot read the wave must not decide routine", got)
	}
}

// ── coalescing ────────────────────────────────────────────────────────────

// The wake costs the coordinator a turn, and one turn answers everything:
// wave.holdings returns the whole session. A second wake before it has
// fetched would spend a turn to say what the first turn was already going to
// show.
func TestSeveralJudgementFactsWakeTheCoordinatorOnce(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	for _, w := range workers {
		if _, err := h.reg.Declared(ctx, w.ID, testLiveness(),
			Declaration{OK: false, Summary: "no"}); err != nil {
			t.Fatalf("declare %s: %v", w.ID, err)
		}
	}
	if got := len(h.wake.seen()); got != 1 {
		t.Fatalf("wakes = %d, want 1 for three facts in one wave", got)
	}
	if got := len(h.reg.Undispatched()); got != 3 {
		t.Fatalf("undispatched = %d, want all three still owed", got)
	}

	// The fetch clears the wave, so the next fact is a new situation.
	if _, err := h.reg.HeldBy(ctx, coordSession); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if _, err := h.reg.Exited(ctx, workers[0].ID, testLiveness(), Exit{Cause: "exited"}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got := len(h.wake.seen()); got != 2 {
		t.Fatalf("wakes after the coordinator fetched and a new fact arrived = %d, want 2", got)
	}
}

// A REFUSED wake told the coordinator nothing, so it does not coalesce: the
// next fact is a fresh chance to catch a pane that is waiting for input.
// Treating a refusal as "already awake" would silence the wave for good the
// first time the coordinator happened to be mid-turn.
func TestARefusedWakeDoesNotSilenceTheNextFact(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	h.wake.out = WakeOutcome{Reason: "that pane is working"}
	workers := fanout(t, h, 3)

	if _, err := h.reg.Declared(ctx, workers[0].ID, testLiveness(), Declaration{OK: false}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if _, err := h.reg.Declared(ctx, workers[1].ID, testLiveness(), Declaration{OK: false}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if got := len(h.wake.seen()); got != 2 {
		t.Fatalf("wakes = %d, want a second attempt after the first was refused", got)
	}
}

// FIVE FACTS ARRIVING WHILE THE COORDINATOR IS AWAY PRODUCE ONE ESCALATION.
//
// Five cards for one situation is how an attention surface becomes noise,
// which is the failure the attention queue's own bead warns about in its
// first paragraph. The card says how many, so coalescing loses nothing.
func TestFiveFactsWhileTheCoordinatorIsAwayProduceOneEscalation(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 8)
	h.wake.out = WakeOutcome{Reason: "nobody is there"}
	workers := fanout(t, h, 5)

	for _, w := range workers {
		if _, err := h.reg.Declared(ctx, w.ID, testLiveness(), Declaration{OK: false}); err != nil {
			t.Fatalf("declare %s: %v", w.ID, err)
		}
	}
	h.alarms.fireAll()

	told := h.human.seen()
	if len(told) != 1 {
		t.Fatalf("escalations = %d, want exactly 1 for one wave", len(told))
	}
	if told[0].AlsoOwed != 4 {
		t.Fatalf("the card says %d others are owed, want 4", told[0].AlsoOwed)
	}
	// Every fact still reached the end of its own deadline: coalescing
	// suppresses the CARD, never the accounting.
	open := h.reg.Undispatched()
	if len(open) != 5 {
		t.Fatalf("undispatched = %d, want 5", len(open))
	}
	for _, f := range open {
		if !f.Escalated {
			t.Fatalf("fact %s never reached its deadline: %+v", f.Participant, f)
		}
	}
}

// And the suppression ends when the person's card does: once the coordinator
// has fetched, the wave owes nothing, and the next fact raises a new card.
func TestAFetchClearsTheWaveSoTheNextFactCanEscalateAgain(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	h.wake.out = WakeOutcome{Reason: "nobody is there"}
	workers := fanout(t, h, 3)

	if _, err := h.reg.Declared(ctx, workers[0].ID, testLiveness(), Declaration{OK: false}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	h.alarms.fireAll()
	if got := len(h.human.seen()); got != 1 {
		t.Fatalf("escalations = %d, want 1", got)
	}

	if _, err := h.reg.HeldBy(ctx, coordSession); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if _, err := h.reg.Declared(ctx, workers[1].ID, testLiveness(), Declaration{OK: false}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	h.alarms.fireAll()
	if got := len(h.human.seen()); got != 2 {
		t.Fatalf("escalations after the coordinator had fetched = %d, want 2", got)
	}
}

// ── the number the design is judged by ────────────────────────────────────

// §12: what fraction of facts reaches the HUMAN rather than the coordinator.
// If most escalate, the mechanism moved the work to a person and should say
// so out loud instead of being described as orchestration.
//
// The routine branch is counted for exactly this reason: a table whose
// routine facts left no trace could report the fraction only over the facts
// it already decided were interesting, which is the flattering denominator.
func TestTheRecordCountsWhatTheMechanismCost(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	finish(t, h, workers[0]) // declared + exited, both routine
	finish(t, h, workers[1]) // routine again
	if _, err := h.reg.Declared(ctx, workers[2].ID, testLiveness(),
		Declaration{OK: false}); err != nil {
		t.Fatalf("declare: %v", err)
	}

	s := h.reg.Cost()
	if s.Routine != 4 {
		t.Fatalf("routine = %d, want the four facts nobody was woken for", s.Routine)
	}
	if s.Judgement != 1 {
		t.Fatalf("judgement = %d, want 1", s.Judgement)
	}
	if s.Facts() != 5 {
		t.Fatalf("facts = %d, want every fact in the denominator", s.Facts())
	}
	if s.Woken != 1 {
		t.Fatalf("woken = %d, want 1", s.Woken)
	}
	if s.Escalated != 0 {
		t.Fatalf("escalated = %d before any deadline fired", s.Escalated)
	}

	h.alarms.fireAll()
	if got := h.reg.Cost().Escalated; got != 1 {
		t.Fatalf("escalated = %d, want 1", got)
	}
	if _, err := h.reg.HeldBy(ctx, coordSession); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if got := h.reg.Cost().Dispatched; got != 1 {
		t.Fatalf("dispatched = %d, want 1", got)
	}
}

// A wake DELIVERED is counted; a wake refused is not. The fraction is about
// who was reached, and an attempt a pane refused reached nobody.
func TestOnlyADeliveredWakeCounts(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	h.wake.out = WakeOutcome{Reason: "that pane is working"}
	workers := fanout(t, h, 3)

	if _, err := h.reg.Declared(ctx, workers[0].ID, testLiveness(), Declaration{OK: false}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if got := h.reg.Cost().Woken; got != 0 {
		t.Fatalf("woken = %d, want 0 for a refused wake", got)
	}
	if got := h.reg.Cost().Judgement; got != 1 {
		t.Fatalf("judgement = %d, want the fact counted anyway", got)
	}
}

// Two numbers, not one. Five facts reaching a person in one card is five
// facts that reached them AND one interruption; a design can be wrong in
// either direction alone, so the record keeps both.
func TestTheRecordCountsWhatReachedThePersonAndWhatItCostThem(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 8)
	h.wake.out = WakeOutcome{Reason: "nobody is there"}
	workers := fanout(t, h, 5)
	for _, w := range workers {
		if _, err := h.reg.Declared(ctx, w.ID, testLiveness(), Declaration{OK: false}); err != nil {
			t.Fatalf("declare %s: %v", w.ID, err)
		}
	}
	h.alarms.fireAll()

	s := h.reg.Cost()
	if s.Escalated != 5 {
		t.Fatalf("escalated = %d, want all five facts counted as having reached the person", s.Escalated)
	}
	if s.Cards != 1 {
		t.Fatalf("cards = %d, want the person interrupted once", s.Cards)
	}
}

// A fact that was not woken about because the coordinator was already awake
// says so. A blank outcome would read as "nothing happened", which is the one
// thing that did not.
func TestACoalescedFactRecordsWhyItWasNotWokenAbout(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	for _, w := range workers[:2] {
		if _, err := h.reg.Declared(ctx, w.ID, testLiveness(), Declaration{OK: false}); err != nil {
			t.Fatalf("declare %s: %v", w.ID, err)
		}
	}
	var coalesced int
	for _, f := range h.reg.Undispatched() {
		if f.Wake.Delivered {
			continue
		}
		coalesced++
		if f.Wake.Reason == "" {
			t.Fatalf("fact %s was silently not woken about: %+v", f.Participant, f.Wake)
		}
	}
	if coalesced != 1 {
		t.Fatalf("coalesced facts = %d, want 1", coalesced)
	}
}
