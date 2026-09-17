package toolendpoint

// The waiting shape of workers.spawn, off the real socket (nocx-f545a.3).
//
// TestGroupEndpoint_OverTheWireConformsToContract carries a spawn whose task
// was typed. The other answer — a live worker whose pane stopped on a question
// — is a different payload with a field the first never sends, and AGENTS.md
// testing rule 5 is explicit that a payload the test built proves the struct
// is well-formed, not that the server sends it. So this one goes over the wire
// too.

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/workers"
)

// waitingWorkerRecord is the contract record whose spawn meets a question.
type waitingWorkerRecord struct{ contractWorkerRecord }

func (waitingWorkerRecord) Register(ctx context.Context, req workers.RegisterRequest) (workers.Registration, error) {
	reg, err := contractWorkerRecord{}.Register(ctx, req)
	reg.Delivery = workers.TaskDelivery{WaitingOn: "permission_choice"}
	return reg, err
}

func TestAWaitingSpawn_OverTheWireConformsToContract(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry,
		waitingWorkerRecord{},
		content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	auth := &testAuthorizer{inv: assistant.ToolInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{RunID: "run-1", Session: "session-1"},
		Grant:      contractGrant(),
	}}
	endpoint := startEndpoint(t, Config{
		Dir:      t.TempDir(),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatcher,
		Logger:   testLogger(),
	})

	conn := dialEndpoint(t, endpoint)
	defer func() { _ = conn.Close() }()
	request := `{"jsonrpc":"2.0","id":1,"method":"workers.spawn","params":{"command":"claude","task":"verify the wire"}}` + "\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error != nil {
		t.Fatalf("a spawn whose pane asked a question came back as an error: %+v", response.Error)
	}
	validateGroupResult(t, workerResultSchema(t, "workers.spawn"), response.Result, "workers.spawn")

	var got struct {
		State     string `json:"state"`
		TaskTyped bool   `json:"taskTyped"`
		WaitingOn string `json:"waitingOn"`
	}
	if err := json.Unmarshal(response.Result, &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if got.State != "live" || got.TaskTyped || got.WaitingOn != "permission_choice" {
		t.Fatalf("result = %+v, want a live worker whose task waits on permission_choice", got)
	}
}
