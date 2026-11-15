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

// A person's message during a pending approval declines the call they were
// asked about AND the calls already queued behind it — and nothing else
// (ADR-anntnxdj decision 4). The calls the agent proposes after it has read
// the refusal are asked about normally: the person answered the calls they
// could see, not the rest of the turn.
func TestThePersonsMessageDeclinesOnlyTheCallsQueuedAtThatMoment(t *testing.T) {
	policy := autonomousMatrix()
	policy.Observe.Decision = "ask"
	grant, dir := testDirGrant(t, policy)
	path := filepath.Join(dir, "read.txt")
	writeFile(t, path, "read me")
	created := filepath.Join(dir, "created.txt")
	other := filepath.Join(dir, "other.txt")
	writeFile(t, other, "other")
	approvals := NewApprovalStore()
	// The sentence the transport settles a person's message with: the words
	// themselves are in the call's result, which is what the agent reads.
	const message = "use the second log instead"
	sentence := PersonMessageResult("files.read", message)
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: sentence}, kind: DeclineCallOnce}
	k := kernelForQuestioner(t, grant, &fakeLedger{}, q)
	k.approvals = approvals
	tools := &RunTools{kernel: k}

	// The call the person was asked about, and the one already queued behind
	// it when they typed.
	first, releaseFirst := tools.ReserveInvocation(ToolInvocation{Context: context.Background(), Method: "files.read", RawParams: []byte(fmt.Sprintf(`{"path":%q}`, path))})
	defer releaseFirst()
	second, releaseSecond := tools.ReserveInvocation(ToolInvocation{Context: context.Background(), Method: "files.create", RawParams: []byte(fmt.Sprintf(`{"path":%q,"content":"must not exist"}`, created))})
	defer releaseSecond()
	secondDone := make(chan error, 1)
	go func() { _, err := tools.Dispatch(second); secondDone <- err }()
	_, firstErr := tools.Dispatch(first)
	releaseFirst()
	secondErr := <-secondDone
	// The queue is empty again: the next call is a NEW arrival, which is the
	// fact this test is about.
	releaseSecond()

	for name, err := range map[string]error{"the call that asked": firstErr, "the call queued behind it": secondErr} {
		var refusal *DispatchRefusalError
		if !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, message) {
			t.Fatalf("%s was not refused in the person's words: %v", name, err)
		}
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the queued mutation ran anyway: %v", err)
	}

	// The call the agent makes AFTER reading the refusal is a new question,
	// not something the message declined.
	q.answer = Approved{}
	third, releaseThird := tools.ReserveInvocation(ToolInvocation{Context: context.Background(), Method: "files.read", RawParams: []byte(fmt.Sprintf(`{"path":%q}`, other))})
	defer releaseThird()
	if _, err := tools.Dispatch(third); err != nil {
		t.Fatalf("a call after the person's message was refused by it: %v", err)
	}
	if len(q.asked) != 2 {
		t.Fatalf("questions asked = %d, want the one the message answered and the new one", len(q.asked))
	}
}
