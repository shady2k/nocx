package waveendpoint

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
	"github.com/shady2k/nocx/internal/wave"
)

const waveContractDir = "../../contracts/tools"

type contractWaveRecord struct{}

func (contractWaveRecord) Register(_ context.Context, req wave.RegisterRequest) (wave.Participant, error) {
	return wave.Participant{
		ID:    "worker-1",
		Wave:  wave.ID("session-1"),
		Role:  wave.RoleWorker,
		State: wave.StateLive,
		Task:  req.Task,
	}, nil
}

func (contractWaveRecord) HeldBy(context.Context, string) ([]wave.Participant, error) {
	return contractWaveParticipants(), nil
}

func (contractWaveRecord) Say(context.Context, wave.ID, wave.ReaderID, wave.ReaderID, string) (wave.Message, error) {
	return wave.Message{ID: "message-1", Seq: 1}, nil
}

func (contractWaveRecord) Inbox(context.Context, wave.ReaderID, wave.ReaderID, int) (wave.Fetch, error) {
	return wave.Fetch{Messages: []wave.Message{}, Cursor: wave.Cursor{}}, nil
}

func (contractWaveRecord) Undelivered(context.Context, wave.ID) ([]wave.Message, error) {
	return []wave.Message{}, nil
}

func (contractWaveRecord) Acknowledge(context.Context, wave.ReaderID, wave.ReaderID, int64) error {
	return nil
}

func (contractWaveRecord) Wait(context.Context, string, wave.ID) ([]wave.Participant, error) {
	return contractWaveParticipants(), nil
}

func (contractWaveRecord) Close(context.Context, string, wave.ParticipantID) error {
	return nil
}

func (contractWaveRecord) Undispatched() []wave.Fact { return nil }

func contractWaveParticipants() []wave.Participant {
	return []wave.Participant{{
		ID:    "worker-1",
		Wave:  wave.ID("session-1"),
		Role:  wave.RoleWorker,
		State: wave.StateLive,
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

func waveResultSchema(t *testing.T, method string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(waveContractDir, method+".schema.json")
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

func validateWaveResult(t *testing.T, schema *jsonschema.Schema, raw json.RawMessage, method string) {
	t.Helper()
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("%s result is not JSON: %v", method, err)
	}
	if err := schema.Validate(document); err != nil {
		t.Fatalf("%s result does not satisfy its contract: %v\npayload: %s", method, err, raw)
	}
}

func TestWaveEndpoint_OverTheWireConformsToContract(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble wave tools: %v", err)
	}
	dispatcher, err := assistant.NewWaveDispatcher(
		registry,
		contractWaveRecord{},
		content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new wave dispatcher: %v", err)
	}
	auth := &testAuthorizer{inv: assistant.WaveInvocation{
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
		{method: "wave.holdings", params: `{}`, result: "wave.holdings"},
		{method: "wave.spawn", params: `{"command":"claude","task":"verify the wire"}`, result: "wave.spawn"},
		{method: "wave.say", params: `{"worker":"worker-1","message":"the wire is a party"}`, result: "wave.say"},
		{method: "wave.wait", params: `{}`, result: "wave.wait"},
		{method: "wave.close", params: `{"worker":"worker-1"}`, result: "wave.close"},
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
			validateWaveResult(t, waveResultSchema(t, tc.result), response.Result, tc.method)
		})
	}
}

func TestWaveEndpoint_ParamsUseTheReferencedToolSchemas(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble wave tools: %v", err)
	}
	dispatcher, err := assistant.NewWaveDispatcher(registry, contractWaveRecord{}, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new wave dispatcher: %v", err)
	}
	auth := &testAuthorizer{inv: assistant.WaveInvocation{
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
		{method: "wave.holdings", params: `{"unexpected":true}`},
		{method: "wave.spawn", params: `{"command":"claude"}`},
		{method: "wave.say", params: `{"worker":"worker-1"}`},
		{method: "wave.wait", params: `{"seconds":0}`},
		{method: "wave.close", params: `{}`},
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

var _ assistant.WaveRecord = contractWaveRecord{}
