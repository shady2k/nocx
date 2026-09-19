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
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// contractPaneReader is session.read's assistant.PaneReader over the real
// endpoint (nocx-6q1uh.8): internal/toolendpoint cannot construct
// internal/app's real one (app depends on assistant and toolendpoint, not
// the reverse), so this fake stands in — the same shape RunContext.SessionReads
// carries it in production, asserted back to assistant.PaneReader at
// assistant's own point of use.
type contractPaneReader struct{}

func (contractPaneReader) Read(_ context.Context, _ any, _ string, want *sessionruntime.TargetKind, _ *sessionruntime.RowRange) (assistant.PaneRead, error) {
	read := assistant.PaneRead{
		Frame:          paneview.Frame{Rows: 1, Cols: 2, Lines: [][]paneview.Cell{{{Text: "h"}, {Text: "i"}}}},
		Classification: agentdriver.StateFreeText,
		ReadBarrier:    true,
	}
	if want != nil {
		read.Target = &assistant.TargetView{
			Token: "tok-1", TokenID: "tid-1", Kind: *want,
			Rows:      sessionruntime.RowRange{First: 0, Last: 0},
			Region:    "hi",
			ExpiresAt: time.Now().Add(time.Minute),
		}
	}
	return read, nil
}

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
		// The briefing reached the queue — what the real Registrar reports for
		// a spawn whose pane was free (nocx-luqz9.5). Typed stays false, which
		// is also what production answers: the task is typed later by the
		// queue, never synchronously by the spawn.
		Delivery: workers.TaskDelivery{BriefingQueued: true},
	}, nil
}

func (contractWorkerRecord) HeldBy(context.Context, string) ([]workers.Participant, error) {
	return contractWorkerParticipants(), nil
}

func (contractWorkerRecord) Say(context.Context, workers.ID, workers.ReaderID, workers.ReaderID, string) (workers.Message, error) {
	return workers.Message{ID: "message-1", Seq: 1}, nil
}

// Report answers with the committed row the executor renders, kind and all
// (nocx-luqz9.4). It is here rather than in the socket test's own double
// because this type is the one the endpoint's whole contract suite uses, and a
// seam it cannot satisfy would make every case in this file fail to build.
func (contractWorkerRecord) Report(context.Context, workers.ParticipantID, workers.Report) (workers.Message, error) {
	return workers.Message{ID: "message-1", Seq: 1, Kind: workers.KindDone}, nil
}

// Inbox answers with BOTH shapes a mailbox holds (nocx-luqz9.2): a message
// somebody wrote and an observation nocx made. The conformance case below is
// the only place this endpoint's answer to workers.inbox is validated against
// its contract, and a stub that returned an empty page would exercise neither
// list's item schema — which is exactly the hole the vault.status failure this
// whole directory was written from left open.
func (contractWorkerRecord) Inbox(context.Context, workers.ReaderID, workers.ReaderID, int) (workers.Fetch, error) {
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	return workers.Fetch{
		Messages: []workers.Message{
			{ID: "message-1", Seq: 1, Sender: workers.ReaderID("sess-coordinator"), Body: "the wire is a party", CommittedAt: at},
			{
				ID: "message-2", Seq: 2, Sender: workers.ReaderID("nocx"),
				Observed: &workers.Observed{
					Worker: workers.ParticipantID("worker-1"),
					State:  workers.ObservedIdle,
					At:     at,
				},
			},
		},
		Cursor: workers.Cursor{Fetched: 2},
	}, nil
}

func (contractWorkerRecord) Undelivered(context.Context, workers.ID) ([]workers.Message, error) {
	return []workers.Message{}, nil
}

func (contractWorkerRecord) Acknowledge(context.Context, workers.ReaderID, workers.ReaderID, int64) error {
	return nil
}

func (contractWorkerRecord) Close(context.Context, string, workers.ParticipantID) (workers.CloseResult, error) {
	return workers.CloseResult{}, nil
}

func (contractWorkerRecord) Undispatched() []workers.Fact { return nil }

// LeftoverCheckouts answers a WHOLE row and an unreadable one, complete —
// for the same reason Inbox above answers both shapes a mailbox holds: the
// socket conformance cases below are the only place this endpoint's answers
// are validated against their contracts, and a stub that returned an empty
// survey would exercise neither the leftover item schema's fields nor the
// unreadable row's no-answer shape.
func (contractWorkerRecord) LeftoverCheckouts(context.Context, string) workers.CheckoutSurvey {
	return workers.CheckoutSurvey{
		Leftovers: []workers.LeftoverCheckout{
			{
				Path: "/wt/nocx-verify", Branch: "feat/verify",
				Uncommitted: true, Ahead: 1, Readable: true,
				LastUsed: time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC),
				Name:     "worker-1", Task: "verify the wire",
			},
			{Path: "/wt/nocx-dark", Branch: "feat/dark", Readable: false},
		},
		Complete: true,
	}
}

func contractWorkerParticipants() []workers.Participant {
	return []workers.Participant{{
		ID:    "worker-1",
		Group: workers.ID("session-1"),
		Role:  workers.RoleWorker,
		State: workers.StateLive,
		Task:  "verify the wire",
		// The worker holds the checkout its spawn created, which is what
		// makes the holdings answer name it BESIDE the worker and never in
		// the leftover list beside it (nocx-xn63t.1.4).
		Worktree: workers.Worktree{Path: "/wt/nocx-held", Branch: "feat/held"},
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
			// The workspace sub-scope a coordinator's grant carries since
			// nocx-luqz9.2: it is what offers workers.inbox, whose declaration
			// names that kind (A11) and whose narrow resolves its resource from
			// the run's own workspace. Without it the case below would be
			// refused by the reachability gate before it ever reached a socket.
			{Kind: content.ResourceWorkspace, ID: agenttools.ParticipantWorkspaceScopeID(contractWorkspace)},
		},
	}
}

// contractWorkspace is the workspace every grant here names, and the one the
// invocation's RunContext carries: the two MUST be the same string, because
// workers.inbox's resolver reads the run context while the grant is what has to
// cover the resource that resolver returns.
const contractWorkspace = "workspace:default"

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
			// The workspace the grant above covers, for workers.inbox: its
			// resolver names the resource from HERE, and the grant has to
			// contain what it names.
			Workspace: contractWorkspace,
			// PaneAccess/SessionReads (nocx-6q1uh.8, design §7.1): `any`
			// stand-ins for internal/app's real DescendantPaneAccess and
			// PaneReader, exactly the shape RunContext carries them in —
			// this test cannot name either concrete type (toolendpoint
			// depends on assistant, not on app), which is the point:
			// session.read with a sessionId naming a descendant reaches
			// them through the SAME capability every other method reaches
			// its own seams through.
			PaneAccess:   "fake-pane-access",
			SessionReads: contractPaneReader{},
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
		{method: "workers.close", params: `{"worker":"worker-1"}`, result: "workers.close"},
		// nocx-luqz9.2: the coordinator's OWN mailbox, over the same socket —
		// the shape of answer the worker above receives, and the one the
		// observations a worker's pane produces arrive in.
		{method: "workers.inbox", params: `{}`, result: "workers.inbox"},
		// A descendant's pane, read through the helper-backed path this
		// task adds (nocx-6q1uh.8): sessionId differs from the admitted
		// session, so this exercises PaneReader over the real endpoint,
		// never RendererRequester (there is none wired here at all).
		{method: "session.read", params: `{"sessionId":"worker-session-1","target":"region"}`, result: "session.read"},
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
	// Not registry.ForGrant(grant) directly: this dispatcher's own
	// orchestrationMethodNames allowlist refuses session.list, session.run
	// and session.wait even though ForGrant's resource/effect projection
	// permits them under this grant shape, and Catalogue's own job
	// (nocx-6q1uh.16, internal/assistant/dispatch.go) is to filter exactly
	// those out before this test's "expected" set is built — asserting
	// against the unfiltered projection would reintroduce the mismatch this
	// endpoint used to ship (tools.catalogue offering a tool Dispatch then
	// refused with ErrUnreachableMethod). This test's own job is the wire
	// shape and the admitted-grant/ignored-params behavior below, not
	// re-deriving Catalogue's admission rules a second time.
	cataloguer, ok := dispatcher.(assistant.ToolCatalogue)
	if !ok {
		t.Fatalf("dispatcher does not implement assistant.ToolCatalogue")
	}
	expected := cataloguer.Catalogue(grant)
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

// A WITHDRAWN TOOL IS ABSENT FROM THE CATALOGUE AND REFUSED ON THE SOCKET —
// the acceptance check for the removal itself (nocx-luqz9.6, design §7).
//
// BOTH HALVES MATTER AND THEY ARE DIFFERENT FAILURES. A catalogue still
// advertising a call the dispatcher no longer has is the mismatch
// nocx-6q1uh.16 exists to prevent, and the model would spend a turn discovering
// it. A name that is merely gone from the registry but still accepted by the
// endpoint would be worse: it would answer something.
//
// The name is spelled here rather than taken from a constant, deliberately: a
// test that read the removed symbol from the code would stop compiling with it
// and would prove nothing about the WIRE, which is what a model sees.
func TestGroupEndpoint_AWithdrawnToolIsNeitherOfferedNorAnswered(t *testing.T) {
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
		RunContext: agenttools.RunContext{RunID: "run-1", Session: "session-1", Workspace: contractWorkspace},
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
	// The catalogue a coordinator-shaped grant is offered, by name. The grant
	// here is the one every case above uses, so this reads the FULL surface —
	// a name missing from it is missing from the product's own offer.
	if _, werr := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"tools.catalogue","params":{"name":"workers.holdings"}}`+"\n"); werr != nil {
		t.Fatalf("write catalogue request: %v", werr)
	}
	catalogue := readResponse(t, conn)
	if catalogue.Error != nil {
		t.Fatalf("catalogue response error = %+v", catalogue.Error)
	}
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(catalogue.Result, &result); err != nil {
		t.Fatalf("decode catalogue result: %v", err)
	}
	offered := make(map[string]bool, len(result.Tools))
	for _, tool := range result.Tools {
		offered[tool.Name] = true
	}
	if offered["workers.wait"] {
		t.Fatalf("the catalogue still offers workers.wait, which no longer exists: %v", result.Tools)
	}
	// The neighbours are still offered, so "absent" is a statement about this
	// one name rather than about an empty catalogue.
	for _, name := range []string{"workers.spawn", "workers.holdings", "workers.close", "workers.inbox"} {
		if !offered[name] {
			t.Fatalf("the catalogue lost %q as well, so this proves nothing about the withdrawal: %v", name, result.Tools)
		}
	}

	// AND A CALL TO IT IS REFUSED AS UNKNOWN. This is the socket half, driven
	// over the same connection type the catalogue was read on.
	if _, werr := io.WriteString(conn, `{"jsonrpc":"2.0","id":2,"method":"workers.wait","params":{}}`+"\n"); werr != nil {
		t.Fatalf("write the withdrawn call: %v", werr)
	}
	answer := readResponse(t, conn)
	if answer.Error == nil {
		t.Fatalf("workers.wait was ANSWERED after its removal: %s", answer.Result)
	}
	if answer.Error.Code != rpcMethodNotFound {
		t.Fatalf("workers.wait refusal code = %d (%s), want %d: a caller must be told the tool does not exist, not that it failed",
			answer.Error.Code, answer.Error.Message, rpcMethodNotFound)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var _ assistant.WorkerRecord = contractWorkerRecord{}
