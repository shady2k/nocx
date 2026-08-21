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
// requests 1 and 2 stream the SAME run tool call (the escalation, then the
// checkpoint-resumed call), later requests stream the answer — the shape
// TestAsk_ApprovedResumeRunsAsSubsequentAttempt proves at the engine level.
func authorisedRunServer(args string) (*runToolCallingServer, *httptest.Server) {
	s := &runToolCallingServer{args: args}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if s.requests.Add(1) <= 2 {
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
	if fake.requests.Load() < 3 {
		t.Fatalf("provider received %d requests, want >= 3 — escalate, resume-call, answer", fake.requests.Load())
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
	q, err := led.Entry(ctx, res.QuestionID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (nil=%v)", err, q == nil)
	}
	if q.Kind != content.EntryAgent || q.Intent != "list the files" {
		t.Errorf("question kind/intent = %q/%q, want agent/%q", q.Kind, q.Intent, "list the files")
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

	// ── the answer, joined by its caused-by edge ────────────────────────
	edges, err := led.Edges(ctx, res.QuestionID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	var caused *content.Edge
	for i := range edges {
		if edges[i].Rel == content.RelCausedBy {
			caused = &edges[i]
		}
	}
	if caused == nil {
		t.Fatalf("edges = %+v, want a caused-by edge from the answer", edges)
	}
	if caused.From != res.AnswerEntryID || caused.To != res.QuestionID {
		t.Errorf("caused-by edge = %+v, want answer %q → question %q", caused, res.AnswerEntryID, res.QuestionID)
	}

	ans, err := led.Entry(ctx, res.AnswerEntryID)
	if err != nil || ans == nil {
		t.Fatalf("answer entry: %v (nil=%v)", err, ans == nil)
	}
	if ans.Kind != content.EntryAgent || ans.Intent != content.AnswerIntent {
		t.Errorf("answer kind/intent = %q/%q, want agent/answer", ans.Kind, ans.Intent)
	}
	if ans.Phase != content.PhaseClosed || ans.Status != content.EntrySuccess {
		t.Errorf("answer phase/status = %q/%q, want closed/success", ans.Phase, ans.Status)
	}
	if len(ans.Executions) != 1 {
		t.Fatalf("answer executions = %d, want exactly 1", len(ans.Executions))
	}
	if len(ans.Executions[0].Artifacts) != 1 {
		t.Fatalf("answer artifacts = %d, want exactly 1", len(ans.Executions[0].Artifacts))
	}
	a := ans.Executions[0].Artifacts[0]
	if a.State != content.ArtifactSealed {
		t.Errorf("answer artifact state = %q, want sealed — the close sealed the answer with the run", a.State)
	}
	if a.ByteLen == 0 {
		t.Error("answer artifact byte_len = 0, want the streamed answer")
	}
	art, err := led.Artifact(ctx, a.ID)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	body := ""
	for _, c := range art.Chunks {
		body += string(c)
	}
	if body != answer {
		t.Errorf("answer artifact body = %q, want %q — the durable answer is exactly what streamed", body, answer)
	}
	if art.ByteLen != int64(len(body)) {
		t.Errorf("artifact byte_len = %d, want %d", art.ByteLen, len(body))
	}
	if !strings.Contains(body, "file1") || !strings.Contains(body, "ok") {
		t.Errorf("answer body = %q, want the tool output and the final answer", body)
	}

	// ── the order they happened in ──────────────────────────────────────
	// Commit order (ADR-0019 §2: ingest_seq is the writer's counter): the
	// question and its answer entry committed in the ask transaction, the
	// proposal when the model first called the tool — after both.
	if q.IngestSeq >= ans.IngestSeq || ans.IngestSeq >= action.IngestSeq {
		t.Errorf("commit order = question %d, answer %d, action %d, want strictly increasing",
			q.IngestSeq, ans.IngestSeq, action.IngestSeq)
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
	q, err := led.Entry(ctx, res.QuestionID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (nil=%v)", err, q == nil)
	}
	if q.Kind != content.EntryAgent || q.Intent != "list the files" {
		t.Errorf("question kind/intent = %q/%q, want agent/%q", q.Kind, q.Intent, "list the files")
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

	// ── the answer, joined by its caused-by edge ────────────────────────
	edges, err := led.Edges(ctx, res.QuestionID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	var caused *content.Edge
	for i := range edges {
		if edges[i].Rel == content.RelCausedBy {
			caused = &edges[i]
		}
	}
	if caused == nil {
		t.Fatalf("edges = %+v, want a caused-by edge from the answer", edges)
	}
	if caused.From != res.AnswerEntryID || caused.To != res.QuestionID {
		t.Errorf("caused-by edge = %+v, want answer %q → question %q", caused, res.AnswerEntryID, res.QuestionID)
	}
	ans, err := led.Entry(ctx, res.AnswerEntryID)
	if err != nil || ans == nil {
		t.Fatalf("answer entry: %v (nil=%v)", err, ans == nil)
	}
	if ans.Kind != content.EntryAgent || ans.Intent != content.AnswerIntent {
		t.Errorf("answer kind/intent = %q/%q, want agent/answer", ans.Kind, ans.Intent)
	}
	if ans.Phase != content.PhaseClosed || ans.Status != content.EntrySuccess {
		t.Errorf("answer phase/status = %q/%q, want closed/success", ans.Phase, ans.Status)
	}
	if len(ans.Executions) != 1 {
		t.Fatalf("answer executions = %d, want exactly 1", len(ans.Executions))
	}
	if len(ans.Executions[0].Artifacts) != 1 {
		t.Fatalf("answer artifacts = %d, want exactly 1", len(ans.Executions[0].Artifacts))
	}
	a := ans.Executions[0].Artifacts[0]
	if a.State != content.ArtifactSealed {
		t.Errorf("answer artifact state = %q, want sealed", a.State)
	}
	art, err := led.Artifact(ctx, a.ID)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	body := ""
	for _, c := range art.Chunks {
		body += string(c)
	}
	if body != answer {
		t.Errorf("answer artifact body = %q, want %q — the durable answer is exactly what streamed", body, answer)
	}

	// ── the order they happened in ──────────────────────────────────────
	if q.IngestSeq >= ans.IngestSeq || ans.IngestSeq >= action.IngestSeq {
		t.Errorf("commit order = question %d, answer %d, action %d, want strictly increasing",
			q.IngestSeq, ans.IngestSeq, action.IngestSeq)
	}
	if *run.StartedAt > *attempt.StartedAt || *attempt.EndedAt > *run.EndedAt {
		t.Errorf("attempt span %v..%v outside the run span %v..%v — the tool ran inside the run's lifetime",
			*attempt.StartedAt, *attempt.EndedAt, *run.StartedAt, *run.EndedAt)
	}
}

// TestRun_RefusedExchangeReadsBackFromTheLedger is criterion 2's end: the
// model proposes the run tool on a session the grant does NOT cover; the
// policy refuses BEFORE anything is submitted — the broker is never asked,
// no tool attempt is ever recorded — and the run terminalizes failed with
// the refusal sentence. The decision is in the thread, not only in a log:
// the question closes failure, the run carries the refusal sentence, the
// answer entry closes with it, and the ledger holds no action row.
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
	if st.RunID != res.RunID || st.State != "failed" {
		t.Fatalf("runState = runId %d state %q, want %d failed", st.RunID, st.State, res.RunID)
	}
	if !strings.Contains(st.Error, "refused") {
		t.Fatalf("runState error = %q, want the refusal sentence", st.Error)
	}
	if fake.requests.Load() != 1 {
		t.Fatalf("provider received %d requests, want exactly 1 (a refused call never re-asks the model)", fake.requests.Load())
	}

	// ── the thread readback: the refusal is in the ledger ───────────────
	ctx := context.Background()
	led := h.db.Ledger()

	q, err := led.Entry(ctx, res.QuestionID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (nil=%v)", err, q == nil)
	}
	if q.Phase != content.PhaseClosed || q.Status != content.EntryFailure {
		t.Errorf("question phase/status = %q/%q, want closed/failure — the refusal is terminal", q.Phase, q.Status)
	}
	if len(q.Executions) != 1 {
		t.Fatalf("question executions = %d, want one run", len(q.Executions))
	}
	run := q.Executions[0]
	if run.ID != res.RunID {
		t.Errorf("run id = %d, want %d", run.ID, res.RunID)
	}
	if run.State == nil || *run.State != content.RunFailed {
		t.Errorf("run state = %v, want failed", run.State)
	}
	if run.TerminationReason == nil || *run.TerminationReason != content.TermFailed {
		t.Errorf("run termination = %v, want failed", run.TerminationReason)
	}
	if run.EndedAt == nil {
		t.Error("failed run has no ended_at — a terminal run has an end")
	}
	if !strings.Contains(run.Payload, "refused") {
		t.Errorf("run payload = %q, want the refusal sentence — the decision is in the thread, not only in a log", run.Payload)
	}

	edges, err := led.Edges(ctx, res.QuestionID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	var caused bool
	for _, e := range edges {
		if e.Rel == content.RelCausedBy && e.From == res.AnswerEntryID && e.To == res.QuestionID {
			caused = true
		}
	}
	if !caused {
		t.Errorf("edges = %+v, want the caused-by edge from the answer", edges)
	}

	ans, err := led.Entry(ctx, res.AnswerEntryID)
	if err != nil || ans == nil {
		t.Fatalf("answer entry: %v (nil=%v)", err, ans == nil)
	}
	if ans.Phase != content.PhaseClosed || ans.Status != content.EntryFailure {
		t.Errorf("answer phase/status = %q/%q, want closed/failure — the answer closes with the run", ans.Phase, ans.Status)
	}
	if len(ans.Executions) != 1 {
		t.Fatalf("answer executions = %d, want exactly 1", len(ans.Executions))
	}
	if len(ans.Executions[0].Artifacts) != 1 {
		t.Fatalf("answer artifacts = %d, want exactly 1", len(ans.Executions[0].Artifacts))
	}
	a := ans.Executions[0].Artifacts[0]
	if a.State != content.ArtifactSealed {
		t.Errorf("answer artifact state = %q, want sealed", a.State)
	}
	if a.ByteLen != 0 {
		t.Errorf("answer artifact byte_len = %d, want 0 — nothing streamed", a.ByteLen)
	}

	// The refusal precedes every submission: the ledger holds the question
	// and the answer and NOTHING ELSE — no tool attempt was ever opened.
	summaries, err := led.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("ledger has %d entries, want exactly 2 (question + answer)", len(summaries))
	}
	for _, s := range summaries {
		if s.Kind == content.EntryAction {
			t.Errorf("refused exchange recorded an action entry: %+v — the refusal precedes every submission", s)
		}
	}
}
