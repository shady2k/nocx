package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestRunToolsAgentCallStartsAreMatchedByID(t *testing.T) {
	tools := &RunTools{kernel: &effectKernel{order: newTurnOrder()}}
	tools.UseAgentCallIDs()
	tools.StartToolCall("agent-first")
	second, err := tools.claimAgentStart("agent-second")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-second:
		t.Fatal("the first start unblocked the second call")
	default:
	}
	tools.StartToolCall("agent-second")
	select {
	case <-second:
	default:
		t.Fatal("the second start did not unblock its call")
	}
	first, err := tools.claimAgentStart("agent-first")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
	default:
		t.Fatal("the first start was lost")
	}
	if _, err := tools.claimAgentStart("agent-first"); err == nil {
		t.Fatal("a second request claimed the same start")
	}
}

func TestRunToolsAgentCallIDCancellationDoesNotShiftLaterCall(t *testing.T) {
	tools := &RunTools{kernel: &effectKernel{order: newTurnOrder()}}
	tools.UseAgentCallIDs()
	ctx, cancel := context.WithCancel(context.Background())
	first, releaseFirst := tools.ReserveInvocation(ToolInvocation{Context: ctx, Method: "files.read", AgentCallID: "cancelled"})
	cancel()
	if _, err := tools.Dispatch(first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	releaseFirst()
	second, err := tools.claimAgentStart("next")
	if err != nil {
		t.Fatal(err)
	}
	tools.StartToolCall("cancelled")
	select {
	case <-second:
		t.Fatal("the cancelled call's start reached the next call")
	default:
	}
	tools.StartToolCall("next")
	select {
	case <-second:
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
	tools, err := BindRunTools(context.Background(), cl, AskParams{Grant: &grant, FreshGrant: func() *content.Grant { return &grant }, KnownMaterial: &fakeKnownMaterial{}, AttemptLedger: &fakeLedger{}, RunID: "run-1", SessionID: "pane-a", Attempt: 1, Environment: localPaneEnvironment(), Files: newLocalFileSource(t)}, func(call ToolCall) error { calls = append(calls, call); return nil })
	if err != nil {
		t.Fatal(err)
	}
	tools.UseAgentCallIDs()
	first, err := tools.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.Method = "files.read"
	first.AgentCallID = "refused"
	first.RawParams = []byte(`{"path":"/outside-run-fence"}`)
	tools.StartToolCall("refused")
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
	second.AgentCallID = "accepted"
	second.RawParams = []byte(fmt.Sprintf(`{"path":%q}`, path))
	tools.StartToolCall("accepted")
	if _, dispatchErr := tools.Dispatch(second); dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if len(calls) == 0 || calls[len(calls)-1].CallID != "accepted" {
		t.Fatalf("calls = %+v", calls)
	}
}
