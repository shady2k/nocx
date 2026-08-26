package transport

// How far an answer reaches (nocx-ki305, design "The prompt grows six
// answers"): agent.approve carries a scope, and the BACKEND is what applies
// it. "once" is today's behaviour and writes nothing anywhere; "session"
// writes the run's session's overlay; "always" writes ONE row of the global
// matrix through the store. Egress keeps two answers, once only.
//
// Every assertion here is over the REAL socket against the REAL stores: the
// point of the change is that the renderer never edits the matrix, so a test
// that reached into the handler and called the applier would prove the one
// thing that is not in question.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

// scopeHarness is a run suspended on a policy ask in a KNOWN session, over a
// global policy every row of which asks — the state a person is in when the
// six answers are drawn. The session id is the fact the "session" scope is
// about, so the harness carries it rather than deriving it later.
type scopeHarness struct {
	*askHarness
	policy assistant.GlobalPolicy
	sid    string
	runID  int64
}

// suspendedRun leaves a run suspended on a readScreen-shaped policy ask
// (effect observe) and hands back the binding to answer with.
func suspendedRun(t *testing.T, policy assistant.GlobalPolicy) *scopeHarness {
	t.Helper()
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", `{"path":"/repo/a.txt"}`, "hash-a")},
		{deltas: []string{"done"}},
	}}
	return suspendedRunWith(t, policy, client)
}

// suspendedEgressRun is the same run suspended by the EGRESS gate: the
// question a standing answer may never be given to.
func suspendedEgressRun(t *testing.T, policy assistant.GlobalPolicy) *scopeHarness {
	t.Helper()
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: func(runID string) error {
			return &assistant.EgressRequestedError{Request: &assistant.EgressRequest{
				RunID: runID, Attempt: 1, Tool: "files.read", CallID: "call_1",
				Arguments: `{"path":"/repo/a.txt"}`, ArgHash: "hash-a",
				Effect:   content.EffectObserve,
				Resource: &content.GrantScope{Kind: content.ResourcePath, ID: "/repo/a.txt"},
				Findings: []assistant.EgressFinding{{
					Source: assistant.EgressFindingHeuristic, Kind: "openai-api-key",
					Start: 11, End: 43,
				}},
			}}
		}},
		{deltas: []string{"done"}},
	}}
	return suspendedRunWith(t, policy, client)
}

func suspendedRunWith(t *testing.T, policy assistant.GlobalPolicy, client *scriptedApprovalClient) *scopeHarness {
	t.Helper()
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(policy))
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	return &scopeHarness{askHarness: h, policy: policy, sid: sid, runID: res.RunID}
}

// answer is the renderer's literal payload for this run's proposal.
func (s *scopeHarness) answer(approved bool, scope string) map[string]any {
	return map[string]any{
		"runId": strconv.FormatInt(s.runID, 10), "attempt": 1, "tool": "files.read",
		"callId": "call_1", "argHash": "hash-a", "approved": approved, "scope": scope,
	}
}

// approve answers yes and fails the test on an error response.
func (s *scopeHarness) approve(t *testing.T, scope string) approvalWireResult {
	t.Helper()
	got, errObj := approveOverWire(t, s.conn, s.answer(true, scope), 2)
	if errObj != nil {
		t.Fatalf("agent.approve scope %q: %+v", scope, errObj)
	}
	return got
}

// deny answers no. The decline no longer terminalizes (nocx-uvac6.1): the
// refusal becomes the call's result, the run resumes, and the response names
// the run as streaming — there is no runState notification preceding it.
func (s *scopeHarness) deny(t *testing.T, scope string) approvalWireResult {
	t.Helper()
	got, errObj := approveOverWire(t, s.conn, s.answer(false, scope), 2)
	if errObj != nil {
		t.Fatalf("agent.approve deny scope %q: %+v", scope, errObj)
	}
	return got
}

// overlay is what this run's session has been told, read off the server's own
// store rather than a copy the test kept.
func (s *scopeHarness) overlay() content.SessionOverrides {
	return s.ws.sessionPolicy.For(session.ID(s.sid))
}

// failingPolicyStore is a GlobalPolicy whose SetPolicy always refuses — the
// store problem the person must not pay for (AGENTS.md: for every external
// call, a test where that call fails). Its Policy() is the all-ask matrix, so
// the run reaching the ask at all is the same as with a real store.
type failingPolicyStore struct{ err error }

func (f failingPolicyStore) Policy() content.EffectPolicy {
	ask := content.EffectRow{Decision: content.DecisionAsk}
	return content.EffectPolicy{
		Observe: ask, MutateReversible: ask, MutateDestructive: ask,
		PrivilegeChange: ask, Disclose: ask, CrossBoundary: ask, Delegate: ask,
	}
}

func (f failingPolicyStore) SetPolicy(content.EffectPolicy) error { return f.err }

// ── "once": exactly today's behaviour ─────────────────────────────────────

// TestAgentApprove_ScopeOnce_WritesNothing: the default answer is not a
// standing one. Asserted by reading BOTH stores — a test that only checked
// that nothing crashed would pass against an implementation that wrote to
// either.
func TestAgentApprove_ScopeOnce_WritesNothing(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))

	if got := h.approve(t, "once"); got.State != string(content.RunStreaming) {
		t.Fatalf("approve state = %q, want streaming", got.State)
	}

	if got := h.overlay(); len(got) != 0 {
		t.Fatalf("session overlay = %+v, want empty — once writes nothing", got)
	}
	if d := h.policy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("global policy observe = %q, want the untouched ask", d)
	}
}

// ── "session": one session's overlay, and no other's ──────────────────────

func TestAgentApprove_ScopeSession_PermitsTheEffectForThatSessionOnly(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))

	h.approve(t, "session")

	if got := h.overlay()[content.EffectObserve]; got != content.DecisionPermit {
		t.Fatalf("this session's observe = %q, want permit", got)
	}
	if _, ok := h.ws.sessionPolicy.For(session.ID("some-other"))[content.EffectObserve]; ok {
		t.Fatal("another session inherited the answer")
	}
	// A session answer is not a standing one.
	if d := h.policy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("global policy observe = %q, want the untouched ask", d)
	}
}

func TestAgentApprove_ScopeSession_DenyRefusesTheEffectForThatSession(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))

	h.deny(t, "session")

	if got := h.overlay()[content.EffectObserve]; got != content.DecisionRefuse {
		t.Fatalf("this session's observe = %q, want refuse", got)
	}
	if d := h.policy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("global policy observe = %q, want the untouched ask", d)
	}
}

// ── "always": ONE row of the matrix ───────────────────────────────────────

func TestAgentApprove_ScopeAlways_WritesTheRowIntoTheGlobalPolicy(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))

	if got := h.approve(t, "always"); got.Warning != "" {
		t.Fatalf("warning = %q, want none — the write succeeded", got.Warning)
	}

	if d := h.policy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("observe = %q, want permit", d)
	}
	// One row, not the matrix.
	for _, e := range []content.Effect{
		content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose,
		content.EffectCrossBoundary, content.EffectDelegate,
	} {
		if d := h.policy.Policy().DecisionFor(e); d != content.DecisionAsk {
			t.Fatalf("%s = %q, want the untouched ask", e, d)
		}
	}
	// And a standing answer is not a session one: nothing was written to the
	// overlay that would die with the session.
	if got := h.overlay(); len(got) != 0 {
		t.Fatalf("session overlay = %+v, want empty — always is the standing row", got)
	}
}

func TestAgentApprove_ScopeAlways_DenyWritesRefuse(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))

	h.deny(t, "always")

	if d := h.policy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionRefuse {
		t.Fatalf("observe = %q, want refuse", d)
	}
}

// TestAgentApprove_ScopeAlways_KeepsTheRowsScopes: a standing answer changes
// the row's DECISION and never its bound. "Allow always" must not silently
// widen the resources the row was limited to.
func TestAgentApprove_ScopeAlways_KeepsTheRowsScopes(t *testing.T) {
	store := askPolicyStore(t)
	bounded := store.Policy()
	bounded.Observe = content.EffectRow{
		Decision: content.DecisionAsk,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/repo"}},
	}
	if err := store.SetPolicy(bounded); err != nil {
		t.Fatalf("seed bounded row: %v", err)
	}
	h := suspendedRun(t, store)

	h.approve(t, "always")

	got := h.policy.Policy().RowScopes(content.EffectObserve)
	if len(got) != 1 || got[0].ID != "/repo" {
		t.Fatalf("observe scopes = %+v, want the row's own bound kept", got)
	}
}

// ── the store fails, and the person does not pay for it ───────────────────

// TestAgentApprove_ScopeAlways_PolicyWriteFailureStillResumesTheRun: the
// person said yes. A store that cannot record the STANDING part must not cost
// them the call they answered — the run resumes and the result names the
// failure.
func TestAgentApprove_ScopeAlways_PolicyWriteFailureStillResumesTheRun(t *testing.T) {
	h := suspendedRun(t, failingPolicyStore{err: errors.New("disk is full")})

	res := h.approve(t, "always")

	if res.State != string(content.RunStreaming) {
		t.Fatalf("state = %q, want streaming — a failed write must not lose the call", res.State)
	}
	if !strings.Contains(res.Warning, "disk is full") {
		t.Fatalf("warning = %q, want it to name the failure", res.Warning)
	}
	// And the run really did resume: the engine was asked a second time.
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
}

// TestAgentApprove_ScopeAlways_PolicyWriteFailureStillDeclines: the same at
// the other end of the decision. A deny whose standing part could not be
// recorded still goes through as the refusal of THIS call — the run resumes
// with the refusal as that call's result (nocx-uvac6.1) — and says so.
func TestAgentApprove_ScopeAlways_PolicyWriteFailureStillDeclines(t *testing.T) {
	h := suspendedRun(t, failingPolicyStore{err: errors.New("disk is full")})

	res := h.deny(t, "always")

	if res.State != string(content.RunStreaming) {
		t.Fatalf("state = %q, want streaming — a failed standing write must not lose the refusal", res.State)
	}
	if !strings.Contains(res.Warning, "disk is full") {
		t.Fatalf("warning = %q, want it to name the failure", res.Warning)
	}
	ap := assistant.Approval{
		RunID: strconv.FormatInt(h.runID, 10), Attempt: 1, Tool: "files.read",
		CallID: "call_1", ArgHash: "hash-a",
	}
	if kind, ok := h.ws.agentApprovals.DeclinedKind(ap); !ok || kind != assistant.DeclineCallOnce {
		t.Fatalf("declined kind = %q/%v, want once/true — a failed standing write must not claim permanence", kind, ok)
	}
	// And the run really did resume: the engine was asked a second time and
	// streams its answer.
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
}

// ── egress: two answers, once only ────────────────────────────────────────

// TestAgentApprove_EgressRefusesAnythingButOnce: "always send secrets to the
// model provider" is not a standing decision to be made by a button sitting
// next to five others. Asserted by TRYING both.
func TestAgentApprove_EgressRefusesAnythingButOnce(t *testing.T) {
	h := suspendedEgressRun(t, askPolicyStore(t))

	for _, scope := range []string{"session", "always"} {
		_, errObj := approveOverWire(t, h.conn, h.answer(true, scope), 2)
		if errObj == nil || errObj.Code != -32602 {
			t.Fatalf("egress scope %q: got %+v, want -32602", scope, errObj)
		}
		// The refusal wrote nothing either.
		if got := h.overlay(); len(got) != 0 {
			t.Fatalf("egress scope %q wrote a session overlay: %+v", scope, got)
		}
		if d := h.policy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionAsk {
			t.Fatalf("egress scope %q wrote the global row: %q", scope, d)
		}
	}

	if res := h.approve(t, "once"); res.State != string(content.RunStreaming) {
		t.Fatalf("once = %q, want it still accepted for an egress question", res.State)
	}
}

// ── the wire is the contract ──────────────────────────────────────────────

// TestAgentApprove_MissingScopeIsRejected: the schema requires it, and the
// validator is what makes the schema true of the running server. An answer
// with no scope is not "once by default" — a default here would be a standing
// decision nobody expressed.
func TestAgentApprove_MissingScopeIsRejected(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))

	params := h.answer(true, "")
	delete(params, "scope")
	_, errObj := approveOverWire(t, h.conn, params, 2)
	if errObj == nil || errObj.Code != -32602 {
		t.Fatalf("missing scope: got %+v, want -32602", errObj)
	}
}

func TestAgentApprove_UnknownScopeIsRejected(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))

	_, errObj := approveOverWire(t, h.conn, h.answer(true, "forever"), 2)
	if errObj == nil || errObj.Code != -32602 {
		t.Fatalf("unknown scope: got %+v, want -32602", errObj)
	}
}

// TestAgentApprove_ParamsWithScopeConformToContract: the renderer's literal
// payload, scope included, validated against the schema AND accepted by the
// running server — the schema and the validator agreeing is the only thing
// that makes the required field real.
func TestAgentApprove_ParamsWithScopeConformToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approve.schema.json")
	h := suspendedRun(t, askPolicyStore(t))

	params, err := json.Marshal(h.answer(true, "session"))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	validateJSON(t, schema, params, "agent.approve params (scope carried)")

	got, errObj := approveOverWireRaw(t, h.conn, params, 2)
	if errObj != nil {
		t.Fatalf("agent.approve with the literal payload: %+v", errObj)
	}
	if got.State != string(content.RunStreaming) {
		t.Fatalf("approve state = %q, want streaming", got.State)
	}
}

// ── the row an "always" writes is the one a REAL gate decided ─────────────

// TestAgentApprove_ScopeAlways_RealEscalationWritesTheGatesRow holds the
// gate's row for a tool with no command carrier. The real engine calls
// readScreen, the real policy middleware escalates it, and "allow always"
// must write the observe row selected by that gate.
//
// The scripted suspensions above prove the transport applies a scope; only
// this real escalation can prove it applies the scope to the row the GATE
// chose. On the scripted path the transport creates the store record itself
// and could record the effect there; here the record already exists.
func TestAgentApprove_ScopeAlways_RealEscalationWritesTheGatesRow(t *testing.T) {
	fake, srv := newToolCallingServer("")
	defer srv.Close()
	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	policy := askObserveStore(t)
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(policy))
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)
	fake.session = sid

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-always-1", "sessionId": sid, "question": "what is on the screen?", "cwd": "/repo",
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 15*time.Second)
	if raw == nil {
		t.Fatalf("no approvalRequested within 15s; provider requests=%d", fake.requests.Load())
	}
	var n agentApprovalRequested
	if uerr := json.Unmarshal(raw, &n); uerr != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", uerr, raw)
	}

	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": n.RunID, "attempt": n.Attempt, "tool": n.Tool,
		"callId": n.CallID, "argHash": n.ArgHash, "approved": true, "scope": "always",
	}, 3)
	if errObj != nil {
		t.Fatalf("agent.approve: %+v", errObj)
	}
	if got.Warning != "" {
		t.Fatalf("warning = %q, want none — the row was there to write", got.Warning)
	}
	if d := policy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("observe = %q, want permit — the gate's own row, written by the answer", d)
	}
}

// TestAgentApprove_ScopeAlways_RealRunClassificationWritesTheObserveRow
// holds the gate's row for a command whose declared worst case was lowered.
// The model proposes `run` with `df -h`; the gate must present observe, not
// the tool's declared destructive worst case. The stored destructive row
// starts at ask and must remain ask after "allow always".
func TestAgentApprove_ScopeAlways_RealRunClassificationWritesTheObserveRow(t *testing.T) {
	fake, srv := newRunToolCallingServer("")
	defer srv.Close()
	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	policy := askObserveStore(t)
	initial := policy.Policy()
	initial.MutateDestructive = content.EffectRow{Decision: content.DecisionAsk}
	if err := policy.SetPolicy(initial); err != nil {
		t.Fatalf("seed destructive ask row: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(policy))
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)
	fake.args = `{"sessionId":"` + sid + `","command":"df -h"}`

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-always-1", "sessionId": sid, "question": "how much disk is free?", "cwd": "/repo",
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 15*time.Second)
	if raw == nil {
		t.Fatalf("no approvalRequested within 15s; provider requests=%d", fake.requests.Load())
	}
	var n agentApprovalRequested
	if uerr := json.Unmarshal(raw, &n); uerr != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", uerr, raw)
	}
	if n.Tool != "run" {
		t.Fatalf("approval tool = %q, want run", n.Tool)
	}
	if n.Effect != string(content.EffectObserve) {
		t.Fatalf("approval effect = %q, want observe — the call is read-only", n.Effect)
	}

	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": n.RunID, "attempt": n.Attempt, "tool": n.Tool,
		"callId": n.CallID, "argHash": n.ArgHash, "approved": true, "scope": "always",
	}, 3)
	if errObj != nil {
		t.Fatalf("agent.approve: %+v", errObj)
	}
	if got.Warning != "" {
		t.Fatalf("warning = %q, want none — the row was there to write", got.Warning)
	}
	if d := policy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("observe = %q, want permit — allow always writes the call's row", d)
	}
	if d := policy.Policy().DecisionFor(content.EffectMutateDestructive); d != content.DecisionAsk {
		t.Fatalf("mutate-destructive = %q, want untouched ask — df must not grant destructive calls", d)
	}
}

// TestAgentApprove_ScopeAlways_RealRunClassificationReadsTheObserveGrantBeforeDestructiveEscalation
// holds the READ half of the standing answer. It uses the same run tool twice:
// after "allow always" for `df -h`, the next destructive proposal must reach
// the wire as approvalRequested instead of executing. The row assertion above
// proves only the WRITE landed in the observe row; it does not prove that the
// policy read consults that row without widening the answer.
func TestAgentApprove_ScopeAlways_RealRunClassificationReadsTheObserveGrantBeforeDestructiveEscalation(t *testing.T) {
	var sid string
	var requests atomic.Int64
	argsFor := func(command string) string {
		raw, err := json.Marshal(map[string]string{"sessionId": sid, "command": command})
		if err != nil {
			t.Fatalf("marshal run args: %v", err)
		}
		return string(raw)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			streamToolCallChunk(w, "run", argsFor("df -h"))
		case 2:
			streamToolCallChunk(w, "run", argsFor("rm -rf /"))
		default:
			streamOKChunks(w)
		}
	}))
	defer srv.Close()

	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	policy := askObserveStore(t)
	initial := policy.Policy()
	initial.MutateDestructive = content.EffectRow{Decision: content.DecisionAsk}
	if err := policy.SetPolicy(initial); err != nil {
		t.Fatalf("seed destructive ask row: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(policy))
	h.createEndpointAt(srv.URL)

	sid = openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-read-half", "sessionId": sid, "question": "how much disk is free?", "cwd": "/repo",
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 15*time.Second)
	var first agentApprovalRequested
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("first approvalRequested unmarshal: %v\nraw: %s", err, raw)
	}
	if first.Tool != "run" || first.Effect != string(content.EffectObserve) {
		t.Fatalf("first proposal = tool %q effect %q, want run/observe", first.Tool, first.Effect)
	}

	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": first.RunID, "attempt": first.Attempt, "tool": first.Tool,
		"callId": first.CallID, "argHash": first.ArgHash, "approved": true, "scope": "always",
	}, 3)
	if errObj != nil {
		t.Fatalf("first agent.approve: %+v", errObj)
	}
	if got.Warning != "" {
		t.Fatalf("first approval warning = %q, want none", got.Warning)
	}

	runRaw := readNotification(t, h.conn, "agent.runRequest", 15*time.Second)
	var runReq struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
		Command   string `json:"command"`
	}
	if err := json.Unmarshal(runRaw, &runReq); err != nil {
		t.Fatalf("first runRequest unmarshal: %v\nraw: %s", err, runRaw)
	}
	if runReq.SessionID != sid || runReq.Command != "df -h" {
		t.Fatalf("first runRequest = session %q command %q, want session %q command %q",
			runReq.SessionID, runReq.Command, sid, "df -h")
	}
	reply := jsonrpcCall(t, h.conn, "agent.runResolved",
		runResolvedWire(runReq.RequestID, "entry-read-half", 0, "success", 1, 0, 1, "free"))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("runResolved response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("runResolved refused: %+v", rerr.Error)
	}

	secondRaw := readNotification(t, h.conn, "agent.approvalRequested", 15*time.Second)
	var second agentApprovalRequested
	if err := json.Unmarshal(secondRaw, &second); err != nil {
		t.Fatalf("second approvalRequested unmarshal: %v\nraw: %s", err, secondRaw)
	}
	if second.Tool != "run" {
		t.Fatalf("second proposal tool = %q, want run", second.Tool)
	}
	if second.RunID != first.RunID {
		t.Fatalf("second proposal runId = %q, want the first run %q", second.RunID, first.RunID)
	}
	if second.Effect != string(content.EffectMutateDestructive) {
		t.Fatalf("second proposal effect = %q, want mutate-destructive — the READ must escalate it", second.Effect)
	}
	if !strings.Contains(second.Arguments, `"command":"rm -rf /"`) {
		t.Fatalf("second proposal arguments = %s, want the destructive run call", second.Arguments)
	}
}
