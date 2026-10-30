package transport

// The approval wire shapes against their contracts (nocx-z9hj4, contracts/
// README row 3 — the real payload off the real socket, not a test-built
// one): the agent.approvalRequested notification's params satisfy
// agent.approvalRequested.schema.json; agent.approve's request and result
// satisfy their separate contracts, including the warning a person sees.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
)

// The DTO conformance: field tags, enum spelling and the omitted
// egress-only fields.
func TestAgentApprovalRequested_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")

	cases := map[string]agentApprovalRequested{
		"policy": {
			RunID: "7", Attempt: 1, Tool: "files.read", CallID: "call_1",
			ArgHash: "hash-a", Arguments: `{"path":"/repo/a.txt"}`,
			Reason: "policy", Effect: "observe",
			Standing: agentApprovalStanding{Reason: "the call has no invocation to show"},
			Resource: &content.GrantScope{Kind: content.ResourcePath, ID: "/repo/a.txt"},
		},
		"egress with findings": {
			RunID: "7", Attempt: 1, Tool: "files.read", CallID: "call_1",
			ArgHash: "hash-a", Arguments: `{"path":"/repo/a.txt"}`,
			Reason: "egress", Effect: "observe", WasError: true,
			Standing: agentApprovalStanding{Reason: "standing answers are not offered for result disclosure"},
			Findings: []assistant.EgressFinding{{
				Source: assistant.EgressFindingKnown, SecretName: "github-token", Start: 0, End: 5,
			}},
		},
	}
	for name, dto := range cases {
		raw, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		validateJSON(t, schema, raw, "agent.approvalRequested DTO ("+name+")")
	}
}

// The real notification off the real socket satisfies its contract — the
// assertion that would catch a field nobody sends.
func TestAgentApprovalRequested_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")
	const args = `{"path":"/repo/a.txt"}`
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", args, "hash-a")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	validateJSON(t, schema, raw, "agent.approvalRequested params (real socket)")
}

// schema — every binding field required, additionalProperties false — and
// the same literal payload is what the over-the-socket flow accepts.
func TestAgentApprove_ParamsOverTheSocketConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approve.params.schema.json")
	const args = `{"path":"/repo/a.txt"}`
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", args, "hash-a")},
		{deltas: []string{"done"}},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)

	// The renderer's literal payload — runId as the string the notification
	// carried, never a number a helper would complete the shape with.
	params := `{"runId":` + strconv.Quote(strconv.FormatInt(res.RunID, 10)) +
		`,"attempt":1,"tool":"files.read","callId":"call_1","argHash":"hash-a","approved":true,"scope":"once"}`
	validateJSON(t, schema, []byte(params), "agent.approve params (renderer's literal payload)")

	result, errObj := approveOverWireRaw(t, h.conn, []byte(params), 2)
	if errObj != nil {
		t.Fatalf("agent.approve with the literal payload: %+v", errObj)
	}
	var got approvalWireResult
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("decode approval result: %v", err)
	}
	if got.State != "streaming" {
		t.Fatalf("approve state = %q, want streaming", got.State)
	}
	// The resume ran: the answer streamed and the run completed.
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if !strings.Contains(string(raw), "completed") {
		t.Fatalf("runState = %s, want completed", raw)
	}
}

func TestAgentApprove_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approve.schema.json")
	for name, dto := range map[string]agentApproveResponse{
		"resumed":                     {State: "streaming"},
		"egress declined":             {State: "failed"},
		"standing write failed":       {State: "streaming", Warning: "could not be saved: disk is full"},
		"resume refused with warning": {State: "failed", Warning: "could not be saved: disk is full"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal approval result: %v", err)
			}
			validateJSON(t, schema, raw, "agent.approve result DTO")
		})
	}
}

// Resolve the advertised schemas, not a second spelling of which contract is
// params and which is result. Filing params as the result fails on real socket
// bytes, even when both schema files and the manifest are freshly generated.
func TestAgentApprove_OverTheWireConformsToContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(contractDir, "openrpc.json"))
	if err != nil {
		t.Fatalf("read OpenRPC manifest: %v", err)
	}
	var manifest openRPCManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode OpenRPC manifest: %v", err)
	}
	var method openRPCMethod
	for _, candidate := range manifest.Methods {
		if candidate.Name == "agent.approve" {
			method = candidate
			break
		}
	}
	if len(method.Params) != 1 || method.Result == nil {
		t.Fatal("agent.approve must advertise independent params and result contracts")
	}
	paramsSchema := loadSchema(t, manifestRefFile(t, method.Params[0].Schema.Ref))
	resultSchema := loadSchema(t, manifestRefFile(t, method.Result.Schema.Ref))
	for _, tc := range []struct {
		name     string
		approved bool
		scope    string
		egress   bool
		warning  bool
		state    string
	}{
		{name: "approve once", approved: true, scope: "once", state: "streaming"},
		{name: "policy decline", scope: "once", state: "streaming"},
		{name: "approve with warning", approved: true, scope: "always", warning: true, state: "streaming"},
		{name: "decline with warning", scope: "always", warning: true, state: "streaming"},
		{name: "egress decline", scope: "once", egress: true, state: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var policy assistant.GlobalPolicy = askPolicyStore(t)
			if tc.warning {
				policy = failingPolicyStore{err: errors.New("disk is full")}
			}
			var h *scopeHarness
			if tc.egress {
				h = suspendedEgressRun(t, policy)
			} else {
				h = suspendedRun(t, policy)
			}
			params, err := json.Marshal(h.answer(tc.approved, tc.scope))
			if err != nil {
				t.Fatalf("marshal approval params: %v", err)
			}
			validateJSON(t, paramsSchema, params, "advertised agent.approve params")
			result, errObj := approveOverWireRaw(t, h.conn, params, 2)
			if errObj != nil {
				t.Fatalf("agent.approve: %+v", errObj)
			}
			validateJSON(t, resultSchema, result, "advertised agent.approve result (real socket)")
			if err := validateJSONErr(resultSchema, params); err == nil {
				t.Fatal("result contract accepts the request instead of its response")
			}
			if err := validateJSONErr(paramsSchema, result); err == nil {
				t.Fatal("params contract accepts the response instead of its request")
			}
			var got approvalWireResult
			if err := json.Unmarshal(result, &got); err != nil {
				t.Fatalf("decode approval result: %v", err)
			}
			if got.State != tc.state {
				t.Fatalf("state = %q, want %q", got.State, tc.state)
			}
			if tc.warning {
				if !strings.Contains(got.Warning, "disk is full") {
					t.Fatalf("consumer warning = %q, want the standing-write failure", got.Warning)
				}
			} else if got.Warning != "" {
				t.Fatalf("unexpected warning: %q", got.Warning)
			}
		})
	}
}

func TestAgentApprove_ContractsRejectIncompleteOrMixedMessages(t *testing.T) {
	params := loadSchema(t, "agent.approve.params.schema.json")
	result := loadSchema(t, "agent.approve.schema.json")
	for _, raw := range []string{
		`{"runId":"7","attempt":1,"tool":"files.read","callId":"call_1","argHash":"hash-a","scope":"once"}`,
		`{"runId":"7","attempt":1,"tool":"files.read","callId":"call_1","argHash":"hash-a","approved":null,"scope":"once"}`,
	} {
		if err := validateJSONErr(params, []byte(raw)); err == nil {
			t.Fatalf("params contract accepted an absent decision: %s", raw)
		}
	}
	for _, raw := range []string{
		`{}`,
		`{"state":"streaming","approved":true}`,
		`{"state":"streaming","warning":false}`,
		`{"state":"awaiting_approval"}`,
	} {
		if err := validateJSONErr(result, []byte(raw)); err == nil {
			t.Fatalf("result contract accepted an invalid response: %s", raw)
		}
	}
}

// approveOverWireRaw drives agent.approve with a LITERAL raw params payload
// — the renderer's bytes, never a helper-completed shape.
func approveOverWireRaw(t *testing.T, conn *websocket.Conn, params json.RawMessage, id int) (json.RawMessage, *jsonrpcErrorObj) {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "agent.approve", "params": params})
	if err != nil {
		t.Fatalf("marshal approve: %v", err)
	}
	if werr := conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write approve: %v", werr)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, resp, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("read approve: %v", rerr)
		}
		var env struct {
			ID     *json.RawMessage `json:"id"`
			Error  *jsonrpcErrorObj `json:"error"`
			Result json.RawMessage  `json:"result"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("decode approval response: %v", err)
		}
		if env.ID == nil {
			continue // a notification; keep looking for the response
		}
		return env.Result, env.Error
	}
}

// askObserveStore is the composition-root policy seam with the observe row
// set to ASK — every other row permits. A readScreen call on the run's own
// session is then IN scope and ESCALATES: the shortest real path to a policy
// question whose effect the gate, not the test, decided.
func askObserveStore(t *testing.T) *assistant.GlobalPolicyStore {
	t.Helper()
	m := autonomousMatrixForTests()
	m.Observe = content.EffectRow{Decision: content.DecisionAsk}
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	if err := store.SetPolicy(m); err != nil {
		t.Fatalf("seed global policy: %v", err)
	}
	return store
}

// TestAgentApprovalRequested_RealEscalationOverTheWireNamesItsEffect is
// Task 3's contract check with nothing test-built in the payload: the REAL
// engine calls readScreen, the REAL policy gate escalates it, and the
// notification that crosses the REAL socket is what is validated. The
// scripted-suspension test above proves the transport's shape; only this one
// can prove the EFFECT is the gate's, because only here did a gate decide
// it. A renderer that had to map readScreen → observe itself would be a rule
// keyed by a tool name (ADR-0028 decision 4); this is the field that spares
// it.
func TestAgentApprovalRequested_RealEscalationOverTheWireNamesItsEffect(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")

	fake, srv := newToolCallingServer()
	defer srv.Close()
	client, _, err := newEinoTestClient(t)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(askObserveStore(t)))
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-effect-1", "sessionId": sid, "question": "what is on the screen?", "cwd": "/repo",
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	raw := readNotification(t, h.conn, "agent.approvalRequested", 15*time.Second)
	if raw == nil {
		t.Fatalf("no approvalRequested within 15s; provider requests=%d", fake.requests.Load())
	}
	validateJSON(t, schema, raw, "agent.approvalRequested params (real gate, real socket)")

	var got struct {
		Tool     string `json:"tool"`
		Reason   string `json:"reason"`
		Effect   string `json:"effect"`
		Resource *struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"resource"`
	}
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", uerr, raw)
	}
	if got.Tool != "session.read" || got.Reason != "policy" {
		t.Fatalf("notification = %s, want the policy escalation of session.read", raw)
	}
	if got.Effect != "observe" {
		t.Fatalf("effect = %q, want %q — session.read's declared class, as the gate decided it", got.Effect, "observe")
	}
	if got.Resource == nil {
		t.Fatalf("resource is null, want the session the call named: %s", raw)
	}
	if got.Resource.Kind != "session" || got.Resource.ID != sid {
		t.Fatalf("resource = %+v, want {session %s}", got.Resource, sid)
	}
}

// TestAgentApprovalRequested_EgressArmOverTheWireConformsToContract is the
// "one shape, whichever gate asked" half of the criterion: the egress
// suspension's notification satisfies the SAME schema, effect included. The
// egress surface ignores the field — its answers are allow/deny, once — but a
// required field present on one arm and absent on the other is a schema that
// has stopped being a contract.
func TestAgentApprovalRequested_EgressArmOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: func(runID string) error {
			return &assistant.EgressRequestedError{Request: &assistant.EgressRequest{
				RunID: runID, Attempt: 1, Tool: "files.read", CallID: "call_1",
				Arguments: `{"path":"/repo/a.txt"}`, ArgHash: "hash-a",
				Effect:   content.EffectObserve,
				Resource: &content.GrantScope{Kind: content.ResourcePath, ID: "/repo/a.txt"},
				Findings: []assistant.EgressFinding{{
					Source: assistant.EgressFindingHeuristic, Kind: "openai-api-key", Start: 0, End: 8,
				}},
			}}
		}},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-egress-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	validateJSON(t, schema, raw, "agent.approvalRequested params (egress arm, real socket)")

	var got struct {
		Reason string `json:"reason"`
		Effect string `json:"effect"`
	}
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", uerr, raw)
	}
	if got.Reason != "egress" || got.Effect != "observe" {
		t.Fatalf("notification = %s, want the egress arm carrying effect observe", raw)
	}
}
