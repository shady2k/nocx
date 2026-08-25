package transport

// The readback the epic closes with (nocx-dw3.3): "the proposal, the
// decision, the attempt and the result are one readable thread in the
// ledger". Every write of the ask transaction is asserted on its own
// elsewhere (ledger_agent_test.go, the wire tests); nothing walked a
// finished exchange back out of the store. These tests do.
//
// The vocabulary is the design's (§5): the question is an entry, the run is
// the ask's own model execution (the question entry's execution row), the
// ANSWER is an entry joined by a caused-by edge, and "each tool call — an
// attempt, with its grant, its policy decision, its result and its
// termination reason". Both ways a tool is authorised, the attempt carries
// the run id so the thread joins: an escalated call's proposal records it
// in the approval block (approval.runId, and the approved call runs as
// attempt 2 of that same entry), and — since nocx-dw3.4 — a GRANTED call's
// attempt payload carries it too (runId), because where the grant permitted
// and nobody was asked, the ledger is the only account of what happened.
// The four pieces of either exchange are linked — question → run and run →
// attempt by the run id, answer → question by the caused-by edge — and this
// file reads them back in the order the exchange happened in.
//
// The thread lives in the TRANSPORT, not in a content test, on purpose:
// the rows the readback walks are written by the PRODUCT's own path — the
// engine, the policy middleware (recordProposal/openAttempt) and the
// broker, over a real socket — never by the test. A content test that
// built the rows by hand would assert on what it wrote; this asserts on
// what the product wrote.
//
// Tests never call a real model provider (the fake OpenAI server is this
// package's own) and never wait on a duration — every wait is on a
// notification or a row.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
)

// askPolicyStore seeds a store with the ask-every-time matrix: every effect
// row asks, so the run tool (mutate-destructive, session scope) escalates
// to a person at the moment it is called — the design's primary shape
// ("each one authorised at the moment it is called", §1).
func askPolicyStore(t *testing.T) *assistant.GlobalPolicyStore {
	t.Helper()
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	ask := content.EffectRow{Decision: content.DecisionAsk}
	if err := store.SetPolicy(content.EffectPolicy{
		Observe: ask, MutateReversible: ask, MutateDestructive: ask,
		PrivilegeChange: ask, Disclose: ask, CrossBoundary: ask, Delegate: ask,
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	return store
}

// authorisedRunServer is the fake provider for the authorised exchange:
// request 1 streams the run tool call the policy escalates on, and every
// later request streams the answer.
//
// It used to stream the call on requests 1 AND 2, because the resume asked
// the model to propose it again. Since nocx-igu4y it does not: the resume
// restores the proposal from the checkpoint and spends no model request at
// all, so a second scripted proposal would be a NEW call — with a new call
// id, escalating again, and the run would never finish. The engine-level
// shape is TestAsk_ApprovedResumeRunsAsSubsequentAttempt.
func authorisedRunServer(args string) (*runToolCallingServer, *httptest.Server) {
	s := &runToolCallingServer{args: args}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if s.requests.Add(1) == 1 {
			streamToolCallChunk(w, "run", s.args)
			return
		}
		streamOKChunks(w)
	}))
	return s, srv
}

// driveOneCompletedRun performs one ordinary GRANTED exchange over the real
// socket: an ask whose first model response proposes the run tool, the
// grant permits it (no person involved), the renderer (this test) answers
// the broker's request with a completed run body, the output streams as
// deltas, the model answers, and the run terminalizes completed. Returns
// the harness (for the ledger), the ask result and the streamed answer.
func driveOneCompletedRun(t *testing.T) (*askHarness, askWireResult, string) {
	t.Helper()
	// The entry id a renderer that never wrote to the ledger would answer
	// with. It names no row, which is exactly the shape the caused-by
	// relation degrades on: the join is refused by the store and the run is
	// untouched (nocx-h1l4o).
	return driveOneCompletedRunResolvingWith(t, func(*askHarness, string) string { return "entry-42" })
}

// driveOneCompletedRunResolvingWith is driveOneCompletedRun with the entry
// id the renderer resolves the run with decided by the caller — the seam a
// test needs to answer with a REAL ledger row, the way the renderer does
// (it submits through ledger.open before it answers).
func driveOneCompletedRunResolvingWith(t *testing.T, entryIDFor func(h *askHarness, sid string) string) (*askHarness, askWireResult, string) {
	t.Helper()
	fake, srv := newRunToolCallingServer("")
	t.Cleanup(srv.Close)

	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)
	fake.args = fmt.Sprintf(`{"sessionId":%q,"command":"ls -la"}`, sid)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-thread-1",
		"sessionId": sid,
		"question":  "list the files",
		"cwd":       "/repo",
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	// The renderer half: the broker's request arrives on the socket; answer
	// it with the completed run body.
	raw := readNotification(t, h.conn, "agent.runRequest", 10*time.Second)
	if raw == nil {
		t.Fatalf("no runRequest within 10s; provider requests=%d", fake.requests.Load())
	}
	var req struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("runRequest unmarshal: %v\nraw: %s", err, raw)
	}
	if req.RequestID == "" {
		t.Fatal("runRequest carries no requestId")
	}
	reply := jsonrpcCall(t, h.conn, "agent.runResolved",
		runResolvedWire(req.RequestID, entryIDFor(h, sid), 0, "success", 2, 0, 2, "file1\nfile2"))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("resolution response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("runResolved refused: %+v", rerr.Error)
	}

	// Collect the streamed answer (the tool message and the model's "ok"),
	// then the terminal state.
	var answer string
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw = readNotification(t, h.conn, "agent.runDelta", 10*time.Second)
		var d struct {
			RunID int64  `json:"runId"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		if d.RunID != res.RunID {
			t.Fatalf("runDelta runId = %d, want %d", d.RunID, res.RunID)
		}
		answer += d.Text
		if strings.Contains(answer, "ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("answer never arrived; collected %q", answer)
		}
	}
	raw = readNotification(t, h.conn, "agent.runState", 10*time.Second)
	var st struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if st.RunID != res.RunID || st.State != "completed" {
		t.Fatalf("runState = runId %d state %q, want %d completed", st.RunID, st.State, res.RunID)
	}
	return h, res, answer
}

// approvalBinding is the five-field binding the policy asked a person about:
// what the agent.approvalRequested notification carried and the approve
// echoed back. The authorised readback asserts the recorded proposal
// carries exactly this binding.
type approvalBinding struct {
	RunID   string
	Attempt int
	Tool    string
	CallID  string
	ArgHash string
}

// driveOneAuthorisedRun performs one AUTHORISED exchange over the real
// socket: the ask streams, the model proposes the run tool, the policy asks
// a person, the person approves the exact proposal over the wire, the
// approved call runs through the broker (the renderer resolves it), the
// answer streams, and the run completes. Returns the harness, the ask
// result and the streamed answer text.
func driveOneAuthorisedRun(t *testing.T) (*askHarness, askWireResult, string, approvalBinding) {
	t.Helper()
	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(askPolicyStore(t)))

	sid := openLocalSession(t, h.conn)
	fake, srv := authorisedRunServer(fmt.Sprintf(`{"sessionId":%q,"command":"ls -la"}`, sid))
	t.Cleanup(srv.Close)
	h.createEndpointAt(srv.URL)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-thread-authorised",
		"sessionId": sid,
		"question":  "list the files",
		"cwd":       "/repo",
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	// The policy asks a person: the notification carries the exact binding.
	raw := readNotification(t, h.conn, "agent.approvalRequested", 10*time.Second)
	if raw == nil {
		t.Fatalf("no approvalRequested within 10s; provider requests=%d", fake.requests.Load())
	}
	var ap approvalBinding
	if err := json.Unmarshal(raw, &ap); err != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", err, raw)
	}
	if ap.RunID != strconv.FormatInt(res.RunID, 10) || ap.Tool != "run" || ap.Attempt != 1 {
		t.Fatalf("approvalRequested binding = %+v, want run %d attempt 1 tool run", ap, res.RunID)
	}
	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": ap.RunID, "attempt": ap.Attempt, "tool": ap.Tool,
		"callId": ap.CallID, "argHash": ap.ArgHash, "approved": true, "scope": "once",
	}, 3)
	if errObj != nil {
		t.Fatalf("agent.approve: %+v", errObj)
	}
	if got.State != "streaming" {
		t.Fatalf("approve state = %q, want streaming (the resume is in flight)", got.State)
	}

	// The approved call executes through the broker: the renderer resolves
	// the run request with the completed body. Nothing reached the broker
	// before the approval — the escalation interrupted before the call.
	raw = readNotification(t, h.conn, "agent.runRequest", 10*time.Second)
	if raw == nil {
		t.Fatalf("no runRequest after the approval; provider requests=%d", fake.requests.Load())
	}
	var req struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("runRequest unmarshal: %v\nraw: %s", err, raw)
	}
	if req.RequestID == "" {
		t.Fatal("runRequest carries no requestId")
	}
	reply := jsonrpcCall(t, h.conn, "agent.runResolved", runResolvedWire(req.RequestID, "entry-42", 0, "success", 2, 0, 2, "file1\nfile2"))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("resolution response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("runResolved refused: %+v", rerr.Error)
	}

	// The answer streams; the run terminalizes completed.
	var answer string
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw = readNotification(t, h.conn, "agent.runDelta", 10*time.Second)
		var d struct {
			RunID int64  `json:"runId"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		if d.RunID != res.RunID {
			t.Fatalf("runDelta runId = %d, want %d", d.RunID, res.RunID)
		}
		answer += d.Text
		if strings.Contains(answer, "ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("answer never arrived; collected %q", answer)
		}
	}
	raw = readNotification(t, h.conn, "agent.runState", 10*time.Second)
	var st struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if st.RunID != res.RunID || st.State != "completed" {
		t.Fatalf("runState = runId %d state %q, want %d completed", st.RunID, st.State, res.RunID)
	}
	if fake.requests.Load() < 2 {
		t.Fatalf("provider received %d requests, want >= 2 — escalate, then the answer after the RESTORED call ran (the resume spends no model request; nocx-igu4y)", fake.requests.Load())
	}
	return h, res, answer, ap
}

// findRunActionEntry returns the run tool's action entry (the audit row the
// middleware opens for the tool call), or fails the test.
func findRunActionEntry(t *testing.T, led content.LedgerRepository) *content.LedgerEntry {
	summaries, err := led.ListEntries(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	for _, s := range summaries {
		if s.Kind == content.EntryAction && s.Intent == "run" {
			e, err := led.Entry(context.Background(), s.ID)
			if err != nil {
				t.Fatalf("Entry(%s): %v", s.ID, err)
			}
			return e
		}
	}
	t.Fatalf("no run tool action entry in the ledger (summaries=%d)", len(summaries))
	return nil
}

// TestRun_AuthorisedThreadReadsBackFromTheLedger is criterion 1: after one
// ordinary exchange — a tool call proposed, AUTHORISED by a person, run,
// completed — the rows read back as ONE thread. The question entry and the
// run are linked by the run id (the run is the question entry's execution),
// the tool attempt carries the run id in its recorded proposal
// (payload.approval.runId — the escalation's own record, and the approved
// call runs as attempt 2 of that same entry), the answer is linked by its
// caused-by edge, and the order the test walks is the order the exchange
// happened in.
func TestRun_AuthorisedThreadReadsBackFromTheLedger(t *testing.T) {
	h, res, answer, binding := driveOneAuthorisedRun(t)
	ctx := context.Background()
	led := h.db.Ledger()

	// ── the question entry: the proposal ────────────────────────────────
	q, err := led.Entry(ctx, res.EntryID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (nil=%v)", err, q == nil)
	}
	if q.Kind != content.EntryAsk || q.Intent != "list the files" {
		t.Errorf("question kind/intent = %q/%q, want ask/%q", q.Kind, q.Intent, "list the files")
	}
	if q.Phase != content.PhaseClosed || q.Status != content.EntrySuccess {
		t.Errorf("question phase/status = %q/%q, want closed/success — the exchange completed", q.Phase, q.Status)
	}

	// ── the run, linked by the run id ───────────────────────────────────
	if len(q.Executions) != 1 {
		t.Fatalf("question executions = %d, want exactly one run", len(q.Executions))
	}
	run := q.Executions[0]
	if run.ID != res.RunID {
		t.Errorf("run id = %d, want %d — the run id links the thread to the question", run.ID, res.RunID)
	}
	if run.Lane == nil || *run.Lane != "agent" || run.Attempt != 1 {
		t.Errorf("run lane/attempt = %v/%d, want agent/1", run.Lane, run.Attempt)
	}
	if run.State == nil || *run.State != content.RunCompleted {
		t.Errorf("run state = %v, want completed", run.State)
	}
	if run.TerminationReason == nil || *run.TerminationReason != content.TermCompleted {
		t.Errorf("run termination = %v, want completed", run.TerminationReason)
	}
	if run.StartedAt == nil || run.EndedAt == nil || *run.StartedAt > *run.EndedAt {
		t.Errorf("run span = %v..%v, want started <= ended", run.StartedAt, run.EndedAt)
	}

	// ── the tool attempt, linked by the run id it carries ───────────────
	action := findRunActionEntry(t, led)
	if action.Status != content.EntrySuccess {
		t.Errorf("action entry status = %q, want success", action.Status)
	}
	var proposal struct {
		Approval struct {
			RunID   string `json:"runId"`
			Attempt int    `json:"attempt"`
			Tool    string `json:"tool"`
			CallID  string `json:"callId"`
			ArgHash string `json:"argHash"`
		} `json:"approval"`
	}
	if umErr := json.Unmarshal([]byte(action.Payload), &proposal); umErr != nil {
		t.Fatalf("action payload = %q, want the proposal JSON: %v", action.Payload, umErr)
	}
	// The approval block carries the exact binding the person was asked
	// about and approved — run id, attempt, tool, call id, argument hash.
	if proposal.Approval.RunID != binding.RunID || proposal.Approval.RunID != strconv.FormatInt(res.RunID, 10) {
		t.Errorf("attempt payload run id = %q, want %q (%d) — the tool attempt is linked to the run by the run id",
			proposal.Approval.RunID, binding.RunID, res.RunID)
	}
	if proposal.Approval.Attempt != binding.Attempt || proposal.Approval.Tool != binding.Tool ||
		proposal.Approval.CallID != binding.CallID || proposal.Approval.ArgHash != binding.ArgHash {
		t.Errorf("attempt approval block = %+v, want the binding the person approved %+v",
			proposal.Approval, binding)
	}
	if len(action.Executions) != 2 {
		t.Fatalf("attempt executions = %d, want exactly two — the escalation and the authorised call", len(action.Executions))
	}
	var escalation, authorised *content.Execution
	for i := range action.Executions {
		ex := &action.Executions[i]
		if ex.Executor == nil || *ex.Executor != "agent" {
			t.Errorf("attempt %d executor = %v, want agent", ex.Attempt, ex.Executor)
		}
		if ex.Grant == nil || ex.Grant.Version != 1 {
			t.Errorf("attempt %d grant = %+v, want the version-1 grant minted at the ask", ex.Attempt, ex.Grant)
		}
		switch ex.Attempt {
		case 1:
			escalation = ex
		case 2:
			authorised = ex
		}
	}
	if escalation == nil || escalation.TerminationReason == nil || *escalation.TerminationReason != content.TermInterrupted {
		t.Fatalf("attempt 1 = %+v, want the interrupted escalation — the ask is in the thread", escalation)
	}
	if authorised == nil || authorised.TerminationReason == nil || *authorised.TerminationReason != content.TermCompleted {
		t.Fatalf("attempt 2 = %+v, want the completed authorised call", authorised)
	}
	if escalation.StartedAt == nil || escalation.EndedAt == nil || authorised.StartedAt == nil || authorised.EndedAt == nil {
		t.Fatalf("attempt spans = %v..%v and %v..%v, want both ends",
			escalation.StartedAt, escalation.EndedAt, authorised.StartedAt, authorised.EndedAt)
	}
	if *escalation.EndedAt > *authorised.StartedAt {
		t.Errorf("escalation ended %d after the authorised call started %d — the ask closed before the call ran",
			*escalation.EndedAt, *authorised.StartedAt)
	}

	// ── the answer, which is the TURN'S OWN BODY (nocx-4em1z) ───────────
	// Not a second entry joined by an edge: the question is the entry's
	// intent and the answer is what it printed, exactly as a command's
	// output is. So no `agent` entry claims to be this turn's answer, and no
	// edge leaves the turn at all — every caused-by edge here points AT it,
	// from the things it caused (nocx-h1l4o).
	assertNothingClaimsToBeTheAnswer(t, led, res.EntryID)
	// And the escalation the turn made IS one of those things: a proposal
	// put to a person is an entry of the turn's flow, so a restored turn
	// shows the question it asked where it asked it.
	assertCausedByTheTurn(t, led, res.EntryID, action.ID, 0)

	ans, err := led.Entry(ctx, res.EntryID)
	if err != nil || ans == nil {
		t.Fatalf("turn entry: %v (nil=%v)", err, ans == nil)
	}
	if ans.Kind != content.EntryAsk {
		t.Errorf("turn kind = %q, want ask", ans.Kind)
	}
	if ans.Phase != content.PhaseClosed || ans.Status != content.EntrySuccess {
		t.Errorf("turn phase/status = %q/%q, want closed/success", ans.Phase, ans.Status)
	}
	if len(ans.Executions) != 1 {
		t.Fatalf("turn executions = %d, want exactly 1", len(ans.Executions))
	}
	// The turn opens NO body of its own (ADR-0040): the answer is its `text`
	// children, one per run of prose, and the close sealed every one of them.
	if len(ans.Executions[0].Artifacts) != 0 {
		t.Fatalf("turn artifacts = %d, want none — the answer is its prose children",
			len(ans.Executions[0].Artifacts))
	}
	assertProseSealed(t, led, res.EntryID)
	body := proseBodyOf(t, led, res.EntryID)
	if body == "" {
		t.Error("the turn's prose is empty, want the streamed answer")
	}
	if body != answer {
		t.Errorf("answer body = %q, want %q — the durable answer is exactly what streamed", body, answer)
	}
	// The DURABLE end of nocx-bshm2: the stored answer is the model's prose
	// and NOT the tool's return value. This assertion used to read the other
	// way round — it required "file1", the run tool's output, to be IN the
	// answer — because that is what the code did: the engine's loop emitted
	// every message with content and a tool result is a message, so the raw
	// return travelled the delta path and was persisted with the answer.
	// A test written from the implementation cannot report that; this one
	// now states the contract instead.
	if !strings.Contains(body, "ok") {
		t.Errorf("answer body = %q, want the model's final answer", body)
	}
	if strings.Contains(body, "file1") {
		t.Errorf("answer body = %q — the tool's output was persisted as though the model had said it", body)
	}

	// ── the order they happened in ──────────────────────────────────────
	// Commit order (ADR-0019 §2: ingest_seq is the writer's counter): the
	// turn committed in the ask transaction, the proposal when the model
	// first called the tool — after it. There is no third seq to compare:
	// the answer is the turn's body, not a row of its own (nocx-4em1z).
	if q.ID != ans.ID {
		t.Errorf("the question %q and the answer %q are not the same entry", q.ID, ans.ID)
	}
	if q.IngestSeq >= action.IngestSeq {
		t.Errorf("commit order = turn %d, action %d, want the turn first", q.IngestSeq, action.IngestSeq)
	}
	// The run's span bounds the attempts': the run existed from the ask
	// (before the tool was proposed) and closed after the authorised call
	// completed (the answer streamed between the two).
	if *run.StartedAt > *escalation.StartedAt {
		t.Errorf("run started %d after the proposal started %d — the run must exist before the tool is proposed",
			*run.StartedAt, *escalation.StartedAt)
	}
	if *authorised.EndedAt > *run.EndedAt {
		t.Errorf("authorised call ended %d after the run ended %d — the tool must complete before the run closes",
			*authorised.EndedAt, *run.EndedAt)
	}
}

// TestRun_GrantedPathThreadReadsBackFromTheLedger is the granted end of
// criterion 1 (nocx-dw3.4): in the GRANTED exchange (the autonomous preset
// — the grant permits the call, nobody is asked), the four rows read back
// as ONE thread exactly as they do in the authorised one — question and run
// linked by the run id, the tool attempt carrying the run id in its own
// payload, the answer linked by its caused-by edge. Where nobody was asked,
// the ledger is the ONLY account of what happened, so the attempt must join
// its run; this test is the inverted form of the gap that used to exist
// (the audit row carrying no run id), so the gap cannot come back
// unnoticed.
func TestRun_GrantedPathThreadReadsBackFromTheLedger(t *testing.T) {
	h, res, answer := driveOneCompletedRun(t)
	ctx := context.Background()
	led := h.db.Ledger()

	// ── the question entry ──────────────────────────────────────────────
	q, err := led.Entry(ctx, res.EntryID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (nil=%v)", err, q == nil)
	}
	if q.Kind != content.EntryAsk || q.Intent != "list the files" {
		t.Errorf("question kind/intent = %q/%q, want ask/%q", q.Kind, q.Intent, "list the files")
	}
	if q.Phase != content.PhaseClosed || q.Status != content.EntrySuccess {
		t.Errorf("question phase/status = %q/%q, want closed/success", q.Phase, q.Status)
	}

	// ── the run, linked by the run id ───────────────────────────────────
	if len(q.Executions) != 1 {
		t.Fatalf("question executions = %d, want exactly one run", len(q.Executions))
	}
	run := q.Executions[0]
	if run.ID != res.RunID {
		t.Errorf("run id = %d, want %d — the run id links the thread to the question", run.ID, res.RunID)
	}
	if run.State == nil || *run.State != content.RunCompleted {
		t.Errorf("run state = %v, want completed", run.State)
	}
	if run.TerminationReason == nil || *run.TerminationReason != content.TermCompleted {
		t.Errorf("run termination = %v, want completed", run.TerminationReason)
	}
	if run.StartedAt == nil || run.EndedAt == nil || *run.StartedAt > *run.EndedAt {
		t.Errorf("run span = %v..%v, want started <= ended", run.StartedAt, run.EndedAt)
	}

	// ── the tool attempt, linked by the run id it carries ───────────────
	action := findRunActionEntry(t, led)
	if action.Status != content.EntrySuccess {
		t.Errorf("action entry status = %q, want success", action.Status)
	}
	if len(action.Executions) != 1 {
		t.Fatalf("attempt executions = %d, want exactly one (no escalation in the granted path)", len(action.Executions))
	}
	attempt := action.Executions[0]
	if attempt.Attempt != 1 || attempt.Executor == nil || *attempt.Executor != "agent" {
		t.Errorf("attempt/executor = %d/%v, want 1/agent", attempt.Attempt, attempt.Executor)
	}
	if attempt.Grant == nil || attempt.Grant.Version != 1 {
		t.Errorf("recorded grant = %+v, want the version-1 grant minted at the ask", attempt.Grant)
	}
	if attempt.TerminationReason == nil || *attempt.TerminationReason != content.TermCompleted {
		t.Errorf("attempt termination = %v, want completed", attempt.TerminationReason)
	}
	if attempt.StartedAt == nil || attempt.EndedAt == nil {
		t.Fatalf("attempt span = %v..%v, want both ends", attempt.StartedAt, attempt.EndedAt)
	}
	var payload struct {
		RunID string `json:"runId"`
	}
	if umErr := json.Unmarshal([]byte(action.Payload), &payload); umErr != nil {
		t.Fatalf("action payload = %q: %v", action.Payload, umErr)
	}
	if payload.RunID != strconv.FormatInt(res.RunID, 10) {
		t.Errorf("attempt payload run id = %q, want %d — a granted call's attempt must carry its run",
			payload.RunID, res.RunID)
	}

	// ── the answer is the turn's own body, and the CALL joins the turn ──
	// nocx-4em1z put the answer in the turn's own body, so nothing claims
	// to be it. nocx-h1l4o joins what the turn CAUSED: the tool call the
	// granted path made is an action entry with no pane, no session and no
	// conversation, and this edge is the only thing that says which turn it
	// belongs to.
	assertNothingClaimsToBeTheAnswer(t, led, res.EntryID)
	assertCausedByTheTurn(t, led, res.EntryID, action.ID, 0)
	ans, err := led.Entry(ctx, res.EntryID)
	if err != nil || ans == nil {
		t.Fatalf("turn entry: %v (nil=%v)", err, ans == nil)
	}
	if ans.Kind != content.EntryAsk {
		t.Errorf("turn kind = %q, want ask", ans.Kind)
	}
	if ans.Phase != content.PhaseClosed || ans.Status != content.EntrySuccess {
		t.Errorf("turn phase/status = %q/%q, want closed/success", ans.Phase, ans.Status)
	}
	if len(ans.Executions) != 1 {
		t.Fatalf("turn executions = %d, want exactly 1", len(ans.Executions))
	}
	if len(ans.Executions[0].Artifacts) != 0 {
		t.Fatalf("turn artifacts = %d, want none — the answer is its prose children (ADR-0040)",
			len(ans.Executions[0].Artifacts))
	}
	assertProseSealed(t, led, res.EntryID)
	if body := proseBodyOf(t, led, res.EntryID); body != answer {
		t.Errorf("answer body = %q, want %q — the durable answer is exactly what streamed", body, answer)
	}

	// ── the order they happened in ──────────────────────────────────────
	// The turn commits before the action it caused. There is no third seq
	// to compare: the answer is the turn's body, not a row of its own
	// (nocx-4em1z), so q and ans ARE the same entry.
	if q.ID != ans.ID {
		t.Errorf("the question %q and the answer %q are not the same entry", q.ID, ans.ID)
	}
	if q.IngestSeq >= action.IngestSeq {
		t.Errorf("commit order = turn %d, action %d, want the turn first",
			q.IngestSeq, action.IngestSeq)
	}
	if *run.StartedAt > *attempt.StartedAt || *attempt.EndedAt > *run.EndedAt {
		t.Errorf("attempt span %v..%v outside the run span %v..%v — the tool ran inside the run's lifetime",
			*attempt.StartedAt, *attempt.EndedAt, *run.StartedAt, *run.EndedAt)
	}
}

// TestRun_RefusedExchangeReadsBackFromTheLedger is criterion 2's end: the
// model proposes the run tool on a session the grant does NOT cover; the
// policy refuses BEFORE anything is submitted — the broker is never asked,
// no tool attempt is ever recorded — and the run continues: the refusal is
// that call's result, the model answers, and the turn completes
// (nocx-uvac6.1). The decision is in the thread, not only in a log: the
// question closes success, the answer is prose, and the ledger holds no
// action row — the refusal preceded every submission.
func TestRun_RefusedExchangeReadsBackFromTheLedger(t *testing.T) {
	fake, srv := newRunToolCallingServer(`{"sessionId":"foreign-session","command":"rm -rf /"}`)
	t.Cleanup(srv.Close)

	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-thread-refused",
		"sessionId": sid,
		"question":  "clean up",
		"cwd":       "/repo",
	}, 3)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	// Drain notifications until the run terminalizes, watching for any
	// agent.runRequest: the refusal must precede every submission.
	var st struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("run never terminalized; state=%+v", st)
		}
		_ = h.conn.SetReadDeadline(time.Now().Add(remaining))
		_, msg, readErr := h.conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("reading notifications: %v (state=%+v)", readErr, st)
		}
		var n struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if umErr := json.Unmarshal(msg, &n); umErr != nil {
			continue
		}
		if n.ID == nil && n.Method == "agent.runRequest" {
			t.Fatalf("a run request reached the renderer for a lane the grant does not cover: %s", n.Params)
		}
		if n.ID == nil && n.Method == "agent.runState" {
			if stErr := json.Unmarshal(n.Params, &st); stErr != nil {
				t.Fatalf("runState unmarshal: %v\nraw: %s", stErr, n.Params)
			}
			break
		}
	}
	if st.RunID != res.RunID || st.State != "completed" {
		t.Fatalf("runState = runId %d state %q, want %d completed — a refused call is an answer, not a fault", st.RunID, st.State, res.RunID)
	}
	if st.Error != "" {
		t.Fatalf("runState error = %q, want none on a completed run", st.Error)
	}
	// The refusal rode the second request as a tool result — the run went
	// on and the model answered.
	if fake.requests.Load() != 2 {
		t.Fatalf("provider received %d requests, want 2 (the refused call, then the answer) — the run must continue", fake.requests.Load())
	}
	if len(fake.bodies) < 2 || !strings.Contains(fake.bodies[1], "REFUSED") {
		t.Fatalf("the second request did not carry the refusal as a tool result: %v", fake.bodies)
	}

	// ── the thread readback: the refusal is in the ledger ───────────────
	ctx := context.Background()
	led := h.db.Ledger()

	q, err := led.Entry(ctx, res.EntryID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (nil=%v)", err, q == nil)
	}
	if q.Phase != content.PhaseClosed || q.Status != content.EntrySuccess {
		t.Errorf("question phase/status = %q/%q, want closed/success — the turn answered after the refusal", q.Phase, q.Status)
	}
	if len(q.Executions) != 1 {
		t.Fatalf("question executions = %d, want one run", len(q.Executions))
	}
	run := q.Executions[0]
	if run.ID != res.RunID {
		t.Errorf("run id = %d, want %d", run.ID, res.RunID)
	}
	if run.State == nil || *run.State != content.RunCompleted {
		t.Errorf("run state = %v, want completed", run.State)
	}
	if run.TerminationReason == nil || *run.TerminationReason != content.TermCompleted {
		t.Errorf("run termination = %v, want completed", run.TerminationReason)
	}
	if run.EndedAt == nil {
		t.Error("completed run has no ended_at — a terminal run has an end")
	}

	ans, err := led.Entry(ctx, res.EntryID)
	if err != nil || ans == nil {
		t.Fatalf("turn entry: %v (nil=%v)", err, ans == nil)
	}
	if ans.ParentID != nil {
		t.Errorf("the turn is drawn inside %q — the answer is its own body (nocx-4em1z)", *ans.ParentID)
	}
	if ans.Phase != content.PhaseClosed || ans.Status != content.EntrySuccess {
		t.Errorf("turn phase/status = %q/%q, want closed/success — the turn closes with the run", ans.Phase, ans.Status)
	}
	if len(ans.Executions) != 1 {
		t.Fatalf("turn executions = %d, want exactly 1", len(ans.Executions))
	}
	// The answer streamed: the model's reply after the refusal is prose in
	// the thread (ADR-0040), exactly what the brief's "with prose in it"
	// means.
	if prose := proseUnder(t, led, res.EntryID); len(prose) == 0 {
		t.Error("the answered turn has no prose block — the model's words after the refusal must be in the thread")
	}

	// The refusal precedes every submission: the ledger holds the TURN and
	// the prose block the answer streamed into — and NOTHING ELSE: no tool
	// attempt was ever opened.
	summaries, err := led.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("ledger has %d entries, want 2 (the turn and its prose child) — a refused call opens no action entry", len(summaries))
	}
	for _, s := range summaries {
		if s.Kind == content.EntryAction {
			t.Errorf("refused exchange recorded an action entry: %+v — the refusal precedes every submission", s)
		}
	}
}

// ── the caused-by relation, read back from the store (nocx-h1l4o) ────────

// assertNothingClaimsToBeTheAnswer is nocx-4em1z's invariant in the form the
// tree left it: the answer is the turn's own body, so no entry is drawn as
// the turn's answer. The turn itself sits inside nothing, and nothing inside
// it is an `agent` entry.
func assertNothingClaimsToBeTheAnswer(t *testing.T, led content.LedgerRepository, turnID string) {
	t.Helper()
	turn, err := led.Entry(context.Background(), turnID)
	if err != nil || turn == nil {
		t.Fatalf("turn entry: %v (nil=%v)", err, turn == nil)
	}
	if turn.ParentID != nil {
		t.Errorf("the turn is drawn inside %q — the answer is its own body", *turn.ParentID)
	}
	caused, err := led.Caused(context.Background(), turnID)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	for _, c := range caused {
		if c.Kind == content.EntryAsk {
			t.Errorf("caused %+v: an ask entry is a turn, never a turn's answer", c)
		}
	}
}

// assertCausedByTheTurn reads the relation back out of the store: the entry
// is drawn inside the turn at the position given, and the position is stored
// on the row rather than inferred from the order rows came out.
func assertCausedByTheTurn(t *testing.T, led content.LedgerRepository, turnID, causedID string, position int) {
	t.Helper()
	caused, err := led.Caused(context.Background(), turnID)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	for _, c := range caused {
		if c.EntryID != causedID {
			continue
		}
		if c.Position != position {
			t.Errorf("%s is caused by the turn at position %d, want %d", causedID, c.Position, position)
		}
		return
	}
	t.Errorf("%s is joined to no turn — the relation is what says which turn it belongs to; the turn caused %+v",
		causedID, caused)
}

// ── criterion 1, over the real socket: the command joins its turn ─────────

// A command an assistant turn ran carries a stored caused-by to that turn
// AND a position within it (nocx-h1l4o).
//
// The renderer here answers with an id the STORE really carries — the row is
// made with ledger.open, which is the cheapest way to have one over this
// socket. That id vocabulary is the whole of it: the real renderer's row is
// written by history.record at the completion and it answers with the id
// that ack named (nocx-9sqii). Either way the join is made by the BACKEND
// from that id and its own turn; the resolution carries no arrangement of
// its own, which is criterion 6 and the same rule ledger.open states for
// paneId.
//
// WHAT THIS TEST CANNOT SEE, and what let the defect through: it chooses the
// id it answers with, so it cannot report a renderer answering with an id
// from some other vocabulary. The real one answered with its OWN record
// number — a per-tab block counter — and the caused-by edge was refused by
// the foreign key on every real run, in a log line nobody read. The check
// that reports that is on the renderer's side of the wire
// (terminal-content.test.ts, "resolves with the completed run body"), and
// the one that watches a person see it is e2e/agent-restore.spec.ts.
func TestRun_TheCommandTheTurnRanJoinsTheTurnWithAPosition(t *testing.T) {
	const commandEntry = "01924f9c-0000-7000-8000-0000000000c1"
	h, res, _ := driveOneCompletedRunResolvingWith(t, func(h *askHarness, sid string) string {
		openEntry(t, h.conn, sid, commandEntry, "ls -la", 900)
		return commandEntry
	})
	ctx := context.Background()
	led := h.db.Ledger()

	caused, err := led.Caused(ctx, res.EntryID)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	// Three things, in the order the turn did them: the tool call's own
	// action entry — the line that says WHEN the assistant reached for run —
	// then the command that really opened a block, and then the run of prose
	// the model wrote from its output (ADR-0040). The prose is a CHILD like
	// the other two, at a seat of its own, which is the whole of what the
	// anchor used to be needed for.
	if got := childKinds(t, led, res.EntryID); len(got) != 3 ||
		got[0] != content.EntryAction || got[1] != content.EntryShell || got[2] != content.EntryText {
		t.Fatalf("the turn's children read %v, want the call, the command it opened, and the answer written after it", got)
	}
	if caused[0].Kind != content.EntryAction || caused[0].Intent != "run" || caused[0].Position != 0 {
		t.Errorf("first cause = %+v, want the run action at position 0", caused[0])
	}
	if caused[1].EntryID != commandEntry || caused[1].Kind != content.EntryShell || caused[1].Position != 1 {
		t.Errorf("second cause = %+v, want the shell entry at position 1", caused[1])
	}
	// The prose is seated AFTER the command it was written from, and nothing
	// was opened before the call: the model reached for the tool first, so
	// seat 0 is the call and not an empty paragraph.
	if caused[2].Kind != content.EntryText || caused[2].Position != 2 || caused[2].Intent != "" {
		t.Errorf("third cause = %+v, want the run of prose at position 2, naming no intent", caused[2])
	}
	// The command is a command, not a tool call: it has no effect class and
	// names no resource, and the restore draws it as the block it is.
	if caused[1].Effect != "" || caused[1].Resource != nil {
		t.Errorf("the command came back with tool facts: %+v", caused[1])
	}
	// And the position is STORED, not inferred from the order rows came out.
	assertCausedByTheTurn(t, led, res.EntryID, commandEntry, 1)
	assertNothingClaimsToBeTheAnswer(t, led, res.EntryID)
}

// Criterion 4's first case, end to end: a resolution naming an entry the
// ledger does not carry joins nothing, and the run is untouched. This is the
// state a renderer that could not submit leaves behind, and the honest
// answer is plain ledger order — never a guessed parent.
func TestRun_AResolutionNamingNoRowJoinsNothingAndDoesNotFailTheRun(t *testing.T) {
	h, res, answer := driveOneCompletedRun(t)
	if answer == "" {
		t.Fatal("the run produced no answer — a relation that could not be written must not fail the run")
	}
	caused, err := h.db.Ledger().Caused(context.Background(), res.EntryID)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	// The call the turn made is joined; the command that names no row is
	// not, and nothing was invented in its place. Beside it sits the run of
	// prose the model wrote after the call — a child of the turn like any
	// other since ADR-0040, and the reason this list is two long rather than
	// one.
	if len(caused) != 2 || caused[0].Intent != "run" || caused[0].Kind != content.EntryAction {
		t.Fatalf("the turn caused %+v, want the call it made and the prose it wrote after it", caused)
	}
	if caused[1].Kind != content.EntryText || caused[1].Position != 1 {
		t.Fatalf("the second child = %+v, want the run of prose at seat 1", caused[1])
	}
	// And nothing shell-shaped is there: the resolution named no row, so no
	// command was seated and none was invented.
	for _, c := range caused {
		if c.Kind == content.EntryShell {
			t.Fatalf("a command was seated for a resolution that named no row: %+v", c)
		}
	}
}
