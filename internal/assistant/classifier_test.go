package assistant

// The classifier's acceptance tests (bead nocx-kpy23). Two invariants, and
// the failure-shape that follows from them, are the bead:
//
//  1. The classifier may only raise suspicion, never lower it — composed
//     as the maximum over permit < ask < refuse, and mechanically enforced
//     by the parser: the ONLY verdict that does not escalate is an exact
//     "clear".
//  2. The classifier is an egress point: its input (the call arguments)
//     passes the SAME gate that screens the answering model's input — the
//     material does not reach the classifier either.
//  3. Failure is escalation, always: unreachable, timed out, unparseable
//     and role-unassigned each escalate; a gate that disappears when the
//     network is bad is not a gate.
//
// Consulted only where the policy says permit: a call the policy escalates
// or refuses never reaches the classifier, because its verdict cannot
// change the outcome.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
)

// ── helpers ───────────────────────────────────────────────────────────────

// recordingClassifier is the middleware's classifier seam with a call log —
// the recording client criterion 3 demands: a test that must fail when the
// classifier is invoked asserts on callCount(), never on the outcome.
type recordingClassifier struct {
	mu     sync.Mutex
	calls  []ClassifyInput
	result Classification
	err    error
}

func (r *recordingClassifier) Classify(_ context.Context, in ClassifyInput) (Classification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, in)
	if r.err != nil {
		return Classification{}, r.err
	}
	return r.result, nil
}

func (r *recordingClassifier) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// failIfCalledClassifier is criterion 3's sharpest form: a classifier that
// FAILS THE TEST the moment it is consulted. A "classifier is not
// consulted" assertion on a recording client would pass over a classifier
// that IS consulted and happens to return clear; this one cannot.
type failIfCalledClassifier struct {
	t *testing.T
}

func (f failIfCalledClassifier) Classify(context.Context, ClassifyInput) (Classification, error) {
	f.t.Fatal("the classifier must not be consulted for a call the policy escalates or refuses")
	return Classification{}, nil
}

// fixedResolver serves one (endpoint, model, key) — the classifier role's
// class, as the transport would resolve them.
type fixedResolver struct {
	target ClassifierTarget
}

func (f fixedResolver) ResolveClassifier(context.Context) (ClassifierTarget, error) {
	return f.target, nil
}

// failingResolver serves the role-resolution refusal — the fourth failure
// class of the bead ("whose role has no model assigned escalates").
type failingResolver struct {
	err error
}

func (f failingResolver) ResolveClassifier(context.Context) (ClassifierTarget, error) {
	return ClassifierTarget{}, f.err
}

// failCountingServer is the classifier provider criterion 4 asserts on: it
// counts the requests it receives and lets the TEST fail the moment a
// request arrived — a material-carrying argument must never reach it.
type failCountingServer struct {
	requests atomic.Int64
	srv      *httptest.Server
}

func newFailCountingServer() *failCountingServer {
	f := &failCountingServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, classifyCompletion(`{"verdict":"clear"}`))
	}))
	return f
}

func (f *failCountingServer) assertZero(t *testing.T) {
	t.Helper()
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("the classifier provider received %d request(s) — the material reached the classifier", n)
	}
}

// newClassifierServer answers the classifier's one-shot completion with a
// fixed body, recording how many requests arrived and the last one.
func newClassifierServer(body string) (*fakeOpenAIServer, *httptest.Server) {
	return newFakeOpenAI(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, body)
	})
}

// classifyCompletion is the non-streaming completion a classifier model
// produces: choices[0].message.content carries the verdict JSON.
func classifyCompletion(content string) string {
	return `{"id":"chatcmpl-clf","object":"chat.completion","created":0,"model":"clf-model",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":` +
		strconv.Quote(content) +
		`},"finish_reason":"stop"}]}`
}

// classifierTargetAt builds the resolved classifier target for a test
// server, the shape the transport would produce when the classifier role
// resolves.
func classifierTargetAt(url string) ClassifierTarget {
	return ClassifierTarget{Key: credential.NewSecret("sk-clf-test"), BaseURL: url, Model: "clf-model"}
}

// askParamsWithClassifier is askParams plus the classifier resolver — the
// one seam this bead adds to the run.
func askParamsWithClassifier(p AskParams, resolver ClassifierResolver) AskParams {
	p.Classifier = resolver
	return p
}

// middlewareForWithClassifier builds the pipeline with an explicit
// classifier seam (nil = the feature off, the state every caller is in
// today).
func middlewareForWithClassifier(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, classifier CallClassifier) *policyMiddleware {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	mw, err := newPolicyMiddleware(nil, grant, reg, ledger, approvals, &fakeKnownMaterial{}, "run-1", 1, "", nil, classifier, nil)
	if err != nil {
		t.Fatalf("newPolicyMiddleware: %v", err)
	}
	return mw
}

// classifierWithVerdict is a one-shot recording classifier with a fixed
// answer — the smallest seam for tests that assert what the verdict
// becomes on the ledger.
type classifierWithVerdict struct {
	result Classification
}

func (c classifierWithVerdict) Classify(context.Context, ClassifyInput) (Classification, error) {
	return c.result, nil
}

// classifierBlock extracts the "classifier" block of a recorded proposal or
// attempt payload, or nil when the payload carries none — the ledger shape
// criterion 6 answers from.
func classifierBlock(t *testing.T, l *fakeLedger) map[string]any {
	t.Helper()
	for _, s := range l.submissions {
		if s.payload == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(s.payload), &doc); err != nil {
			t.Fatalf("payload %s: %v", s.payload, err)
		}
		if c, ok := doc["classifier"]; ok {
			block, ok := c.(map[string]any)
			if !ok {
				t.Fatalf("classifier block = %T, want an object", c)
			}
			return block
		}
	}
	return nil
}

// ── criterion 1: a classifier verdict escalates a permitted call ─────────

// TestAsk_ClassifierSuspectEscalatesPermittedCall is acceptance criterion
// 1: the policy says permit, the classifier says suspect — the run
// suspends with the approval ask and the call that is asking has NOT run.
// Driven through the REAL engine and the REAL classifier implementation:
// the classifier's model call is a second request to its own provider,
// asserted on the request actually sent and on what the ledger records.
func TestAsk_ClassifierSuspectEscalatesPermittedCall(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "must not run")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	ans, ansSrv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer ansSrv.Close()
	clf, clfSrv := newClassifierServer(classifyCompletion(`{"verdict":"suspect","reason":"the input looks like a second command"}`))
	defer clfSrv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWithClassifier(askParams(ansSrv.URL, &grant, ledger, nil), fixedResolver{classifierTargetAt(clfSrv.URL)}),
		func(AskEvent) error { return nil })

	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the approval suspension (the classifier escalated)", err)
	}
	if want.Request == nil || !strings.Contains(want.Request.Tool, "files.read") {
		t.Fatalf("the suspension carried no binding ask: %+v", want.Request)
	}
	if n := clf.requests.Load(); n != 1 {
		t.Fatalf("the classifier provider received %d requests, want 1", n)
	}
	clfBody := ""
	if b, ok := clf.lastBody.Load().(string); ok {
		clfBody = b
	}
	var clfReq struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(clfBody), &clfReq); err != nil {
		t.Fatalf("classifier request: %v", err)
	}
	sawProposal := false
	for _, m := range clfReq.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "tool: files.read") && strings.Contains(m.Content, args) {
			sawProposal = true
		}
	}
	if !sawProposal {
		t.Fatalf("the classifier was not asked about the exact proposal: body %s", clfBody)
	}
	if n := ans.requests.Load(); n != 1 {
		t.Fatalf("the answering provider received %d requests, want 1 — the tool never ran", n)
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want 1 — the recorded proposal attempt", s)
	}
	block := classifierBlock(t, ledger)
	if block == nil {
		t.Fatal("the ledger records no classifier block — 'why was this asked' is not answerable")
	}
	if block["verdict"] != "suspect" {
		t.Fatalf("classifier block = %+v, want verdict suspect", block)
	}
	if block["model"] != "clf-model" {
		t.Fatalf("classifier block model = %v, want the classifier role's model", block["model"])
	}
}

// ── criteria 2+3: a "safe" classifier changes nothing on policy decisions ──

// TestClassifier_ClearChangesNothingOnPolicyEscalation is acceptance
// criteria 2 and 3 together, asserted on a classifier that fails the test
// if invoked: a classifier that WOULD say "clear" is NEVER consulted for a
// call the policy escalates or refuses — its verdict cannot change the
// outcome, so the policy's decision stands untouched.
func TestClassifier_ClearChangesNothingOnPolicyEscalation(t *testing.T) {
	// Arm 1: the policy REFUSES (a scope the grant does not cover — a
	// cleared classifier cannot rescue the call). The refusal is the call's
	// result, in our words, and the classifier was never consulted.
	grant, _ := testDirGrant(t, autonomousMatrix()) // scoped to the temp dir
	outside := t.TempDir() + "/outside.txt"
	ledger := &fakeLedger{}
	mw := middlewareForWithClassifier(t, grant, ledger, nil, failIfCalledClassifier{t})
	out, err := wrappedEndpoint(mw, "files.read", "call_1", fmt.Sprintf(`{"path":%q}`, outside))
	if err != nil {
		t.Fatalf("refused-call error = %v, want the refusal as a tool result — a clear classifier changed nothing", err)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, "outside") {
		t.Fatalf("refused-call result = %q, want the refusal naming why in our words", out)
	}

	// Arm 2: the policy ASKS (ask-every-time). The classifier would say
	// clear — the call still escalates and the classifier is never asked.
	grantAsk, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "must not run")
	ledgerAsk := &fakeLedger{}
	mwAsk := middlewareForWithClassifier(t, grantAsk, ledgerAsk, nil, failIfCalledClassifier{t})
	if _, err := wrappedEndpoint(mwAsk, "files.read", "call-ask", fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))); err == nil {
		t.Fatal("the ask-policy call ran under a clear-classifier — its verdict changed the outcome")
	}
}

// ── criterion 4: the classifier's input passes the egress gate ──────────────

// TestAsk_ClassifierInputKnownMaterialNeverReachesClassifier is acceptance
// criterion 4: an argument carrying known vault material suspends the run
// BEFORE the classifier is asked. The egress gate is a party to the
// classifier's input exactly as it is to the answering model's result, and
// the material does not reach the classifier either — asserted on what the
// classifier provider actually received: nothing.
func TestAsk_ClassifierInputKnownMaterialNeverReachesClassifier(t *testing.T) {
	const secret = "known-secret-value-clf"
	grant, dir := testDirGrant(t, autonomousMatrix())
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, secret+".txt"))

	ledger := &fakeLedger{}
	ans, ansSrv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer ansSrv.Close()
	clfCount := newFailCountingServer()
	defer clfCount.srv.Close()

	p := askParams(ansSrv.URL, &grant, ledger, nil)
	p.KnownMaterial = &knownMatcher{value: secret, name: "github-token"}
	p = askParamsWithClassifier(p, fixedResolver{classifierTargetAt(clfCount.srv.URL)})

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil })

	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the approval suspension — the classifier could not be consulted, and the call escalated", err)
	}
	clfCount.assertZero(t) // the material never reached the classifier
	if n := ans.requests.Load(); n != 1 {
		t.Fatalf("the answering provider received %d requests, want 1", n)
	}
	block := classifierBlock(t, ledger)
	if block == nil {
		t.Fatal("the ledger records no classifier block for the input-gate escalation")
	}
	findings, ok := block["findings"].([]any)
	if !ok || len(findings) != 1 {
		t.Fatalf("classifier block findings = %v, want one finding", block["findings"])
	}
	f0, ok := findings[0].(map[string]any)
	if !ok || f0["source"] != "known" || f0["secretName"] != "github-token" {
		t.Fatalf("finding = %v, want the known-material finding named github-token", findings[0])
	}
	// The findings are facts only — kind, source, offsets, the NAME — the
	// value itself is the thing being withheld, on the classifier path too.
	for _, s := range ledger.submissions {
		if !strings.Contains(s.payload, "findings") {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(s.payload), &doc); err != nil {
			t.Fatalf("payload %s: %v", s.payload, err)
		}
		blob, _ := json.Marshal(doc["classifier"])
		if strings.Contains(string(blob), secret) {
			t.Fatalf("the recorded classifier facts carry the material that was withheld: %s", blob)
		}
	}
}

// ── criterion 5: the four failure classes each escalate ─────────────────────

// TestClassifierUnreachableEscalates is failure one: a classifier that
// cannot be reached escalates — a gate that disappears when the network is
// bad is not a gate. The concrete classifier dials a closed destination;
// the run suspends and the ledger says why.
func TestClassifierUnreachableEscalates(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "must not run")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	ans, ansSrv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer ansSrv.Close()

	// Port 1 is closed by convention on loopback, which the guard permits —
	// the dial fails with connection refused.
	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWithClassifier(askParams(ansSrv.URL, &grant, ledger, nil), fixedResolver{classifierTargetAt("http://127.0.0.1:1")}),
		func(AskEvent) error { return nil })

	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the escalation (unreachable classifier must escalate)", err)
	}
	block := classifierBlock(t, ledger)
	if block == nil || !strings.Contains(fmt.Sprint(block["reason"]), "connection refused") {
		t.Fatalf("the escalation reason does not name the failure: %v", block)
	}
	if n := ans.requests.Load(); n != 1 {
		t.Fatalf("the answering provider received %d requests, want 1 — the tool never ran", n)
	}
}

// TestClassifierTimeoutEscalates is failure two: a classifier whose answer
// does not arrive in time escalates — a query that hangs is a classifier
// that is not a gate.
func TestClassifierTimeoutEscalates(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "must not run")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	mw := middlewareForWithClassifier(t, grant, ledger, nil, &recordingClassifier{err: context.DeadlineExceeded})
	_, err := wrappedEndpoint(mw, "files.read", "call_1", args)
	if err == nil {
		t.Fatal("a classifier that timed out cleared the call")
	}
	block := classifierBlock(t, ledger)
	if block == nil || !strings.Contains(fmt.Sprint(block["reason"]), "deadline exceeded") {
		t.Fatalf("the escalation reason does not name the timeout: %v", block)
	}
}

// TestAsk_ClassifierUnparseableEscalates is failure three: the
// classifier's answer is not a verdict — it escalates. The parser is the
// mechanical floor of invariant 1: anything that is not an exact "clear" or
// "suspect" is a failure, and an unknown word is never read as "clear".
func TestAsk_ClassifierUnparseableEscalates(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "must not run")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	_, ansSrv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer ansSrv.Close()
	_, clfSrv := newClassifierServer(classifyCompletion(`{"verdict":"safe"}`))
	defer clfSrv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWithClassifier(askParams(ansSrv.URL, &grant, ledger, nil), fixedResolver{classifierTargetAt(clfSrv.URL)}),
		func(AskEvent) error { return nil })
	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the escalation (unparseable classifier output)", err)
	}
	block := classifierBlock(t, ledger)
	if block == nil || !strings.Contains(fmt.Sprint(block["reason"]), "verdict") {
		t.Fatalf("the escalation reason does not name the unparseable verdict: %v", block)
	}
}

// TestClassifierRoleUnassignedEscalates is failure four: the classifier
// role has no model assignment — every permit call escalates.
func TestClassifierRoleUnassignedEscalates(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "must not run")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	ans, ansSrv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer ansSrv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWithClassifier(askParams(ansSrv.URL, &grant, ledger, nil),
			failingResolver{err: errors.New("classifier role: no model assigned")}),
		func(AskEvent) error { return nil })
	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the escalation (role-unassigned must escalate)", err)
	}
	block := classifierBlock(t, ledger)
	if block == nil || !strings.Contains(fmt.Sprint(block["reason"]), "no model assigned") {
		t.Fatalf("the escalation reason does not name the unassigned role: %v", block)
	}
	if n := ans.requests.Load(); n != 1 {
		t.Fatalf("the answering provider received %d requests, want 1", n)
	}
}

// ── criterion 6: the verdict is recorded, so the ledger answers "why" ───────

// TestClassifier_VerdictRecordedOnTheAttempt is acceptance criterion 6:
// "why was this asked" is answerable afterwards from the ledger — the
// classifier block rides the recorded proposal/attempt payload, never a
// log. Two ends: the suspect escalation's record carries the verdict and
// the model that supplied it; a cleared call's attempt record carries the
// clearing, so the audit sees both dispositions.
func TestClassifier_VerdictRecordedOnTheAttempt(t *testing.T) {
	{
		grant, dir := testDirGrant(t, autonomousMatrix())
		writeFile(t, filepath.Join(dir, "a.txt"), "x")
		args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
		ledger := &fakeLedger{}
		mw := middlewareForWithClassifier(t, grant, ledger, nil, classifierWithVerdict{result: Classification{Verdict: ClassifierSuspect, Model: "clf-model"}})
		if _, err := wrappedEndpoint(mw, "files.read", "call_1", args); err == nil {
			t.Fatal("the suspect call did not escalate")
		}
		block := classifierBlock(t, ledger)
		if block == nil || block["verdict"] != "suspect" || block["model"] != "clf-model" {
			t.Fatalf("proposal block = %v, want {verdict: suspect, model: clf-model}", block)
		}
	}
	{
		grant, dir := testDirGrant(t, autonomousMatrix())
		writeFile(t, filepath.Join(dir, "a.txt"), "cleared read")
		args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
		ledger := &fakeLedger{}
		mw := middlewareForWithClassifier(t, grant, ledger, nil, classifierWithVerdict{result: Classification{Verdict: ClassifierClear, Model: "clf-model"}})
		out, err := wrappedEndpoint(mw, "files.read", "call_2", args)
		if err != nil {
			t.Fatalf("the cleared call failed: %v", err)
		}
		if !strings.Contains(out, "cleared") {
			t.Fatalf("cleared output = %s, want the file's contents", out)
		}
		block := classifierBlock(t, ledger)
		if block == nil || block["verdict"] != "clear" || block["model"] != "clf-model" {
			t.Fatalf("attempt block = %v, want {verdict: clear, model: clf-model}", block)
		}
	}
}

// ── criterion 7: both ends ────────────────────────────────────────────────

// TestAsk_ClassifierQuietPermittedCallRuns is criterion 7's first half:
// with the classifier configured and quiet ("clear"), an ordinary permitted
// call still runs — the classifier's latency is on the path, its verdict
// is not.
func TestAsk_ClassifierQuietPermittedCallRuns(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "the file's contents")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	ans, ansSrv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer ansSrv.Close()
	clf, clfSrv := newClassifierServer(classifyCompletion(`{"verdict":"clear"}`))
	defer clfSrv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	var got string
	err := cl.Ask(context.Background(),
		askParamsWithClassifier(askParams(ansSrv.URL, &grant, ledger, nil), fixedResolver{classifierTargetAt(clfSrv.URL)}),
		func(e AskEvent) error {
			if e.Kind == AskAnswer {
				got += e.Text
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("answer = %q, want the model's reply", got)
	}
	if n := clf.requests.Load(); n != 1 {
		t.Fatalf("the classifier received %d requests, want exactly 1", n)
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want 1", s)
	}
	// The tool result rode the second answering request — the cleared call
	// ran and its result reached the model as it does without a classifier.
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(ans.body()), &req); err != nil {
		t.Fatalf("request 2 body: %v", err)
	}
	found := false
	for _, m := range req.Messages {
		if m["role"] == "tool" {
			found = true
			content, _ := m["content"].(string)
			if !strings.Contains(content, "the file's contents") {
				t.Fatalf("tool message content = %s, want the file's contents", content)
			}
		}
	}
	if !found {
		t.Fatal("the tool result never reached the model")
	}
}

// TestAsk_NoClassifierBehavesAsToday is criterion 7's second half: with no
// classifier resolver wired for the run, permitted calls run exactly as
// the product runs today — no consultation, no escalation, and the attempt
// record carries no classifier block.
func TestAsk_NoClassifierBehavesAsToday(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "as today")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	ans, ansSrv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer ansSrv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(), askParams(ansSrv.URL, &grant, ledger, nil), func(AskEvent) error { return nil })
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if n := ans.requests.Load(); n != 2 {
		t.Fatalf("requests = %d, want 2 (the ordinary call + answer — no classifier request, no escalation)", n)
	}
	if block := classifierBlock(t, ledger); block != nil {
		t.Fatalf("attempt records a classifier block without a classifier: %v", block)
	}
}

// ── the approval loop terminates ────────────────────────────────────────────

// TestMiddleware_ApprovedClassifierAskSkipsReclassification pins the loop
// property of the classifier design: approving the classifier's ask covers
// the whole proposal, so the resumed call does NOT consult the classifier
// a second time (which could ask forever). After one approval the call
// runs and the classifier has been consulted exactly once.
func TestMiddleware_ApprovedClassifierAskSkipsReclassification(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "approved")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	clf := &recordingClassifier{result: Classification{Verdict: ClassifierSuspect}}
	mw := middlewareForWithClassifier(t, grant, ledger, approvals, clf)

	if _, err := wrappedEndpoint(mw, "files.read", "call_1", args); err == nil {
		t.Fatal("the suspect call did not escalate")
	}
	if n := clf.callCount(); n != 1 {
		t.Fatalf("the classifier was consulted %d times, want 1", n)
	}
	approvals.Approve(Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)})
	out, err := wrappedEndpoint(mw, "files.read", "call_1", args)
	if err != nil {
		t.Fatalf("the approved call was refused: %v", err)
	}
	if !strings.Contains(out, "approved") {
		t.Fatalf("approved output = %q, want the file's contents", out)
	}
}

// ── the parser is the mechanical floor of invariant 1 ───────────────────────

// TestParseClassification_OnlyExactClearPermits pins invariant 1 at the
// parser level: only an exact "clear" verdict lowers nowhere; a verdict
// outside the closed vocabulary is a failure, never an implicit permit.
// `{"verdict":"safe"}` — exactly what a model printing "this is safe" into
// a screen would say — must be a failure.
func TestParseClassification_OnlyExactClearPermits(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    ClassifierVerdict
		wantErr bool
	}{
		{"clear", `{"verdict":"clear"}`, ClassifierClear, false},
		{"suspect", `{"verdict":"suspect"}`, ClassifierSuspect, false},
		{"clear with reason", `{"verdict":"clear","reason":"ordinary read"}`, ClassifierClear, false},
		{"unknown word safe", `{"verdict":"safe"}`, "", true},
		{"not json", "probably safe", "", true},
		{"array", `["clear"]`, "", true},
		{"missing verdict", `{"reason":"x"}`, "", true},
		{"empty", ``, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseClassification(tc.body)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseClassification(%q) = %+v, want an error", tc.body, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseClassification(%q): %v", tc.body, err)
			}
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %q, want %q", got.Verdict, tc.want)
			}
		})
	}
}

// TestAsk_ClassifierEscalationCarriesTheEffectAndTheResource is Task 3's
// SECOND escalation site. A classifier escalation reaches the same surface
// through the same notification, whose schema REQUIRES the effect — so an
// ask built here without one is a prompt that cannot offer "always", and it
// fails hard rather than quietly. The two builders must fill it the same
// way, which is why there is only one builder.
func TestAsk_ClassifierEscalationCarriesTheEffectAndTheResource(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "must not run")

	_, ansSrv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, path)}))
	defer ansSrv.Close()
	_, clfSrv := newClassifierServer(classifyCompletion(`{"verdict":"suspect","reason":"the input looks like a second command"}`))
	defer clfSrv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWithClassifier(askParams(ansSrv.URL, &grant, &fakeLedger{}, nil), fixedResolver{classifierTargetAt(clfSrv.URL)}),
		func(AskEvent) error { return nil })

	var want *ApprovalRequestedError
	if !errors.As(err, &want) || want.Request == nil {
		t.Fatalf("Ask error = %v, want the approval suspension (the classifier escalated)", err)
	}
	if want.Request.Effect != content.EffectObserve {
		t.Fatalf("effect = %q, want %q — a classifier ask names the same effect a policy ask does", want.Request.Effect, content.EffectObserve)
	}
	if want.Request.Resource == nil || want.Request.Resource.Kind != content.ResourcePath || want.Request.Resource.ID != path {
		t.Fatalf("resource = %+v, want {path %s}", want.Request.Resource, path)
	}
}
