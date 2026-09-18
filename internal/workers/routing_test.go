package workers

// The routing table, and the number the design is judged by (nocx-dkawo.4,
// kept by nocx-luqz9.3).
//
// The design calls this "the question that decides whether any of this is
// useful": which facts are routine, which need judgement, and — where the
// mechanism that used to answer it has been replaced — what the record still
// reports about either. Get the table wrong in one direction and the
// coordinator is told about every completion, which is the poll this mechanism
// replaced; get it wrong in the other and the end of the worker reaches nobody.
//
// WHAT THIS FILE NO LONGER ASSERTS is the per-fact wake and its deadline. Both
// are deleted (design §5, wake_test.go): the wake is about a coordinator's
// MAILBOX, and a fact about a worker is not a message in it. What survives is
// the table itself and the counters, because they are statements about the WORK
// and not about timers.

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
			Group: testGroup, CoordinatorSession: coordSession, Role: RoleWorker,
			Task: fmt.Sprintf("task %d", i+1), Command: "claude",
		})
		if err != nil {
			t.Fatalf("register worker %d: %v", i+1, err)
		}
		out = append(out, p.Participant)
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

// THE TABLE. A routine completion is recorded and needs nobody; the end of the
// worker needs judgement.
//
// The two halves are one rule read at two moments. A worker finishing with two
// still running tells the coordinator nothing it did not expect, and spending
// its turn on "yes, one of three is done" is the poll this mechanism exists to
// replace. The LAST one finishing is the worker arriving, which is the moment
// the coordinator exists for — and it is the fact the record OWES judgement
// on, which is what a coordinator is told when it looks.
func TestThreeWorkersRunAndOnlyTheEndOfTheGroupNeedsJudgement(t *testing.T) {
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	finish(t, h, workers[0])
	if got := len(h.reg.Undispatched()); got != 0 {
		t.Fatalf("undispatched after a routine completion = %d, want 0", got)
	}
	if got := h.reg.Cost().Routine; got != 2 {
		t.Fatalf("routine = %d, want the two facts nobody had anything to decide about", got)
	}

	finish(t, h, workers[1])
	if got := len(h.reg.Undispatched()); got != 0 {
		t.Fatalf("undispatched after two of three finished = %d, want 0", got)
	}

	finish(t, h, workers[2])
	if got := len(h.reg.Undispatched()); got == 0 {
		t.Fatalf("the worker finished and the record owes nobody judgement")
	}
}

// A worker that did NOT succeed needs judgement whatever else is running.
// Holding a crash until the worker finishes would report it after the work that
// depended on it, which is the one ordering that cannot be undone.
func TestAWorkerThatDidNotSucceedNeedsJudgementWhileOthersRun(t *testing.T) {
	ctx := context.Background()

	t.Run("it says it failed", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		workers := fanout(t, h, 3)
		if _, err := h.reg.Declared(ctx, workers[0].ID, testLiveness(),
			Declaration{OK: false, Summary: "could not build"}); err != nil {
			t.Fatalf("declare: %v", err)
		}
		if got := len(h.reg.Undispatched()); got != 1 {
			t.Fatalf("undispatched = %d, want 1 for a worker that reported failure", got)
		}
	})

	t.Run("it is gone and never said anything", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		workers := fanout(t, h, 3)
		if _, err := h.reg.Exited(ctx, workers[0].ID, testLiveness(),
			Exit{Cause: "signalled"}); err != nil {
			t.Fatalf("exit: %v", err)
		}
		open := h.reg.Undispatched()
		if len(open) != 1 || open[0].State != StateAbandoned {
			t.Fatalf("undispatched = %+v, want the abandoned worker", open)
		}
	})
}

// A read that failed is not evidence the worker is finished, and it is not
// evidence that it is not. Judgement is the fail-closed direction: a fact the
// coordinator did not need costs it one look, and a fact it never learns about
// costs it the workers.
func TestAStoreThatCannotSayWhatElseIsRunningNeedsJudgement(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	h.store.setFault("nonterminal", 1)
	h.store.resetCounts()
	if _, err := h.reg.Declared(ctx, workers[0].ID, testLiveness(),
		Declaration{OK: true, Summary: "done"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if got := len(h.reg.Undispatched()); got != 1 {
		t.Fatalf("undispatched = %d, want 1: a table that cannot read the worker must not decide routine", got)
	}
}

// ── what one fetch closes ─────────────────────────────────────────────────

// One look answers everything the session owes: the coordinator is handed its
// whole holdings, so every fact about every one of them has reached it.
func TestOneFetchClosesEveryFactTheSessionOwed(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	for _, w := range workers {
		if _, err := h.reg.Declared(ctx, w.ID, testLiveness(),
			Declaration{OK: false, Summary: "no"}); err != nil {
			t.Fatalf("declare %s: %v", w.ID, err)
		}
	}
	if got := len(h.reg.Undispatched()); got != 3 {
		t.Fatalf("undispatched = %d, want all three owed", got)
	}

	if _, err := h.reg.HeldBy(ctx, coordSession); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if got := len(h.reg.Undispatched()); got != 0 {
		t.Fatalf("undispatched after the coordinator looked = %d, want 0", got)
	}

	// And the next fact is a new situation rather than a suppressed one: the
	// fetch cleared the worker, so what comes after it is owed again.
	if _, err := h.reg.Exited(ctx, workers[0].ID, testLiveness(), Exit{Cause: "exited"}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got := len(h.reg.Undispatched()); got != 1 {
		t.Fatalf("undispatched after a new fact = %d, want 1", got)
	}
}

// ── the number the design is judged by ────────────────────────────────────

// §12: what fraction of facts needs JUDGEMENT rather than being routine. If
// most need a person's coordination, the mechanism moved the work to somebody
// and should say so out loud instead of being described as orchestration.
//
// The routine branch is counted for exactly this reason: a table whose routine
// facts left no trace could report the fraction only over the facts it already
// decided were interesting, which is the flattering denominator.
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
		t.Fatalf("routine = %d, want the four facts nobody had to decide about", s.Routine)
	}
	if s.Judgement != 1 {
		t.Fatalf("judgement = %d, want 1", s.Judgement)
	}
	if s.Facts() != 5 {
		t.Fatalf("facts = %d, want every fact in the denominator", s.Facts())
	}

	if _, err := h.reg.HeldBy(ctx, coordSession); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if got := h.reg.Cost().Dispatched; got != 1 {
		t.Fatalf("dispatched = %d, want 1", got)
	}
}
