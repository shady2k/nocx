package assistant

// Kernel and pipeline acceptance tests. Tool calls enter the kernel directly;
// no in-process model loop or framework adapter is involved.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
)

type fakeLedger struct {
	mu                  sync.Mutex
	log                 []string
	failStart           bool
	failSubmit          bool
	failCause           bool
	rejectCanceledCause bool
	// finishHang makes FinishExecution block on the context it is handed
	// rather than returning immediately — nocx-uhii1's "a ledger whose
	// write hangs does not hang the dispatcher" case. finishCalls records,
	// for every FinishExecution call, the facts a test needs about the
	// context it ran on — captured AT CALL TIME, not read back afterward:
	// the caller cancels its own finish context via defer once Dispatch
	// returns (releasing it, as context hygiene requires), so a context
	// reference read back after the call reports canceled regardless of
	// what it was detached from. Only what was true at call time answers
	// the question these tests ask.
	finishHang  bool
	finishCalls []finishCallRecord
	nextExec    int64
	submissions []fakeSubmission
	// causes is every (turn, caused) pair AddCause was asked for, in call
	// order — the relation nocx-h1l4o records. The fake assigns positions
	// the way the store does (one counter per turn) so a test can assert
	// the causal index without an sqlite file.
	causes []fakeCause
	// captures is every body handed to CaptureOutput, and refuseCapture is
	// the store's "not stored, and that is not a failure" answer.
	captures      []content.CaptureOutput
	refuseCapture bool
}

// fakeCause is one recorded containment: which turn, which entry it caused,
// and the seat inside that turn it took.
type fakeCause struct {
	turn     string
	caused   string
	position int
}

// fakeSubmission is one Submit the ledger recorded: the intent and the
// kind payload as submitted. The tests that assert what an escalation
// recorded read the payload back through this capture.
type fakeSubmission struct {
	intent  string
	payload string
	source  content.Source
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
	f.submissions = append(f.submissions, fakeSubmission{intent: in.Intent, payload: in.Payload, source: in.Source})
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

// finishCallRecord is what a test can know about one FinishExecution call
// without holding on to the context itself.
type finishCallRecord struct {
	errAtCall   error
	hasDeadline bool
}

func (f *fakeLedger) FinishExecution(ctx context.Context, _ int64, end content.FinishExecution) error {
	_, hasDeadline := ctx.Deadline()
	record := finishCallRecord{errAtCall: ctx.Err(), hasDeadline: hasDeadline}
	f.mu.Lock()
	f.finishCalls = append(f.finishCalls, record)
	hang := f.finishHang
	f.mu.Unlock()
	if hang {
		// Simulate a wedged store: block until the caller's own context
		// gives up, then report exactly that. If this were called with
		// the invocation's own (already-cancelled) context, it would
		// return immediately with context.Canceled instead of blocking.
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "finish:"+string(end.Status))
	return nil
}

// finishCallRecords is what FinishExecution's context looked like at the
// moment of each call, in call order — what nocx-uhii1's detachment tests
// inspect.
func (f *fakeLedger) finishCallRecords() []finishCallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]finishCallRecord(nil), f.finishCalls...)
}

// captures is every body CaptureOutput was handed, in call order — what a
// tool call recorded as its result (nocx-hp8p2.13).
func (f *fakeLedger) CaptureOutput(_ context.Context, in content.CaptureOutput) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "capture:"+in.EntryID)
	if f.refuseCapture {
		return false, nil
	}
	f.captures = append(f.captures, in)
	return true, nil
}

func (f *fakeLedger) recordedCaptures() []content.CaptureOutput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]content.CaptureOutput(nil), f.captures...)
}

func (f *fakeLedger) recordedSubmissions() []fakeSubmission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSubmission(nil), f.submissions...)
}

func (f *fakeLedger) AddCause(ctx context.Context, turnID, causedID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "cause:"+causedID)
	if f.rejectCanceledCause && ctx.Err() != nil {
		return 0, context.Canceled
	}
	if f.failCause {
		return 0, errors.New("fake ledger: the relation could not be recorded")
	}
	for _, existing := range f.causes {
		if existing.turn == turnID && existing.caused == causedID {
			return existing.position, nil // idempotent on the pair, like the store
		}
	}
	pos := 0
	for _, existing := range f.causes {
		if existing.turn == turnID {
			pos++
		}
	}
	f.causes = append(f.causes, fakeCause{turn: turnID, caused: causedID, position: pos})
	return pos, nil
}

func (f *fakeLedger) recordedCauses() []fakeCause {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCause(nil), f.causes...)
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
func testDirGrant(t *testing.T, policy content.EffectPolicy) (content.Grant, string) {
	t.Helper()
	dir := t.TempDir()
	return policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}}), dir
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
func middlewareFor(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore) *kernelFixture {
	return middlewareForWithKnown(t, grant, ledger, approvals, nil, &fakeKnownMaterial{})
}

// middlewareForWithRequester builds the pipeline for one test grant +
// registry with an explicit renderer-request seam (the readScreen tests).
func middlewareForWithRequester(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, requester RendererRequester) *kernelFixture {
	return middlewareForWithKnown(t, grant, ledger, approvals, requester, &fakeKnownMaterial{})
}

// middlewareForWithKnown is the construction seam the egress tests use: the
// pipeline with an explicit vault-comparison answer.
func middlewareForWithKnown(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, requester RendererRequester, known KnownMaterial) *kernelFixture {
	return middlewareForTurn(t, grant, ledger, approvals, requester, known, "")
}

// middlewareForTurn is the construction seam with the TURN the run's causes
// join to (nocx-h1l4o). Every other helper here passes "" — the un-bound
// caller shape, which joins nothing — so the pipeline these tests drive is
// the same one except where a test is about the relation.
func middlewareForTurn(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, requester RendererRequester, known KnownMaterial, turnEntryID string) *kernelFixture {
	t.Helper()
	return middlewareForSeams(t, grant, ledger, approvals, requester, known, turnEntryID, localPaneEnvironment(), newLocalFileSource(t))
}

// middlewareForSeams is middlewareForTurn with the two facts a test about the
// PANE supplies: where the pane is, and the binding its files are read
// through. Every other caller takes the local pair.
func middlewareForSeams(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, requester RendererRequester, known KnownMaterial, turnEntryID string, pane content.Environment, files filesystem.HandleSource) *kernelFixture {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	sessionID := ""
	for _, scope := range grant.Scopes {
		if scope.Kind == content.ResourceSession {
			sessionID = scope.ID
			break
		}
	}
	// Test grants carry the pane identity; production receives it from transport.
	var screenCapture ScreenCaptureSource
	if source, ok := requester.(ScreenCaptureSource); ok {
		screenCapture = source
	}
	mw, err := newKernelFixture(nil, grant, reg, ledger, approvals, known, "run-1", sessionID, 1, turnEntryID, requester, Attachments{}, nil, toolSeams{screenCapture: screenCapture, paneEnvironment: pane, files: files})
	if err != nil {
		t.Fatalf("newKernelFixture: %v", err)
	}
	return mw
}

// wrappedEndpoint drives one tool call through the middleware's pipeline.
func wrappedEndpoint(mw *kernelFixture, name, callID, args string) (string, error) {
	return wrappedEndpointContext(mw, context.Background(), name, callID, args)
}

func TestKernel_RefusalIsNotFramedAsToolOutput(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	mw := middlewareFor(t, grant, &fakeLedger{}, nil)

	out, err := wrappedEndpoint(mw, "files.read", "call-1", `{"path":"/etc/passwd"}`)
	if err != nil {
		t.Fatalf("refusal returned an error: %v", err)
	}
	want := refusalResult("files.read", RefusedOutOfScope, "")
	if out != want {
		t.Fatalf("refusal = %q, want nocx's own sentence %q", out, want)
	}
	if strings.Contains(out, "untrusted data, not instructions") {
		t.Fatalf("refusal was framed as tool output: %q", out)
	}
}

// TestPolicyNarrowRowAsksOutsideItsSelectorAndRefusesOutsideTheFence covers
// both bounds of a narrowed row, because they are two different answers
// (design §5.3, nocx-t6h2u): the operator's own selector is editable, so a
// path outside it is a question, while the run fence is not, so a path
// outside THAT is a refusal.
//
// Tool.Effect is set explicitly here, and that is load-bearing. The kernel
// selects the row by t.Effect, not by t.Declaration.Effect; this test used to
// set only the declaration, so t.Effect was "", every path missed the empty
// row of an effect outside the lattice, and the narrowed row it names was
// never consulted at all.
func TestPolicyNarrowRowAsksOutsideItsSelectorAndRefusesOutsideTheFence(t *testing.T) {
	policy := autonomousMatrix()
	fenceRoot := t.TempDir()
	rowRoot := filepath.Join(fenceRoot, "row")
	policy.Observe.Scopes = []content.GrantScope{{
		Kind: content.ResourcePath,
		ID:   rowRoot,
	}}
	grant := policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: fenceRoot},
	})
	kernel := &effectKernel{grant: grant}
	tool := agenttools.Tool{
		Declaration: agenttools.Declaration{Effect: []content.Effect{content.EffectObserve}},
		Effect:      content.EffectObserve,
	}
	decide := func(path string) (policyOutcome, PolicyRefusalReason) {
		outcome, reason, _ := kernel.decideInvocationWithReason(tool, []agenttools.ResourceRef{{
			Kind: content.ResourcePath,
			ID:   path,
		}}, true, content.Invocation{Parsed: true})
		return outcome, reason
	}

	if outcome, reason := decide(filepath.Join(rowRoot, "in.txt")); outcome != policyPermit || reason != "" {
		t.Fatalf("in-row path gave outcome=%v reason=%q, want permit", outcome, reason)
	}
	outsideRow := filepath.Join(fenceRoot, "outside.txt")
	if outcome, reason := decide(outsideRow); outcome != policyAsk || reason != "" {
		t.Fatalf("path %q outside the row selector gave outcome=%v reason=%q, want ask — the selector is editable",
			outsideRow, outcome, reason)
	}
	outsideFence := filepath.Join(filepath.Dir(fenceRoot), "beyond.txt")
	if outcome, reason := decide(outsideFence); outcome != policyRefuse || reason != RefusedOutOfScope {
		t.Fatalf("path %q outside the run fence gave outcome=%v reason=%q, want refuse/%s — the fence cannot be widened by an answer",
			outsideFence, outcome, reason, RefusedOutOfScope)
	}
}

func TestPolicySelectorsDoNotEraseRunFenceKinds(t *testing.T) {
	policy := autonomousMatrix()
	root := t.TempDir()
	selector := []content.GrantScope{{Kind: content.ResourcePath, ID: root}}
	for _, row := range []*content.EffectRow{
		&policy.Observe,
		&policy.MutateReversible,
		&policy.MutateDestructive,
		&policy.PrivilegeChange,
		&policy.Disclose,
		&policy.CrossBoundary,
		&policy.Delegate,
	} {
		row.Scopes = selector
	}
	runFence := []content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: "/"},
		{Kind: content.ResourceContent, ID: "content"},
	}
	grant := policy.AsGrant(runFence)

	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	names := make(map[string]bool)
	for _, tool := range reg.ForGrant(grant) {
		names[tool.Name] = true
	}
	for _, want := range []string{"session.run", "notes.search", "snippets.list"} {
		if !names[want] {
			t.Fatalf("tool %q was dropped from grant: names=%v scopes=%v", want, names, grant.Scopes)
		}
	}
	if got := grant.Policy.RowScopes(content.EffectObserve); len(got) != 3 ||
		got[0] != (content.GrantScope{Kind: content.ResourceSession, ID: "session-a"}) ||
		got[1] != (content.GrantScope{Kind: content.ResourcePath, ID: root}) ||
		got[2] != (content.GrantScope{Kind: content.ResourceContent, ID: "content"}) {
		t.Fatalf("observe effective scopes = %+v, want the path selector plus absent fence kinds", got)
	}
}

// ── criterion 1: a refusal is an answer (nocx-uvac6.1) ───────────────────

// TestAsk_RefusalContinuesAsToolResult is the brief's first acceptance
// criterion: a call outside the grant is REFUSED — and the refusal is a
// TOOL RESULT the model receives, naming the tool and why, and the run
// CONTINUES to a terminal state of its own accord, with prose in it. The
// system prompt promises this ("A refusal is an answer") and the
// conversation design names it ("refused by policy"). Asserted on what the
// engine sent: the second request carries the refusal as a tool message,
// and the run completes with the model's answer. Not a spy over next.
func TestMiddleware_ApprovalBindsToExactArguments(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "approved read")
	writeFile(t, filepath.Join(dir, "b.txt"), "changed read")

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, ledger, approvals)

	argsA := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	argsB := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))

	// The exact proposal is approved by the person, and the call runs — as a
	// SUBSEQUENT attempt of the proposal's own entry (ADR-0020 decision 4):
	// the escalation's record plus the approved call's execution.
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	q.before = func() {
		if len(q.asked) != 1 {
			return // only the first question is the escalation this asserts
		}
		if s := ledger.started(); s != 1 {
			t.Errorf("ledger opened %d executions after the escalation, want 1 — the recorded proposal attempt", s)
		}
	}
	mw.kernel.questioner = q
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

	// A CHANGED argument does not run under the old approval: it is a new
	// proposal and asks again. The question is the assertion — the person is
	// asked about the changed bytes, never covered by the yes to the old ones.
	askedBefore := len(q.asked)
	if _, err := wrappedEndpoint(mw, "files.read", "call_1", argsB); err != nil {
		t.Fatalf("the changed-argument call = %v, want the person's answer as its result", err)
	}
	if len(q.asked) != askedBefore+1 {
		t.Fatal("the changed-argument call ran under the old approval")
	}
	if got := q.asked[len(q.asked)-1].ArgHash; got != canonicalArgHash(argsB) {
		t.Fatalf("the new question binds %q, want the changed arguments' hash", got)
	}
}

// ── a person's no: the declined proposal (nocx-uvac6.1) ──────────────────

// TestMiddleware_DeclinedProposalReturnsRefusalInsteadOfReasking is the
// brief's second acceptance criterion at the seam the engine cannot reach:
// the person's no is recorded against the EXACT proposal, and the resumed
// pipeline must not re-ask about it — the refusal is the call's result. A
// retry of the same call with a NEW call id is a different proposal: under
// an unchanged matrix it asks again, which is what a one-off no means.
func TestMiddleware_DeclinedProposalReturnsRefusalInsteadOfReasking(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, &fakeLedger{}, approvals)

	proposal := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	approvals.Request(proposal)
	approvals.Decline(proposal, DeclineCallOnce)

	// The declined proposal: the refusal IS the result — the call does not
	// run and is not re-asked.
	out, err := wrappedEndpoint(mw, "files.read", "call_1", args)
	if err != nil {
		t.Fatalf("the declined proposal returned an error %v — the refusal must be a tool result", err)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, "declined") {
		t.Fatalf("declined-call result = %q, want the person's refusal in our words", out)
	}
	if strings.Contains(out, "in this session") || strings.Contains(out, "from now on") {
		t.Fatalf("one-off refusal = %q, must not claim a standing refusal", out)
	}
	// A retry with a NEW call id is a different proposal: the matrix still
	// asks, so it is put to the person again — a one-off no is not standing.
	// (The turn's order refuses the calls queued BEHIND a declined one; a
	// retry the model proposes after seeing the refusal is a new turn step
	// and is asked about, which is what a one-off no means.)
	fresh := middlewareFor(t, grant, &fakeLedger{}, approvals)
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: "not this one"}, kind: DeclineCallOnce}
	fresh.kernel.questioner = q
	if retryOut, retryErr := wrappedEndpoint(fresh, "files.read", "call_2", args); retryErr != nil {
		t.Fatalf("the retry = %v, want the person's no as its answer", retryErr)
	} else if len(q.asked) != 1 {
		t.Fatalf("the retry ran after a one-off decline (result %q) — a one-off no must not be standing", retryOut)
	}
}

// TestMiddleware_StandingDeclineDoesNotLeakAcrossRuns (nocx-uvac6.1): the
// approval store is process-lifetime and SHARED by every run, and a standing
// no is a fact about the run it was given in. A "deny always" in run 1 must
// never answer a call in run 2 — run 2's grant is minted from the matrix the
// answer wrote (and, for a standing no, already refuses the effect), so a
// run-2 call is decided by run 2's own policy, never by another run's
// declined record. Without the run scoping, run 2's call here returns run
// 1's standing refusal instead of escalating under the unchanged matrix.
func TestMiddleware_StandingDeclineDoesNotLeakAcrossRuns(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	approvals := NewApprovalStore()

	// Run 1: the person answers "deny always" to the exact proposal — the
	// declined record carries run 1's identity and the effect class.
	proposal := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	approvals.Request(proposal)
	approvals.NoteEffect(Approval{
		RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1",
		ArgHash: canonicalArgHash(args), Effect: content.EffectObserve,
	})
	approvals.Decline(proposal, DeclineCallAlways)

	mw1 := middlewareFor(t, grant, &fakeLedger{}, approvals)
	out, err := wrappedEndpoint(mw1, "files.read", "call_1", args)
	if err != nil {
		t.Fatalf("run 1's declined proposal returned an error %v — want the refusal as a tool result", err)
	}
	if !strings.Contains(out, "from now on") {
		t.Fatalf("run 1's refusal = %q, want the standing sentence", out)
	}

	// Run 2: a NEW ask, its own middleware and its own run id. The matrix
	// still asks (this test's grant is the same ask-every-time matrix), so
	// the call must ESCALATE — a fresh question — never be answered by run
	// 1's declined record.
	reg, regErr := agenttools.Assemble(os.DirFS(realToolsFS))
	if regErr != nil {
		t.Fatalf("Assemble: %v", regErr)
	}
	mw2, mwErr := newKernelFixture(nil, grant, reg, &fakeLedger{}, approvals, &fakeKnownMaterial{}, "run-2", "", 1, "", nil, Attachments{}, nil, toolSeams{paneEnvironment: localPaneEnvironment(), files: newLocalFileSource(t)})
	if mwErr != nil {
		t.Fatalf("newKernelFixture(run-2): %v", mwErr)
	}
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: "not this one"}, kind: DeclineCallOnce}
	mw2.kernel.questioner = q
	if out2, err2 := wrappedEndpoint(mw2, "files.read", "call_1", args); err2 != nil {
		t.Fatalf("run 2's call = %v, want the person's no as its answer", err2)
	} else if len(q.asked) != 1 {
		t.Fatalf("run 2's call returned %q without asking — run 1's standing decline leaked across runs; run 2 must decide for itself", out2)
	}
}

// refusal text reaches the provider as a tool message.
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
	refs, err := decl.ResolveResources(map[string]any{"path": short}, localRunContext(t))
	if err != nil {
		t.Fatalf("ResolveResources: %v", err)
	}
	narrowed, err := decl.Narrow(grant, refs, localRunContext(t))
	if err != nil {
		t.Fatalf("Narrow: %v", err)
	}
	out, err := executeFilesRead(toolTestContext(), narrowed, json.RawMessage(fmt.Sprintf(`{"path":%q}`, short)), toolSeams{})
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
	emptyRefs, err := decl.ResolveResources(map[string]any{"path": empty}, localRunContext(t))
	if err != nil {
		t.Fatalf("ResolveResources(empty): %v", err)
	}
	emptyNarrowed, err := decl.Narrow(grant, emptyRefs, localRunContext(t))
	if err != nil {
		t.Fatalf("Narrow(empty): %v", err)
	}
	out, err = executeFilesRead(toolTestContext(), emptyNarrowed, json.RawMessage(fmt.Sprintf(`{"path":%q}`, empty)), toolSeams{})
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
		case agenttools.InRenderer, agenttools.Dynamic:
			// InRenderer and Dynamic have explicit middleware dispatch.
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

	cl, _, err := NewClientAndRegistry(nil, content.Floor{})
	if err != nil {
		t.Fatalf("NewClient outside the repo: %v", err)
	}
	internal, ok := cl.(*client)
	if !ok {
		t.Fatalf("NewClient returned %T, want *client", cl)
	}
	names := make([]string, 0, len(internal.tools.All()))
	for _, tl := range internal.tools.All() {
		names = append(names, tl.Name)
	}
	want := []string{"files.read", "fetch.url", "session.list", "session.read", "session.keys", "session.message", "session.run", "session.wait", "files.edit", "files.create", "git.status", "notes.search", "notes.create", "notes.update", "notes.delete", "snippets.list", "snippets.create", "snippets.update", "snippets.delete", "snippets.reorder", "workers.holdings", "workers.spawn", "workers.say", "workers.inbox", "workers.report", "workers.close", "workers.removeCheckout"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("assembled tools = %v, want %v", names, want)
	}
}

// ── the full pipeline against the real ledger ────────────────────────────

// realLedger opens a real content store in a temp dir — the honest ledger
// for the tests that exercise the full pipeline.
func TestMiddleware_RefusesWhenAnyResolvedResourceIsOutsideScope(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	inside := filepath.Join(dir, "inside.txt")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	var found bool
	for i, tool := range reg.All() {
		if tool.Name != "files.read" {
			continue
		}
		reg.All()[i].ResolveResources = func(args map[string]any, _ agenttools.RunContext) ([]agenttools.ResourceRef, error) {
			path, ok := args["path"].(string)
			if !ok {
				return nil, errors.New("path is not a string")
			}
			return []agenttools.ResourceRef{
				{Kind: content.ResourcePath, ID: path},
				{Kind: content.ResourcePath, ID: outside},
			}, nil
		}
		found = true
		break
	}
	if !found {
		t.Fatal("files.read declaration not found")
	}
	mw, err := newKernelFixture(nil, grant, reg, &fakeLedger{}, nil, &fakeKnownMaterial{}, "run-1", "", 1, "", nil, Attachments{}, nil, toolSeams{paneEnvironment: localPaneEnvironment(), files: newLocalFileSource(t)})
	if err != nil {
		t.Fatalf("newKernelFixture: %v", err)
	}
	out, err := wrappedEndpoint(mw, "files.read", "call-1", `{"path":"`+inside+`"}`)
	if err != nil {
		t.Fatalf("outside resource returned an error: %v", err)
	}
	if !strings.Contains(out, "REFUSED") {
		t.Fatalf("result = %q, want a refusal when the second resource is outside scope", out)
	}
}

func wrappedEndpointContext(mw *kernelFixture, ctx context.Context, name, callID, args string) (string, error) {
	out, err := mw.kernel.invokeClassified(ctx, name, callID, args)
	if err != nil {
		return "", err
	}
	if out.kind == modelNocxMessage {
		return out.text, nil
	}
	return mw.kernel.FrameForModel(name, out.text), nil
}
