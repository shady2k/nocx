package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestRunToolsAgentCallIDsPreserveReservedOrder(t *testing.T) {
	tools := &RunTools{kernel: &effectKernel{order: newTurnOrder()}}
	tools.UseAgentCallIDs()
	first, releaseFirst := tools.ReserveInvocation(ToolInvocation{Method: "files.read"})
	defer releaseFirst()
	discovery, releaseDiscovery := tools.ReserveInvocation(ToolInvocation{Method: "tools.catalogue"})
	defer releaseDiscovery()
	second, releaseSecond := tools.ReserveInvocation(ToolInvocation{Method: "files.read"})
	defer releaseSecond()
	tools.StartToolCall("agent-first")
	tools.StartToolCall("agent-second")
	if discovery.agentCallID != nil {
		t.Fatal("discovery reserved an identity")
	}
	if got := <-second.agentCallID; got != "agent-second" {
		t.Fatalf("second identity = %q", got)
	}
	if got := <-first.agentCallID; got != "agent-first" {
		t.Fatalf("first identity = %q", got)
	}
}

func TestRunToolsAgentCallIDCancellationDoesNotShiftLaterCall(t *testing.T) {
	tools := &RunTools{kernel: &effectKernel{order: newTurnOrder()}}
	tools.UseAgentCallIDs()
	ctx, cancel := context.WithCancel(context.Background())
	first, releaseFirst := tools.ReserveInvocation(ToolInvocation{Context: ctx, Method: "files.read"})
	defer releaseFirst()
	cancel()
	if _, err := tools.Dispatch(first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	second, releaseSecond := tools.ReserveInvocation(ToolInvocation{Method: "files.read"})
	defer releaseSecond()
	tools.StartToolCall("cancelled")
	tools.StartToolCall("next")
	if got := <-second.agentCallID; got != "next" {
		t.Fatalf("next identity = %q", got)
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
	second.RawParams = []byte(fmt.Sprintf(`{"path":%q}`, path))
	tools.StartToolCall("accepted")
	if _, dispatchErr := tools.Dispatch(second); dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if len(calls) == 0 || calls[len(calls)-1].CallID != "accepted" {
		t.Fatalf("calls = %+v", calls)
	}
}
