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

	"github.com/shady2k/nocx/internal/session"
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

// finish ends a worker the way an ordinary one ends: its shell exits, which is
// the only fact the record has left to route.
func finish(t *testing.T, h *harness, p Participant) {
	t.Helper()
	if _, err := h.reg.Exited(context.Background(), p.ID, testLiveness(), Exit{Cause: string(session.ExitExited)}); err != nil {
		t.Fatalf("exit %s: %v", p.ID, err)
	}
}

// ── the table ─────────────────────────────────────────────────────────────

// THE TABLE, over the one fact that is left. An ordinary end is recorded and
// needs nobody while others still run; the END OF THE WORKER needs judgement.
//
// The two halves are one rule read at two moments. A worker whose shell exited
// with two still running tells the coordinator nothing it did not expect, and
// spending its turn on "yes, one of three is gone" is the poll this mechanism
// exists to replace. The LAST one ending is the worker arriving, which is the
// moment the coordinator exists for — and it is the fact the record OWES
// judgement on, which is what a coordinator is told when it looks.
func TestThreeWorkersRunAndOnlyTheEndOfTheGroupNeedsJudgement(t *testing.T) {
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	finish(t, h, workers[0])
	if got := len(h.reg.Undispatched()); got != 0 {
		t.Fatalf("undispatched after a routine end = %d, want 0", got)
	}
	if got := h.reg.Cost().Routine; got != 1 {
		t.Fatalf("routine = %d, want the one fact nobody had anything to decide about", got)
	}

	finish(t, h, workers[1])
	if got := len(h.reg.Undispatched()); got != 0 {
		t.Fatalf("undispatched after two of three ended = %d, want 0", got)
	}

	finish(t, h, workers[2])
	if got := len(h.reg.Undispatched()); got == 0 {
		t.Fatalf("the worker ended and the record owes nobody judgement")
	}
}

// An end nocx cannot call ordinary — the shell did not exit, the backend LOST
// it — needs judgement whatever else is running. There is no verdict left in
// the record (ADR-0070 decision 3), so the cause is the discriminator: a loss
// is the case nobody expected, and holding it until the rest of the worker
// stopped would report it after the work that depended on it.
func TestALostWorkerNeedsJudgementWhileOthersRun(t *testing.T) {
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	if _, err := h.reg.Exited(context.Background(), workers[0].ID, testLiveness(),
		Exit{Cause: string(session.ExitInterrupted)}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	open := h.reg.Undispatched()
	if len(open) != 1 || open[0].State != StateExited {
		t.Fatalf("undispatched = %+v, want the worker nocx lost", open)
	}
}

// A read that failed is not evidence the worker is finished, and it is not
// evidence that it is not. Judgement is the fail-closed direction: a fact the
// coordinator did not need costs it one look, and a fact it never learns about
// costs it the workers.
func TestAStoreThatCannotSayWhatElseIsRunningNeedsJudgement(t *testing.T) {
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	h.store.setFault("nonterminal", 1)
	h.store.resetCounts()
	finish(t, h, workers[0])
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
		if _, err := h.reg.Exited(ctx, w.ID, testLiveness(),
			Exit{Cause: string(session.ExitInterrupted)}); err != nil {
			t.Fatalf("exit %s: %v", w.ID, err)
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
	// fetch cleared the participants it was told about, so the worker that ends
	// afterwards is owed again. It is a FOURTH one, because a participant
	// produces its exit once — the fetch is what cleared the three, and asking
	// the same one to end twice would be a fact about an already-terminal
	// record.
	workers = append(workers, mustRegister(t, h))
	if _, err := h.reg.Exited(ctx, workers[3].ID, testLiveness(), Exit{Cause: "exited"}); err != nil {
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

	finish(t, h, workers[0]) // an ordinary end, routine
	finish(t, h, workers[1]) // routine again
	if _, err := h.reg.Exited(ctx, workers[2].ID, testLiveness(),
		Exit{Cause: string(session.ExitInterrupted)}); err != nil {
		t.Fatalf("exit: %v", err)
	}

	s := h.reg.Cost()
	if s.Routine != 2 {
		t.Fatalf("routine = %d, want the two facts nobody had to decide about", s.Routine)
	}
	if s.Judgement != 1 {
		t.Fatalf("judgement = %d, want 1", s.Judgement)
	}
	if s.Facts() != 3 {
		t.Fatalf("facts = %d, want every fact in the denominator", s.Facts())
	}

	if _, err := h.reg.HeldBy(ctx, coordSession); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if got := h.reg.Cost().Dispatched; got != 1 {
		t.Fatalf("dispatched = %d, want 1", got)
	}
}
