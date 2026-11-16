package assistant

// The order of one turn's calls (nocx-a62cl.3.4, ADR-anntnxdj §2): two calls
// against a blocked approver, and the second has not started. Declining the
// first refuses the second with the same reason rather than starting it.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/waittest"
)

// holdingQuestioner is a person who takes the first question and holds it until
// the test answers: the state two concurrent calls are tested against.
// waitForQueued waits until the turn has accepted this many calls. It is the
// observable a person's answer is about when it declines one.
func waitForQueued(t *testing.T, order *TurnOrder, n uint64) {
	t.Helper()
	waittest.WaitFor(t, fmt.Sprintf("the turn to hold %d call(s)", n), func() bool {
		order.mu.Lock()
		defer order.mu.Unlock()
		return order.arrivals >= n
	})
}

type holdingQuestioner struct {
	asked   chan *ApprovalRequest
	answers chan Answer

	mu   sync.Mutex
	asks int
	// cutoff is where the answer settles, as the real approver stamps it.
	cutoff func() uint64
}

func newHoldingQuestioner() *holdingQuestioner {
	return &holdingQuestioner{asked: make(chan *ApprovalRequest, 4), answers: make(chan Answer, 4)}
}

func (q *holdingQuestioner) Ask(_ context.Context, req *ApprovalRequest) Answer {
	q.mu.Lock()
	q.asks++
	n := q.asks
	q.mu.Unlock()
	q.asked <- req
	if n > 1 {
		// A SECOND question is the defect this test exists to catch: with the
		// turn's order the call behind a blocked one never starts, so it is
		// answered here rather than left blocking, and the assertions fail on
		// the outcome.
		return Unanswered{Err: errors.New("a second question was asked: the turn's order is gone")}
	}
	answer := <-q.answers
	if declined, ok := answer.(Declined); ok && declined.Through == 0 && q.cutoff != nil {
		declined.Through = q.cutoff()
		answer = declined
	}
	return answer
}

func (q *holdingQuestioner) AskEgress(_ context.Context, req *EgressRequest) Answer {
	return Unanswered{Err: fmt.Errorf("unexpected egress question for %s", req.Tool)}
}

// TestTwoCallsOfOneTurnRunOneAtATimeInOrder is the DONE WHEN's second
// assertion: two concurrent calls on one turn against a blocked approver. The
// second has not started — one question is ever asked, one proposal is ever
// recorded — and declining the first refuses the second with the SAME reason.
func TestTwoCallsOfOneTurnRunOneAtATimeInOrder(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first")
	writeFile(t, filepath.Join(dir, "b.txt"), "second")
	argsA := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	argsB := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	q := newHoldingQuestioner()
	k := kernelForQuestioner(t, grant, ledger, q)
	k.approvals = approvals
	// The answer settles where the person gave it, as the transport's wait
	// stamps it: the calls queued by then are the ones it covers.
	q.cutoff = k.order.ArrivalCutoff

	type outcome struct {
		out string
		err error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		out, err := k.Invoke(context.Background(), "files.read", "call-1", argsA)
		firstDone <- outcome{out, err}
	}()
	// The first call is now blocked on its question: the approver has it.
	first := <-q.asked
	if first.CallID != "call-1" {
		t.Fatalf("the first question names %q, want call-1", first.CallID)
	}

	secondDone := make(chan outcome, 1)
	go func() {
		out, err := k.Invoke(context.Background(), "files.read", "call-2", argsB)
		secondDone <- outcome{out, err}
	}()
	// THE SECOND CALL IS QUEUED, and that is a fact this test must establish
	// before it answers: a decline covers the calls that are already waiting
	// for their turn, and one that arrives afterwards is a new question
	// (ADR-anntnxdj decision 4). Answering first would leave the test deciding
	// at a random point which of the two the person's no is about.
	waitForQueued(t, k.order, 2)

	// The person says no to the first. The second call is queued behind it,
	// so it is refused with that same reason instead of being asked.
	q.answers <- Declined{Reason: "not this one"}

	gotFirst := <-firstDone
	gotSecond := <-secondDone
	if gotFirst.err != nil || gotSecond.err != nil {
		t.Fatalf("refusals became run errors: %v / %v", gotFirst.err, gotSecond.err)
	}
	if !strings.Contains(gotFirst.out, "not this one") {
		t.Fatalf("the first call answered %q, want the person's no", gotFirst.out)
	}
	if !strings.Contains(gotSecond.out, "not this one") {
		t.Fatalf("the second call answered %q, want the SAME reason the person gave the first", gotSecond.out)
	}
	// One question, one proposal: the second call never started.
	if got := len(q.asked); got != 0 {
		t.Fatalf("%d further question(s) were asked, want none — the second call must not start while the first is open", got)
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("the ledger opened %d attempts, want 1 — only the first call's proposal", got)
	}
	if strings.Contains(gotSecond.out, "first") || strings.Contains(gotSecond.out, "second") {
		t.Fatalf("the refused second call read a file: %q", gotSecond.out)
	}
}

// TestACallQueuedBehindOneIsStoppedByTheTurnsCancellation: a call waiting for
// its turn does not outlive the turn it was queued in — a person's stop ends
// the wait, and the queued call is refused as a cancellation.
func TestACallQueuedBehindOneIsStoppedByTheTurnsCancellation(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first")
	argsA := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	argsB := argsA

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	q := newHoldingQuestioner()
	k := kernelForQuestioner(t, grant, ledger, q)
	k.approvals = approvals
	// The answer settles where the person gave it, as the transport's wait
	// stamps it: the calls queued by then are the ones it covers.
	q.cutoff = k.order.ArrivalCutoff

	go func() {
		_, _ = k.Invoke(context.Background(), "files.read", "call-1", argsA)
	}()
	<-q.asked

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := k.Invoke(ctx, "files.read", "call-2", argsB)
	var refusal *PipelineRefusal
	if !errors.As(err, &refusal) || !errors.Is(err, context.Canceled) {
		t.Fatalf("the queued call = %v, want a cancellation", err)
	}
	// Release the first so nothing is left blocked.
	q.answers <- Declined{Reason: "done"}
}
