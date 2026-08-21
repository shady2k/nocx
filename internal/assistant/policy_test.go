package assistant

// The pipeline's acceptance tests (nocx-lndv): refusal terminalizes, the
// batch latch stops later calls, the attempt precedes the call, the
// escalation suspends before the domain is touched and binds to the exact
// proposal, and files.read returns a window and the file's contents. The
// engine-level tests drive the REAL eino agent against the fake OpenAI
// server and assert on what the engine sent (criterion 1's "not a spy over
// next"); the middleware-level tests drive the wrapped endpoint directly
// where the engine seam cannot reach (the resume with changed arguments).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// ── helpers ───────────────────────────────────────────────────────────────

// fakeLedger is the attempt seam with a scriptable failure and a call log —
// the ledger is a party to the contract, and criterion 4 needs exactly the
// StartExecution write to fail.
type fakeLedger struct {
	mu          sync.Mutex
	log         []string
	failStart   bool
	failSubmit  bool
	nextExec    int64
	submissions []fakeSubmission
}

// fakeSubmission is one Submit the ledger recorded: the intent and the
// kind payload as submitted. The attempts' payloads carry the classifier
// block (bead nocx-kpy23 — "why was this asked" is answerable from the
// ledger), so the classifier tests read them back through this capture.
type fakeSubmission struct {
	intent  string
	payload string
}

func (f *fakeLedger) EnsureEnvironment(context.Context, content.Environment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "ensure-env")
	return nil
}

func (f *fakeLedger) RecordObservation(context.Context, content.Observation) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "observe")
	return 1, nil
}

func (f *fakeLedger) Submit(_ context.Context, in content.SubmitEntry) (content.SubmitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "submit:"+in.Intent)
	if f.failSubmit {
		return content.SubmitResult{}, errors.New("fake ledger: submit failed")
	}
	f.submissions = append(f.submissions, fakeSubmission{intent: in.Intent, payload: in.Payload})
	return content.SubmitResult{ID: "entry-" + in.Intent}, nil
}

func (f *fakeLedger) StartExecution(_ context.Context, in content.StartExecution) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "start:"+in.EntryID)
	if f.failStart {
		return 0, errors.New("fake ledger: start execution failed")
	}
	f.nextExec++
	return f.nextExec, nil
}

func (f *fakeLedger) FinishExecution(_ context.Context, _ int64, end content.FinishExecution) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "finish:"+string(end.Status))
	return nil
}

func (f *fakeLedger) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeLedger) started() int {
	n := 0
	for _, c := range f.calls() {
		if strings.HasPrefix(c, "start:") {
			n++
		}
	}
	return n
}

// fakeKnownMaterial is the egress vault-comparison seam with a scripted
// answer: the gate's tests need "this text contains vault material" (and,
// by default, the honest "nothing known") without standing up a vault. The
// spans it returns are into the text the gate screens — the seam's contract
// — and the values never cross the seam, here or in production.
type fakeKnownMaterial struct {
	matches []KnownMatch
}

func (f *fakeKnownMaterial) FindKnown(context.Context, string) ([]KnownMatch, error) {
	return f.matches, nil
}

// toolCallSpec is one tool call in a model response.
type toolCallSpec struct {
	name string
	args string
	id   string
}

// streamToolCalls writes one SSE completion whose single chunk carries the
// given tool calls and finish_reason tool_calls — the shape the openai
// adapter's streaming builder consumes.
func streamToolCalls(w http.ResponseWriter, calls ...toolCallSpec) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	tcs := make([]map[string]any, 0, len(calls))
	for i, c := range calls {
		id := c.id
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		tcs = append(tcs, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      c.name,
				"arguments": c.args,
			},
		})
	}
	d := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "probe-model",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "tool_calls": tcs},
			"finish_reason": "tool_calls",
		}},
	}
	b, _ := json.Marshal(d)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// callThenAnswer answers the FIRST completion with the tool calls and every
// later one with the ordinary streamed "ok" — the shape of a run that
// executes its tools and then finishes.
func callThenAnswer(calls ...toolCallSpec) func(w http.ResponseWriter, r *http.Request) {
	var n int
	return func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			streamToolCalls(w, calls...)
			return
		}
		streamOK(w)
	}
}

// testDirGrant mints a grant scoped to a fresh temp dir the way the
// transport does — the matrix AsGrant, with the dir as the base scope of
// every row — so enforcement (row scopes) and declaration (derived
// effects/union) see the form a minted grant really has.
func testDirGrant(t *testing.T, policy content.EffectPolicy) (content.Grant, string) {
	t.Helper()
	dir := t.TempDir()
	return policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}}), dir
}

func askParams(baseURL string, grant *content.Grant, ledger AttemptLedger, approvals *ApprovalStore) AskParams {
	p := testAskParams(baseURL)
	p.Grant = grant
	p.AttemptLedger = ledger
	p.Approvals = approvals
	p.KnownMaterial = &fakeKnownMaterial{}
	p.RunID = "run-1"
	p.Attempt = 1
	return p
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// middlewareFor builds the pipeline for one test grant + registry. The
// egress vault-comparison seam is a no-match fake by default — tests that
// need a known-material finding pass their own through
// middlewareForWithKnown.
func middlewareFor(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore) *policyMiddleware {
	return middlewareForWithKnown(t, grant, ledger, approvals, nil, &fakeKnownMaterial{})
}

// middlewareForWithRequester builds the pipeline for one test grant +
// registry with an explicit renderer-request seam (the readScreen tests).
func middlewareForWithRequester(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, requester RendererRequester) *policyMiddleware {
	return middlewareForWithKnown(t, grant, ledger, approvals, requester, &fakeKnownMaterial{})
}

// middlewareForWithKnown is the construction seam the egress tests use: the
// pipeline with an explicit vault-comparison answer.
func middlewareForWithKnown(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, requester RendererRequester, known KnownMaterial) *policyMiddleware {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	mw, err := newPolicyMiddleware(grant, reg, ledger, approvals, known, "run-1", 1, requester, nil)
	if err != nil {
		t.Fatalf("newPolicyMiddleware: %v", err)
	}
	return mw
}

// wrappedEndpoint drives one tool call through the middleware's pipeline.
func wrappedEndpoint(mw *policyMiddleware, name, callID, args string) (string, error) {
	wrapped, err := mw.WrapInvokableToolCall(context.Background(), nil, &adk.ToolContext{Name: name, CallID: callID})
	if err != nil {
		return "", err
	}
	return wrapped(context.Background(), args)
}

// ── criterion 1: a refusal terminalizes ──────────────────────────────────

// TestAsk_RefusalTerminalizes is acceptance criterion 1: a call outside the
// grant is REFUSED — the run fails with ErrPolicyRefused, no tool message is
// produced and no second model request is made. Asserted on what the engine
// sent (the fake server received exactly one request and the run's error is
// the refusal), not on a spy over next.
func TestAsk_RefusalTerminalizes(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "in scope")

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: `{"path":"/etc/passwd"}`}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, nil), func(string) error { return nil })
	if err == nil {
		t.Fatal("Ask succeeded — the out-of-grant call was not refused")
	}
	if !errors.Is(err, ErrPolicyRefused) {
		t.Fatalf("Ask error = %v, want ErrPolicyRefused", err)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests after the refusal, want exactly 1 — a refusal must terminalize", n)
	}
	// Nothing ran: no attempt was opened (refusal precedes the attempt
	// write) and no tool result was produced.
	if s := ledger.started(); s != 0 {
		t.Fatalf("ledger opened %d executions after a refusal, want 0", s)
	}
}

// ── criterion 2: the batch latch ─────────────────────────────────────────

// TestAsk_RefusalOnSecondCallPreventsThird is acceptance criterion 2: three
// calls in one model response — the second refuses, so the third must not
// run. sequentialRunToolCall runs every task and never inspects
// tasks[i].err; the batch latch is what stops the third. Asserted by what
// the ledger records: exactly one execution (the first call's), never a
// second or third.
func TestAsk_RefusalOnSecondCallPreventsThird(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first")

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))},
		toolCallSpec{name: "files.read", args: `{"path":"/etc/passwd"}`}, // refuses
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))},
	))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, nil), func(string) error { return nil })
	if err == nil {
		t.Fatal("Ask succeeded — the refusal did not terminalize the run")
	}
	if !errors.Is(err, ErrPolicyRefused) {
		t.Fatalf("Ask error = %v, want ErrPolicyRefused", err)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests, want exactly 1", n)
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want exactly 1 (the first call) — the refused second call must stop the third", s)
	}
}

// TestAsk_EscalationOnSecondCallPreventsThird is acceptance criterion 2's
// escalation half: three calls in one model response under ask-every-time —
// the first escalates, so the second and third must not run, and the batch
// must SUSPEND cleanly (the latched calls return an interrupt, not a plain
// error, or the run would fail instead of awaiting approval). Asserted by
// what the ledger records: exactly one execution — the first call's recorded
// proposal attempt, closed interrupted, never run.
func TestAsk_EscalationOnSecondCallPreventsThird(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first")
	writeFile(t, filepath.Join(dir, "b.txt"), "third")

	ledger := &fakeLedger{}
	_, srv := newFakeOpenAI(callThenAnswer(
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))},
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))},
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))},
	))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, nil), func(string) error { return nil })
	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want exactly 1 — the first call's recorded proposal; the latched calls must not run", s)
	}
}

// ── criterion 4: a failed attempt write stops the call ───────────────────

// TestAsk_FailedAttemptWritePreventsExecution is acceptance criterion 4: the
// attempt is written BEFORE the call, and a ledger whose write fails means
// the tool is not called — no capability is constructed, next is not called,
// and the run fails with a terminal infrastructure error. Asserted by the
// ledger's own log: the failed start is the last write, and nothing was
// finished (no execution ever ran).
func TestAsk_FailedAttemptWritePreventsExecution(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "would have been read")

	ledger := &fakeLedger{failStart: true}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, nil), func(string) error { return nil })
	if err == nil {
		t.Fatal("Ask succeeded — a failed attempt write was ignored")
	}
	if !strings.Contains(err.Error(), "record attempt") {
		t.Fatalf("Ask error = %v, want the terminal attempt-write failure", err)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests after the failed write, want exactly 1", n)
	}
	calls := ledger.calls()
	if len(calls) == 0 || calls[len(calls)-1] != "start:entry-files.read" {
		t.Fatalf("ledger log = %v — the attempt write must be the last write; the tool must not have run", calls)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "finish:") {
			t.Fatalf("ledger log = %v — an execution was finished, so the tool ran despite the failed attempt write", calls)
		}
	}
}

// ── criterion 5: escalation suspends, and binds to the exact proposal ────

// TestAsk_EscalationSuspendsBeforeDomain is acceptance criterion 5's first
// half: under ask-every-time the run SUSPENDS before the domain is touched —
// Ask returns the approval request (not a failure, and not a tool result),
// and no second model request is made. The escalation IS recorded (nocx-5dldy
// — the proposal is a ledger fact): exactly one attempt, closed interrupted,
// the call that is asking has NOT run.
func TestAsk_EscalationSuspendsBeforeDomain(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "must not be read yet")

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(string) error { return nil })
	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}
	if want.Request == nil || want.Request.Tool != "files.read" || !strings.Contains(want.Request.Arguments, "a.txt") {
		t.Fatalf("approval request = %+v, want files.read on a.txt", want.Request)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests after the suspension, want exactly 1", n)
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want exactly 1 — the recorded proposal attempt; the call that is asking has not run", s)
	}
	for _, c := range ledger.calls() {
		if strings.HasPrefix(c, "finish:") && !strings.Contains(c, "interrupted") {
			t.Fatalf("ledger log = %v — the escalation's attempt must close interrupted, never completed: the tool did not run", ledger.calls())
		}
	}
}

// TestMiddleware_ApprovalBindsToExactArguments is acceptance criterion 5's
// second half, at the seam the engine cannot reach: after an escalation,
// approving the EXACT proposal lets the call run; the same call with a
// CHANGED argument does not resume under the old approval — it escalates
// again, and the tool does not run.
func TestMiddleware_ApprovalBindsToExactArguments(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "approved read")
	writeFile(t, filepath.Join(dir, "b.txt"), "changed read")

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, ledger, approvals)

	argsA := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	argsB := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))

	// First call: escalates before the domain is touched, and the
	// escalation is RECORDED — one proposal attempt, closed interrupted.
	if _, err := wrappedEndpoint(mw, "files.read", "call_1", argsA); err == nil {
		t.Fatal("the ask-every-time call was not escalated")
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions after the escalation, want 1 — the recorded proposal attempt", s)
	}

	// Approve the exact proposal, then the same call runs — as a
	// SUBSEQUENT attempt of the proposal's own entry (ADR-0020 decision 4):
	// the escalation's record plus the approved call's execution.
	approvals.Approve(Approval{
		RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1",
		ArgHash: canonicalArgHash(argsA),
	})
	out, err := wrappedEndpoint(mw, "files.read", "call_1", argsA)
	if err != nil {
		t.Fatalf("the approved call was refused: %v", err)
	}
	if !strings.Contains(out, "approved read") {
		t.Fatalf("approved call output = %s, want the file's contents", out)
	}
	if s := ledger.started(); s != 2 {
		t.Fatalf("ledger opened %d executions, want 2 — the proposal record plus the approved call's own execution", s)
	}

	// A CHANGED argument does not resume under the old approval: it
	// escalates again (recording a NEW proposal), and the tool does not run.
	if _, err := wrappedEndpoint(mw, "files.read", "call_1", argsB); err == nil {
		t.Fatal("the changed-argument call ran under the old approval")
	}
	if s := ledger.started(); s != 3 {
		t.Fatalf("ledger opened %d executions, want 3 — the changed call recorded a NEW proposal and nothing ran under the old approval", s)
	}
}

// ── criteria 6 and 7: the window, and the happy path ─────────────────────

// TestAsk_PermittedReadReturnsFileContents is criterion 7, the paired
// positive end of the interval: a permitted files.read under a grant naming
// its path returns the file's contents — asserted on the second request the
// engine actually sent, whose tool message carries the window.
func TestAsk_PermittedReadReturnsFileContents(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "the file's contents")

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	var answer string
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, nil), func(d string) error {
		answer += d
		return nil
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(answer, "ok") {
		t.Fatalf("answer = %q, want the model's reply", answer)
	}
	if n := f.requests.Load(); n != 2 {
		t.Fatalf("the engine made %d model requests, want 2 (call + answer)", n)
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want exactly 1", s)
	}
	// The tool result rode the second request as a tool message — the wire
	// is a party to the contract, so assert on what was sent.
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(f.body()), &req); err != nil {
		t.Fatalf("request 2 body: %v", err)
	}
	found := false
	for _, m := range req.Messages {
		if m["role"] == "tool" {
			found = true
			content, _ := m["content"].(string)
			if !strings.Contains(content, "the file's contents") {
				t.Fatalf("tool message content = %s, want the file's contents in the window", content)
			}
		}
	}
	if !found {
		t.Fatal("request 2 carried no tool message — the tool result never reached the model")
	}
}

// TestExecuteFilesRead_WindowIsHonest is criterion 6: files.read returns a
// window — total, the window, and which window was actually returned — and a
// file shorter than the window (a window past the end) is answered honestly,
// never as an error.
func TestExecuteFilesRead_WindowIsHonest(t *testing.T) {
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	dir := t.TempDir()
	short := filepath.Join(dir, "short.txt")
	writeFile(t, short, "ten bytes!")

	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
	decl, ok := reg.Lookup("files.read")
	if !ok {
		t.Fatal("files.read not in the registry")
	}
	narrowed, err := decl.Narrow(grant)
	if err != nil {
		t.Fatalf("Narrow: %v", err)
	}
	out, err := executeFilesRead(context.Background(), narrowed, json.RawMessage(fmt.Sprintf(`{"path":%q}`, short)))
	if err != nil {
		t.Fatalf("executeFilesRead: %v", err)
	}
	var res filesReadResult
	if parseErr := json.Unmarshal([]byte(out), &res); parseErr != nil {
		t.Fatalf("result %s: %v", out, parseErr)
	}
	if res.Total != int64(len("ten bytes!")) {
		t.Fatalf("total = %d, want %d", res.Total, len("ten bytes!"))
	}
	if res.Returned != int64(len("ten bytes!")) || res.Window.Start != 0 || res.Window.End != int64(len("ten bytes!")) {
		t.Fatalf("window = %+v returned %d, want the whole short file [0,%d)", res.Window, res.Returned, len("ten bytes!"))
	}
	if !strings.Contains(res.Text, "ten bytes!") {
		t.Fatalf("text = %q, want the file's contents", res.Text)
	}

	// An EMPTY file is a window past the end answered honestly: total 0,
	// a zero-length window, no error.
	empty := filepath.Join(dir, "empty.txt")
	writeFile(t, empty, "")
	out, err = executeFilesRead(context.Background(), narrowed, json.RawMessage(fmt.Sprintf(`{"path":%q}`, empty)))
	if err != nil {
		t.Fatalf("executeFilesRead on an empty file: %v — a window past the end is answered honestly, not as an error", err)
	}
	res = filesReadResult{}
	if parseErr := json.Unmarshal([]byte(out), &res); parseErr != nil {
		t.Fatalf("result %s: %v", out, parseErr)
	}
	if res.Total != 0 || res.Returned != 0 || res.Window.Start != 0 || res.Window.End != 0 {
		t.Fatalf("empty file window = %+v returned %d total %d, want an honest zero window", res.Window, res.Returned, res.Total)
	}
}

// ── malformed model output ───────────────────────────────────────────────

// TestMiddleware_UnknownToolAndBadArgumentsAreMalformed is design §6.1-6.2:
// a name absent from the registry and arguments that do not match the schema
// are malformed model output — terminal, and NOT a refusal (there is nothing
// to call) and NOT an ask.
func TestMiddleware_UnknownToolAndBadArgumentsAreMalformed(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	mw := middlewareFor(t, grant, &fakeLedger{}, nil)

	if _, err := wrappedEndpoint(mw, "no.such.tool", "c1", `{}`); !errors.Is(err, ErrMalformedModelOutput) {
		t.Fatalf("unknown tool error = %v, want ErrMalformedModelOutput", err)
	}
	// An extra property is refused by additionalProperties: false.
	if _, err := wrappedEndpoint(mw, "files.read", "c2", `{"path":"/x","extra":1}`); !errors.Is(err, ErrMalformedModelOutput) {
		t.Fatalf("bad-args error = %v, want ErrMalformedModelOutput", err)
	}
	// Missing the required path.
	if _, err := wrappedEndpoint(mw, "files.read", "c3", `{}`); !errors.Is(err, ErrMalformedModelOutput) {
		t.Fatalf("missing-path error = %v, want ErrMalformedModelOutput", err)
	}
	// The ingress size bound.
	huge := `{"path":"` + strings.Repeat("a", maxArgsBytes) + `"}`
	if _, err := wrappedEndpoint(mw, "files.read", "c4", huge); !errors.Is(err, ErrMalformedModelOutput) {
		t.Fatalf("oversize error = %v, want ErrMalformedModelOutput", err)
	}
}

// ── the executor table stays honest ──────────────────────────────────────

// TestExecutorsCoverTheRegistry is the exhaustiveness test of §5's "the
// table grows by addition plus tests": every tool with a Narrow must be
// EXECUTABLE — an InGo tool needs an entry in the executors table, an
// InRenderer tool is executed by the middleware's renderer-request branch
// (design §6.6 — the step differs by exactly one field of the declaration).
// A Narrowed tool with neither is a registration that cannot run and must
// not assemble silently.
func TestExecutorsCoverTheRegistry(t *testing.T) {
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for _, tl := range reg.All() {
		if tl.Narrow == nil {
			continue // declared-but-not-executable: the middleware refuses honestly
		}
		switch tl.Executes {
		case agenttools.InGo:
			if _, ok := executors[tl.Name]; !ok {
				t.Fatalf("tool %q executes in Go but has no executor entry", tl.Name)
			}
		case agenttools.InRenderer:
			// The middleware's executeInRenderer branch is the executor; a
			// Narrowed InRenderer tool without the branch is a compile-time
			// impossibility, asserted by the branch's own type switch.
		default:
			t.Fatalf("tool %q has an unknown execution site %q", tl.Name, tl.Executes)
		}
	}
}

// ── criterion 8: the schemas reach the binary ────────────────────────────

// TestNewClient_AssemblesFromTheEmbedOutsideTheRepo is criterion 8: the
// schemas are embedded, so an assembly whose working directory is NOT the
// repo still gets the real set — the cwd-relative os.DirFS("contracts/tools")
// of the old build assembled the quiet empty set outside the repo.
func TestNewClient_AssemblesFromTheEmbedOutsideTheRepo(t *testing.T) {
	t.Chdir(t.TempDir()) // the embed is compiled into the binary: cwd must not matter

	cl, err := NewClient(nil)
	if err != nil {
		t.Fatalf("NewClient outside the repo: %v", err)
	}
	internal, ok := cl.(*client)
	if !ok {
		t.Fatalf("NewClient returned %T, want *client", cl)
	}
	if got := len(internal.tools.All()); got != 4 {
		t.Fatalf("registry has %d tools, want the real set of 4", got)
	}
	names := make([]string, 0, len(internal.tools.All()))
	for _, tl := range internal.tools.All() {
		names = append(names, tl.Name)
	}
	if names[0] != "files.read" || names[1] != "readScreen" || names[2] != "run" || names[3] != "git.status" {
		t.Fatalf("assembled tools = %v, want [files.read readScreen run git.status]", names)
	}
}

// ── the full pipeline against the real ledger ────────────────────────────

// realLedger opens a real content store in a temp dir — the honest ledger
// for the tests that exercise the full pipeline.
func realLedger(t *testing.T) content.LedgerRepository {
	t.Helper()
	db, err := content.Open(context.Background(), content.Config{
		Path: filepath.Join(t.TempDir(), "content.db"),
		Key:  bytes.Repeat([]byte{7}, 32),
		Budget: content.Budget{
			RetentionBytes:   1 << 20,
			DiskCeilingBytes: 4 << 20,
			CompactionFloor:  0.5,
		},
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.Ledger()
}

// TestAsk_PermittedReadRecordsTheAttempt is the full-pipeline happy path
// with the REAL ledger: the attempt (nocx-m4r3m's StartExecution) gains its
// first live caller here, and the action entry plus the recorded grant are
// what "what was this allowed to do" queries (ADR-0020 decision 5).
func TestAsk_PermittedReadRecordsTheAttempt(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "recorded")

	ledger := realLedger(t)
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, ledger, nil)
	p.RunID = "run-1"
	p.Attempt = 1
	if err := cl.Ask(context.Background(), p, func(string) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	// The audit holds one action entry for the read, with the grant.
	entries, err := ledger.ListEntries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ledger has %d entries, want exactly 1 action", len(entries))
	}
	if entries[0].Kind != content.EntryAction || entries[0].Intent != "files.read" {
		t.Fatalf("entry = %+v, want kind=action intent=files.read", entries[0])
	}
	entry, err := ledger.Entry(context.Background(), entries[0].ID)
	if err != nil || entry == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, entry == nil)
	}
	if len(entry.Executions) != 1 {
		t.Fatalf("entry has %d executions, want 1", len(entry.Executions))
	}
	ex := entry.Executions[0]
	if ex.Grant == nil || ex.Grant.Version != 1 {
		t.Fatalf("recorded grant = %+v, want a version-1 minted grant", ex.Grant)
	}
	if ex.Grant.Policy.DecisionFor(content.EffectObserve) != content.DecisionPermit {
		t.Fatalf("recorded grant policy = %+v, want the autonomous matrix (observe permitted) recorded on the attempt", ex.Grant.Policy)
	}
	if len(ex.Grant.Scopes) != 1 || ex.Grant.Scopes[0].Kind != content.ResourcePath {
		t.Fatalf("recorded grant scopes = %+v, want the dir's path scope", ex.Grant.Scopes)
	}

	// The attempt carries the run id it happened in (nocx-dw3.4): a granted
	// call's audit row joins its run's thread exactly as an escalated call's
	// approval block does — where the grant permitted and nobody was asked,
	// the ledger is the only account of what happened.
	var payload struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal([]byte(entry.Payload), &payload); err != nil {
		t.Fatalf("attempt payload: %v", err)
	}
	if payload.RunID != "run-1" {
		t.Fatalf("attempt payload runId = %q, want run-1 — the granted attempt must carry its run", payload.RunID)
	}
	if ex.TerminationReason == nil || *ex.TerminationReason != content.TermCompleted {
		t.Fatalf("termination = %v, want completed — the outcome must be recorded on the attempt", ex.TerminationReason)
	}
}

// ── the ask names its effect and its resource ─────────────────────────────

// TestAsk_PolicyEscalationCarriesTheEffectAndTheResource is Task 3's first
// criterion: the question a person is asked names the effect class the gate
// decided on and the resource it matched the call against. The effect is
// what a standing answer writes a row for, so it must come from the gate —
// a renderer deriving it from the tool name would be a rule keyed by a tool
// name, which ADR-0028 decision 4 forbids.
func TestAsk_PolicyEscalationCarriesTheEffectAndTheResource(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "must not be read yet")

	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, path)}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore()), func(string) error { return nil })
	var want *ApprovalRequestedError
	if !errors.As(err, &want) || want.Request == nil {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}
	if want.Request.Effect != content.EffectObserve {
		t.Fatalf("effect = %q, want %q — files.read's declared class, decided by the gate", want.Request.Effect, content.EffectObserve)
	}
	if want.Request.Resource == nil {
		t.Fatal("resource is nil, want the path the call named")
	}
	if want.Request.Resource.Kind != content.ResourcePath || want.Request.Resource.ID != path {
		t.Fatalf("resource = %+v, want {path %s}", want.Request.Resource, path)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests after the suspension, want exactly 1", n)
	}
}

// TestAsk_EscalationWithoutAResourceArgCarriesNoResource is the null half:
// a tool that names no resource in its parameters (git.status's repository
// IS the grant's path scope) escalates with an effect and NO resource. Null
// is a fact, not a gap — and the wire says so.
func TestAsk_EscalationWithoutAResourceArgCarriesNoResource(t *testing.T) {
	grant, _ := testDirGrant(t, askEveryTimeMatrix())

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "git.status", args: `{}`}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore()), func(string) error { return nil })
	var want *ApprovalRequestedError
	if !errors.As(err, &want) || want.Request == nil {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}
	if want.Request.Effect != content.EffectObserve {
		t.Fatalf("effect = %q, want %q", want.Request.Effect, content.EffectObserve)
	}
	if want.Request.Resource != nil {
		t.Fatalf("resource = %+v, want nil — git.status names no resource in its parameters", want.Request.Resource)
	}
}

// TestAsk_EgressSuspensionCarriesTheEffectAndTheResource keeps the wire ONE
// shape: the egress ask fills the same two fields from the same declaration,
// even though the surface offers only allow/deny once there. Two shapes on
// one notification is how a required field becomes absent on one path.
func TestAsk_EgressSuspensionCarriesTheEffectAndTheResource(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const secret = "known-secret-value-123"
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "deploy key: "+secret)

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, path)}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWith(srv.URL, &grant, &fakeLedger{}, nil, &knownMatcher{value: secret, name: "github-token"}, nil),
		func(string) error { return nil })
	var want *EgressRequestedError
	if !errors.As(err, &want) || want.Request == nil {
		t.Fatalf("Ask error = %v, want the egress suspension", err)
	}
	if want.Request.Effect != content.EffectObserve {
		t.Fatalf("effect = %q, want %q", want.Request.Effect, content.EffectObserve)
	}
	if want.Request.Resource == nil || want.Request.Resource.ID != path {
		t.Fatalf("resource = %+v, want {path %s}", want.Request.Resource, path)
	}
}
