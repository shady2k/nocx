package toolendpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/workers"
)

const toolContractDir = "../../contracts/tools"

type contractWorkerRecord struct{}

func (contractWorkerRecord) Register(_ context.Context, req workers.RegisterRequest) (workers.Registration, error) {
	return workers.Registration{
		Participant: workers.Participant{
			ID:    "worker-1",
			Group: workers.ID("session-1"),
			Role:  workers.RoleWorker,
			State: workers.StateLive,
			Task:  req.Task,
		},
		Delivery: workers.TaskDelivery{Typed: true},
	}, nil
}

func (contractWorkerRecord) HeldBy(context.Context, string) ([]workers.Participant, error) {
	return contractWorkerParticipants(), nil
}

func (contractWorkerRecord) Say(context.Context, workers.ID, workers.ReaderID, workers.ReaderID, string) (workers.Message, error) {
	return workers.Message{ID: "message-1", Seq: 1}, nil
}

func (contractWorkerRecord) Inbox(context.Context, workers.ReaderID, workers.ReaderID, int) (workers.Fetch, error) {
	return workers.Fetch{Messages: []workers.Message{}, Cursor: workers.Cursor{}}, nil
}

func (contractWorkerRecord) Undelivered(context.Context, workers.ID) ([]workers.Message, error) {
	return []workers.Message{}, nil
}

func (contractWorkerRecord) Acknowledge(context.Context, workers.ReaderID, workers.ReaderID, int64) error {
	return nil
}

func (contractWorkerRecord) Wait(context.Context, string, workers.ID) ([]workers.Participant, error) {
	return contractWorkerParticipants(), nil
}

func (contractWorkerRecord) Close(context.Context, string, workers.ParticipantID) error {
	return nil
}

func (contractWorkerRecord) Undispatched() []workers.Fact { return nil }

func contractWorkerParticipants() []workers.Participant {
	return []workers.Participant{{
		ID:    "worker-1",
		Group: workers.ID("session-1"),
		Role:  workers.RoleWorker,
		State: workers.StateLive,
		Task:  "verify the wire",
	}}
}

func contractGrant() content.Grant {
	return content.Grant{
		Effects: []content.Effect{
			content.EffectObserve,
			content.EffectMutateDestructive,
			content.EffectDelegate,
		},
		Scopes: []content.GrantScope{
			{Kind: content.ResourceSession, ID: "session-1"},
			{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
		},
	}
}

func workerResultSchema(t *testing.T, method string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(toolContractDir, method+".schema.json")
	raw, err := os.ReadFile(path) //nolint:gosec // test-only path from fixed method cases
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if decodeErr := json.Unmarshal(raw, &document); decodeErr != nil {
		t.Fatalf("decode %s: %v", path, decodeErr)
	}
	result, ok := document.Defs["result"]
	if !ok {
		t.Fatalf("%s has no $defs.result", path)
	}
	resultDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("decode %s result schema: %v", path, err)
	}
	name := "https://nocx.local/contracts/tools/" + method + ".result.schema.json"
	compiler := jsonschema.NewCompiler()
	if addErr := compiler.AddResource(name, resultDocument); addErr != nil {
		t.Fatalf("add %s result schema: %v", path, addErr)
	}
	schema, err := compiler.Compile(name)
	if err != nil {
		t.Fatalf("compile %s result schema: %v", path, err)
	}
	return schema
}

func validateGroupResult(t *testing.T, schema *jsonschema.Schema, raw json.RawMessage, method string) {
	t.Helper()
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("%s result is not JSON: %v", method, err)
	}
	if err := schema.Validate(document); err != nil {
		t.Fatalf("%s result does not satisfy its contract: %v\npayload: %s", method, err, raw)
	}
}

func TestGroupEndpoint_OverTheWireConformsToContract(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry,
		contractWorkerRecord{},
		content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	auth := &testAuthorizer{inv: assistant.ToolInvocation{
		Context: context.Background(),
		RunContext: agenttools.RunContext{
			RunID:   "run-1",
			Session: "session-1",
		},
		Grant: contractGrant(),
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

	cases := []struct {
		method string
		params string
		result string
	}{
		{method: "workers.holdings", params: `{}`, result: "workers.holdings"},
		{method: "workers.spawn", params: `{"command":"claude","task":"verify the wire"}`, result: "workers.spawn"},
		{method: "workers.say", params: `{"worker":"worker-1","message":"the wire is a party"}`, result: "workers.say"},
		{method: "workers.wait", params: `{}`, result: "workers.wait"},
		{method: "workers.close", params: `{"worker":"worker-1"}`, result: "workers.close"},
	}

	for i, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			conn := dialEndpoint(t, endpoint)
			defer func() { _ = conn.Close() }()
			request := `{"jsonrpc":"2.0","id":` + string(rune('1'+i)) + `,"method":"` + tc.method + `","params":` + tc.params + "}\n"
			if _, err := io.WriteString(conn, request); err != nil {
				t.Fatalf("write request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error != nil {
				t.Fatalf("response error = %+v", response.Error)
			}
			validateGroupResult(t, workerResultSchema(t, tc.result), response.Result, tc.method)
		})
	}
}

func TestGroupEndpoint_CatalogueUsesAdmittedGrantAndIgnoresParams(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry,
		contractWorkerRecord{},
		content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	grant := contractGrant()
	auth := &testAuthorizer{inv: assistant.ToolInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{RunID: "run-1", Session: "session-1"},
		Grant:      grant,
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
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"tools.catalogue","params":{"name":"workers.inbox"}}`+"\n"); err != nil {
		t.Fatalf("write catalogue request: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error != nil {
		t.Fatalf("catalogue response error = %+v", response.Error)
	}
	assertCatalogueToolKeys(t, response.Result)
	var result struct {
		Tools []struct {
			Name    string          `json:"name"`
			Summary string          `json:"summary"`
			Params  json.RawMessage `json:"params"`
			Result  json.RawMessage `json:"result"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode catalogue result: %v", err)
	}
	expected := registry.ForGrant(grant)
	if len(result.Tools) != len(expected) {
		t.Fatalf("catalogue has %d tools, want %d", len(result.Tools), len(expected))
	}
	for i, tool := range expected {
		got := result.Tools[i]
		if got.Name != tool.Name || got.Summary != tool.Description {
			t.Fatalf("catalogue tool %d = %#v, want name %q summary %q", i, got, tool.Name, tool.Description)
		}
		if !bytes.Equal(compactJSON(got.Params), compactJSON(tool.ParamsSchema)) {
			t.Fatalf("catalogue tool %q params = %s, want %s", tool.Name, got.Params, tool.ParamsSchema)
		}
		if !bytes.Equal(compactJSON(got.Result), compactJSON(tool.ResultSchema)) {
			t.Fatalf("catalogue tool %q result = %s, want %s", tool.Name, got.Result, tool.ResultSchema)
		}
	}
}

func TestGroupEndpoint_EmptyCatalogueIsSuccessful(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry,
		contractWorkerRecord{},
		content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	auth := &testAuthorizer{inv: assistant.ToolInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{RunID: "run-1", Session: "session-1"},
		Grant:      content.Grant{},
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
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"tools.catalogue","params":{"name":"workers.spawn"}}`+"\n"); err != nil {
		t.Fatalf("write catalogue request: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error != nil {
		t.Fatalf("catalogue response error = %+v", response.Error)
	}
	var result struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode catalogue result: %v", err)
	}
	if result.Tools == nil {
		t.Fatal("catalogue tools is null, want an empty array")
	}
	if len(result.Tools) != 0 {
		t.Fatalf("catalogue has %d tools, want empty", len(result.Tools))
	}
}

func assertCatalogueToolKeys(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var result struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode catalogue shape: %v", err)
	}
	want := []string{"name", "summary", "params", "result"}
	for i, rawTool := range result.Tools {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawTool, &fields); err != nil {
			t.Fatalf("decode catalogue tool %d: %v", i, err)
		}
		if len(fields) != len(want) {
			t.Fatalf("catalogue tool %d has fields %v, want exactly %v", i, fields, want)
		}
		for _, key := range want {
			if _, ok := fields[key]; !ok {
				t.Fatalf("catalogue tool %d lacks %q: %s", i, key, rawTool)
			}
		}
	}
}

func compactJSON(raw []byte) []byte {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return raw
	}
	return compact.Bytes()
}

func TestGroupEndpoint_ParamsUseTheReferencedToolSchemas(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(registry, contractWorkerRecord{}, content.EnvironmentIDFor(content.EnvLocal, ""))
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

	cases := []struct {
		method string
		params string
	}{
		{method: "workers.holdings", params: `{"unexpected":true}`},
		{method: "workers.spawn", params: `{"command":"claude"}`},
		{method: "workers.say", params: `{"worker":"worker-1"}`},
		{method: "workers.wait", params: `{"seconds":0}`},
		{method: "workers.close", params: `{}`},
	}
	for i, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			conn := dialEndpoint(t, endpoint)
			defer func() { _ = conn.Close() }()
			request := `{"jsonrpc":"2.0","id":` + string(rune('1'+i)) + `,"method":"` + tc.method + `","params":` + tc.params + "}\n"
			if _, err := io.WriteString(conn, request); err != nil {
				t.Fatalf("write request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error == nil || response.Error.Code != rpcInvalidParams {
				t.Fatalf("response error = %+v, want invalid params", response.Error)
			}
		})
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var _ assistant.WorkerRecord = contractWorkerRecord{}
