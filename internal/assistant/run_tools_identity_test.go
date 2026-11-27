package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestRunToolsAgentCallStartsAreMatchedByKey(t *testing.T) {
	tools := &RunTools{kernel: &effectKernel{order: newTurnOrder()}}
	tools.UseAgentCallIDs()
	tools.StartToolCall("1:agent-first", "agent-first")
	second, err := tools.claimAgentStart("2:agent-second")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-second.published:
		t.Fatal("the first start unblocked the second call")
	default:
	}
	tools.StartToolCall("2:agent-second", "agent-second")
	select {
	case <-second.published:
	default:
		t.Fatal("the second start did not unblock its call")
	}
	first, err := tools.claimAgentStart("1:agent-first")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.published:
	default:
		t.Fatal("the first start was lost")
	}
	if _, err := tools.claimAgentStart("1:agent-first"); err == nil {
		t.Fatal("a second request claimed the same start")
	}
}

func TestRunToolsAgentCallIDCancellationDoesNotShiftLaterCall(t *testing.T) {
	tools := &RunTools{kernel: &effectKernel{order: newTurnOrder()}}
	tools.UseAgentCallIDs()
	ctx, cancel := context.WithCancel(context.Background())
	first, releaseFirst := tools.ReserveInvocation(ToolInvocation{Context: ctx, Method: "files.read", AgentCallKey: "1:cancelled"})
	cancel()
	if _, err := tools.Dispatch(first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	releaseFirst()
	second, err := tools.claimAgentStart("2:next")
	if err != nil {
		t.Fatal(err)
	}
	tools.StartToolCall("1:cancelled", "cancelled")
	select {
	case <-second.published:
		t.Fatal("the cancelled call's start reached the next call")
	default:
	}
	tools.StartToolCall("2:next", "next")
	select {
	case <-second.published:
	default:
		t.Fatal("the next call did not receive its start")
	}
}

func TestRunToolsAgentCallIDRefusalDoesNotShiftLaterCall(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	var calls []ToolCall
	tools, err := BindRunTools(context.Background(), cl, AskParams{Grant: &grant, FreshGrant: func() *content.Grant { return &grant }, KnownMaterial: &fakeKnownMaterial{}, AttemptLedger: &fakeLedger{}, RunID: "run-1", SessionID: "pane-a", Attempt: 1, Files: newLocalFileSource(t)}, func(call ToolCall) error { calls = append(calls, call); return nil })
	if err != nil {
		t.Fatal(err)
	}
	tools.UseAgentCallIDs()
	first, err := tools.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.Method = "files.read"
	first.AgentCallKey = "1:refused"
	first.RawParams = []byte(`{"path":"/outside-run-fence"}`)
	tools.StartToolCall("1:refused", "refused")
	if _, dispatchErr := tools.Dispatch(first); dispatchErr == nil {
		t.Fatal("expected refusal")
	}
	path := dir + "/read.txt"
	writeFile(t, path, "hello")
	second, err := tools.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second.Method = "files.read"
	second.AgentCallKey = "2:accepted"
	second.RawParams = []byte(fmt.Sprintf(`{"path":%q}`, path))
	tools.StartToolCall("2:accepted", "accepted")
	if _, dispatchErr := tools.Dispatch(second); dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if len(calls) == 0 || calls[len(calls)-1].CallID != "accepted" {
		t.Fatalf("calls = %+v", calls)
	}
	later := second
	later.AgentCallKey = "3:accepted"
	tools.StartToolCall("3:accepted", "accepted")
	if _, dispatchErr := tools.Dispatch(later); dispatchErr != nil {
		t.Fatalf("later call with the same model ID: %v", dispatchErr)
	}
	if len(calls) != 2 || calls[0].AgentCallKey != "2:accepted" || calls[1].AgentCallKey != "3:accepted" || calls[1].CallID != "accepted" {
		t.Fatalf("calls after reusing the model ID = %+v", calls)
	}
}
