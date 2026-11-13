package assistant

// The egress gate's acceptance tests (nocx-0p7y2, design §7.1): a tool
// result containing secret-shaped material does not leave for the provider —
// the call BLOCKS on the person's answer and the findings name what was found
// and where. These are the criteria 1-5 assertions, driven through the REAL
// eino agent against the fake OpenAI server (asserted on what the engine sent)
// and through the middleware where the engine seam cannot reach.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/skill"
)

// knownMatcher is the vault-comparison seam whose answer is computed from
// the text it is given: a value the "vault" holds, reported at its actual
// byte span with the catalogue name the surface would show. This is the
// seam's honest contract — spans into the text, values never crossing it.
type knownMatcher struct {
	value string
	name  string
}

func (k *knownMatcher) FindKnown(_ context.Context, text string) ([]KnownMatch, error) {
	i := strings.Index(text, k.value)
	if i < 0 {
		return nil, nil
	}
	return []KnownMatch{{Start: i, End: i + len(k.value), SecretName: k.name}}, nil
}

// askParamsWith builds AskParams with the full run seams: the ledger, the
// approvals, the egress vault comparison and the renderer-request seam.
func askParamsWith(baseURL string, grant *content.Grant, ledger AttemptLedger, approvals *ApprovalStore, known KnownMaterial, requester RendererRequester) AskParams {
	p := askParams(baseURL, grant, ledger, approvals)
	p.KnownMaterial = known
	p.Requester = requester
	return p
}

// ── criterion 1: a value known to the vault does not leave ───────────────

// TestAsk_EgressKnownVaultValueIsAskedBeforeItLeaves is acceptance criterion
// 1: a tool result containing a value the vault holds is withheld until the
// person answers, and the finding names it as known material. The value is
// deliberately NOT a recognizer shape (a short password-like token) — the
// vault comparison is what catches it, and the assertion proves that path
// fires. Nothing reached the model before the answer: exactly one request hit
// the server, the one that proposed the call.
func TestAsk_EgressKnownVaultValueIsAskedBeforeItLeaves(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const secret = "known-secret-value-123"
	writeFile(t, filepath.Join(dir, "a.txt"), "deploy key: "+secret)

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))}))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: "do not send that"}, kind: DeclineCallOnce}
	q.before = func() {
		if n := f.requests.Load(); n != 1 {
			t.Errorf("the engine made %d model requests while the result was withheld, want 1", n)
		}
	}
	p := askParamsWith(srv.URL, &grant, ledger, approvals, &knownMatcher{value: secret, name: "github-token"}, nil)
	p.Questioner = q
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err == nil {
		t.Fatal("the run went on after the person refused to send the result")
	}
	if len(q.egressAsked) != 1 {
		t.Fatalf("the person was asked %d egress questions, want exactly 1", len(q.egressAsked))
	}
	req := q.egressAsked[0]
	if len(req.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", req.Findings)
	}
	f0 := req.Findings[0]
	if f0.Source != EgressFindingKnown {
		t.Fatalf("finding source = %q, want known (the vault comparison fired)", f0.Source)
	}
	if f0.SecretName != "github-token" {
		t.Fatalf("finding names %q, want the vault's catalogue name", f0.SecretName)
	}
	if f0.Start < 0 || f0.End <= f0.Start {
		t.Fatalf("finding has no valid span: %+v", f0)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests, want 1 — the tool result never left for the provider", n)
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want 1 (the call ran; its result was withheld)", s)
	}
}

// ── criterion 2: a heuristic match suspends the same way, distinguishably ─

// TestAsk_EgressHeuristicIsAskedDistinguishably is acceptance criterion 2: a
// recognizer match asks through the same gate, and the finding is
// distinguishable from the known-material case — the source field names which
// detector fired, and the heuristic finding carries the recognizer's own kind
// where the known finding carried the vault's name.
func TestAsk_EgressHeuristicIsAskedDistinguishably(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const key = "sk-proj-abcdefghijklmnopqrstuvwx"
	writeFile(t, filepath.Join(dir, "a.txt"), "the key is "+key)

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))}))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: "do not send that"}, kind: DeclineCallOnce}
	p := askParamsWith(srv.URL, &grant, ledger, approvals, &knownMatcher{}, nil) // the vault knows nothing
	p.Questioner = q
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err == nil {
		t.Fatal("the run went on after the person refused to send the result")
	}
	if len(q.egressAsked) != 1 {
		t.Fatalf("the person was asked %d egress questions, want exactly 1", len(q.egressAsked))
	}
	if len(q.egressAsked[0].Findings) == 0 {
		t.Fatal("no findings for a recognizer-shaped value")
	}
	f0 := q.egressAsked[0].Findings[0]
	if f0.Source != EgressFindingHeuristic {
		t.Fatalf("finding source = %q, want heuristic — a shape, not a known value", f0.Source)
	}
	if f0.Kind == "" {
		t.Fatalf("heuristic finding carries no kind: %+v", f0)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests, want 1 — nothing left for the provider", n)
	}
}

// ── criterion 3: an error returned by a tool is screened on the same path ─

// TestAsk_EgressErrorStringScreened is acceptance criterion 3: a tool that
// FAILS is screened on the same path as a success — an error string carries
// paths, hostnames and names, and the gate that screens successes and not
// failures has closed the wide door and left the narrow one open. The
// session.read tool's renderer-request seam fails with a secret-shaped
// message; the question reports the finding and that it was an error.
func TestAsk_EgressErrorStringScreened(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	requester := &recordingRequester{err: errors.New("capture failed: token sk-proj-abcdefghijklmnopqrstuvwx")}

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "session.read", args: `{}`}))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: "do not send that"}, kind: DeclineCallOnce}
	p := askParamsWith(srv.URL, &grant, ledger, approvals, &knownMatcher{}, requester)
	p.Questioner = q
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err == nil {
		t.Fatal("the run went on after the person refused to send the error")
	}
	if len(q.egressAsked) != 1 {
		t.Fatalf("the person was asked %d egress questions, want exactly 1", len(q.egressAsked))
	}
	if !q.egressAsked[0].WasError {
		t.Fatal("the question does not say the findings were in an error string")
	}
	if len(q.egressAsked[0].Findings) == 0 {
		t.Fatal("no findings in the error string")
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests, want 1 — the error never left for the provider", n)
	}
}

// ── criterion 4: no finding means the result is returned unchanged ────────

// TestMiddleware_EgressNoFindingReturnsByteForByte is acceptance criterion
// 4's paired end: a tool result with no finding passes the egress gate
// unchanged, byte for byte. The shared model-facing frame is applied only
// after that gate, so this test compares the executor bytes with the result
// after the one deliberate framing step.
func TestMiddleware_EgressNoFindingReturnsByteForByte(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the file's contents")
	args := fmt.Sprintf(`{"path":%q}`, path)

	mw := middlewareFor(t, grant, &fakeLedger{}, nil)
	out, err := wrappedEndpoint(mw, "files.read", "call_1", args)
	if err != nil {
		t.Fatalf("wrappedEndpoint: %v", err)
	}

	// The reference is the executor's own bytes, untouched by the egress gate;
	// the model-facing return adds the registry-derived frame exactly once.
	decl, ok := mw.kernel.registry.Lookup("files.read")
	if !ok {
		t.Fatal("files.read not in the registry")
	}
	refs, refErr := decl.ResolveResources(map[string]any{"path": path}, agenttools.RunContext{})
	if refErr != nil {
		t.Fatalf("ResolveResources: %v", refErr)
	}
	cap, capErr := decl.Narrow(grant, refs, agenttools.RunContext{})
	if capErr != nil {
		t.Fatalf("Narrow: %v", capErr)
	}
	ref, refErr := executors["files.read"](toolTestContext(), cap, []byte(args), toolSeams{})
	if refErr != nil {
		t.Fatalf("executor: %v", refErr)
	}
	want := decl.FrameToolResult(ref)
	if out != want {
		t.Fatalf("the gate changed the result:\n through the gate: %s\n expected model result: %s", out, want)
	}
	if !strings.Contains(out, "the file's contents") {
		t.Fatalf("result = %s, want the file's contents in the window", out)
	}
}

func TestExecuteSkillsReadReturnsNormalizedPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "references"), 0o700); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	writeFile(t, filepath.Join(root, "deploy", "SKILL.md"), "---\nname: deploy\ndescription: deployment instructions\n---\n\nbody")
	writeFile(t, filepath.Join(root, "deploy", "references", "guide.md"), "guide")

	source := skill.NewStore(skill.OSFileSystem{}, []skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, nil)
	cap := agenttools.NewContentScope([]agenttools.ResourceRef{{
		Kind: content.ResourceContent,
		ID:   "content",
	}})
	got, err := executeSkillsRead(context.Background(), cap, json.RawMessage(`{"name":"deploy","path":"./references/guide.md"}`), toolSeams{skills: source})
	if err != nil {
		t.Fatalf("executeSkillsRead: %v", err)
	}

	var result skillReadResult
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Path != "references/guide.md" {
		t.Fatalf("result path = %q, want normalized path", result.Path)
	}
	if result.Content != "guide" {
		t.Fatalf("result content = %q, want guide", result.Content)
	}
}

// ── criterion 5: no finding ever carries the secret material ──────────────

// TestEgressRequest_NeverCarriesMaterial is acceptance criterion 5: a finding
// is kind/source/offset/name only — the value it found is the thing being
// withheld. Asserted on the JSON the surface renders, which is the one form
// the request travels in now that the question blocks instead of crossing a
// checkpoint.
func TestEgressRequest_NeverCarriesMaterial(t *testing.T) {
	const secret = "sk-proj-abcdefghijklmnopqrstuvwx"
	req := &EgressRequest{
		RunID:     "run-1",
		Attempt:   1,
		Tool:      "files.read",
		CallID:    "call_1",
		Arguments: `{"path":"/workspace/a.txt"}`,
		Findings: []EgressFinding{
			{Source: EgressFindingHeuristic, Kind: "openai", Start: 10, End: 40},
			{Source: EgressFindingKnown, SecretName: "github-token", Start: 60, End: 90},
		},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("the request carries the secret value itself")
	}
	for _, frag := range []string{"sk-proj", "abcdefghijklmnopqrstuvwx"} {
		if strings.Contains(string(raw), frag) {
			t.Errorf("the request carries a fragment of the value (%q)", frag)
		}
	}
}

// ── a declined result ends the run before the next call ───────────────────

// TestAsk_ADeclinedEgressStopsTheRunBeforeTheNextCall: a finding is answered
// with a no, and no call after it in that model response runs — asserted by
// what the ledger records: exactly one execution (the first call's), never a
// second. The bytes ARE the call's result, so a no leaves nothing to continue
// with and the run ends (ADR-anntnxdj §3).
func TestAsk_ADeclinedEgressStopsTheRunBeforeTheNextCall(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "token: sk-proj-abcdefghijklmnopqrstuvwx")
	writeFile(t, filepath.Join(dir, "b.txt"), "clean")

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))},
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))},
	))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: "do not send that"}, kind: DeclineCallOnce}
	p := askParamsWith(srv.URL, &grant, ledger, approvals, &knownMatcher{}, nil)
	p.Questioner = q
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err == nil {
		t.Fatal("the run went on after the person refused to send the result")
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want exactly 1 — the second call must not run after the refusal", s)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the engine made %d model requests, want 1 — nothing left for the provider", n)
	}
}

// ── the seam fails closed ─────────────────────────────────────────────────

// TestMiddleware_NewPolicyFailsClosedWithoutKnownMaterial pins the wiring
// requirement: a run that may execute tools MUST carry the egress vault
// comparison, or the gate cannot see short vault values and a result would
// leave for the provider unscreened. The failure is at construction, before
// any tool runs — never a silent weaker gate.
func TestMiddleware_NewPolicyFailsClosedWithoutKnownMaterial(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if _, err := newPolicyMiddleware(nil, grant, reg, &fakeLedger{}, NewApprovalStore(), nil, "run-1", "", 1, "", nil, Attachments{}, nil, nil); err == nil {
		t.Fatal("newPolicyMiddleware accepted a run with no egress vault comparison")
	}
}

// THE MASKING BELT IS FOR THE ONE MOMENT THE GATE STANDS DOWN (nocx-hp8p2.13).
// The egress gate asks on a finding, so a result reaching the durable record
// normally carries none. An APPROVED question is the exception: the person
// authorised sending those exact bytes to the provider, and they go. What must
// not follow is a second, unmasked copy of them in the store — the pane's
// "show me what it returned" would then be a path around the masking every
// other durable row obeys (ADR-0021, nocx-a21v).
func TestMiddleware_ApprovedEgressResultIsRecordedMasked(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const secret = "sk-proj-abcdefghijklmnopqrstuvwx"
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	writeFile(t, filepath.Join(dir, "a.txt"), "the key is "+secret)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, ledger, approvals)

	// The gate finds the key and asks; nothing is recorded while the person
	// decides, and the tool's body is not entered a second time.
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	mw.kernel.questioner = q
	out, err := wrappedEndpoint(mw, "files.read", "call_1", args)
	if err != nil {
		t.Fatalf("the approved call failed: %v", err)
	}
	if len(q.egressAsked) != 1 {
		t.Fatalf("the person was asked %d egress questions, want exactly 1", len(q.egressAsked))
	}
	if !strings.Contains(out, secret) {
		t.Fatalf("the model received %q, want the exact approved bytes", out)
	}
	captures := ledger.recordedCaptures()
	if len(captures) != 1 {
		t.Fatalf("captures = %d, want the call's own body", len(captures))
	}
	body := string(captures[0].Body)
	if strings.Contains(body, secret) {
		t.Fatalf("recorded body = %q, want the key masked out of the durable record", body)
	}
	if !strings.Contains(body, "the key is ") {
		t.Fatalf("recorded body = %q, want the rest of the result kept", body)
	}
}
