package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunToolsReservedApprovalPrecedesLaterMutation(t *testing.T) {
	policy := autonomousMatrix()
	policy.Observe.Decision = "ask"
	grant, dir := testDirGrant(t, policy)
	path := filepath.Join(dir, "read.txt")
	writeFile(t, path, "read me")
	created := filepath.Join(dir, "created.txt")
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: "do not proceed"}}
	k := kernelForQuestioner(t, grant, &fakeLedger{}, q)
	k.approvals = approvals
	tools := &RunTools{kernel: k}
	first, releaseFirst := tools.ReserveInvocation(ToolInvocation{Context: context.Background(), Method: "files.read", RawParams: []byte(fmt.Sprintf(`{"path":%q}`, path))})
	defer releaseFirst()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second, releaseSecond := tools.ReserveInvocation(ToolInvocation{Context: ctx, Method: "files.create", RawParams: []byte(fmt.Sprintf(`{"path":%q,"content":"must not exist"}`, created))})
	defer releaseSecond()
	secondDone := make(chan error, 1)
	go func() { _, err := tools.Dispatch(second); secondDone <- err }()
	_, firstErr := tools.Dispatch(first)
	releaseFirst()
	err := <-secondDone
	var refusal *DispatchRefusalError
	if !errors.As(firstErr, &refusal) || !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, "do not proceed") {
		t.Fatalf("first=%v second=%v; want inherited decline", firstErr, err)
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("later mutation touched disk: %v", err)
	}
	if len(q.asked) != 1 {
		t.Fatalf("approval count = %d, want 1", len(q.asked))
	}
}

func TestTurnReservationCancellationKeepsLaterCallsBehindPredecessor(t *testing.T) {
	order := newTurnOrder()
	first, finishFirst := order.reserve()
	release, _, err := first.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, finishSecond := order.reserve()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := second.enter(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancelled call: %v", err)
	}
	finishSecond()
	third, finishThird := order.reserve()
	// The first call still holds the slot. A later already-cancelled call must
	// remain queued rather than inherit a premature release from the second.
	if _, _, err := third.enter(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("call passed its unsettled predecessor: %v", err)
	}
	finishThird()
	release()
	finishFirst()
}
