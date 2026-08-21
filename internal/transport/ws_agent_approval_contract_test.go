package transport

// The approval wire shapes against their contracts (nocx-z9hj4, contracts/
// README row 3 — the real payload off the real socket, not a test-built
// one): the agent.approvalRequested notification's params satisfy
// agent.approvalRequested.schema.json, and the agent.approve request the
// renderer actually sends satisfies agent.approve.schema.json —
// additionalProperties false, every field required.

import (
	"encoding/json"
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
			Resource: &content.GrantScope{Kind: content.ResourcePath, ID: "/repo/a.txt"},
		},
		"egress with findings": {
			RunID: "7", Attempt: 1, Tool: "files.read", CallID: "call_1",
			ArgHash: "hash-a", Arguments: `{"path":"/repo/a.txt"}`,
			Reason: "egress", Effect: "observe", WasError: true,
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
	schema := loadSchema(t, "agent.approve.schema.json")
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

	got, errObj := approveOverWireRaw(t, h.conn, []byte(params), 2)
	if errObj != nil {
		t.Fatalf("agent.approve with the literal payload: %+v", errObj)
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

// approveOverWireRaw drives agent.approve with a LITERAL raw params payload
// — the renderer's bytes, never a helper-completed shape.
func approveOverWireRaw(t *testing.T, conn *websocket.Conn, params json.RawMessage, id int) (approvalWireResult, *jsonrpcErrorObj) {
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
			ID     *json.RawMessage   `json:"id"`
			Error  *jsonrpcErrorObj   `json:"error"`
			Result approvalWireResult `json:"result"`
		}
		_ = json.Unmarshal(resp, &env)
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

	fake, srv := newToolCallingServer("") // session filled per ask below
	defer srv.Close()
	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(askObserveStore(t)))
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)
	fake.session = sid

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
	if got.Tool != "readScreen" || got.Reason != "policy" {
		t.Fatalf("notification = %s, want the policy escalation of readScreen", raw)
	}
	if got.Effect != "observe" {
		t.Fatalf("effect = %q, want %q — readScreen's declared class, as the gate decided it", got.Effect, "observe")
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
