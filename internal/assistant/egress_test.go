package assistant

// The egress gate's acceptance tests (nocx-0p7y2, design §7.1): a tool
// result containing secret-shaped material does not leave for the provider —
// the run suspends and the findings name what was found and where. These
// are the criteria 1-5 assertions, driven through the REAL eino agent
// against the fake OpenAI server (asserted on what the engine sent) and
// through the middleware where the engine seam cannot reach.

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
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

// TestAsk_EgressKnownVaultValueSuspends is acceptance criterion 1: a tool
// result containing a value the vault holds suspends the run before the
// bytes leave, and the finding names it as known material. The value is
// deliberately NOT a recognizer shape (a short password-like token) — the
// vault comparison is what catches it, and the assertion proves that path
// fires. Nothing reached the model: exactly one request hit the server.
func TestAsk_EgressKnownVaultValueSuspends(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const secret = "known-secret-value-123"
	writeFile(t, filepath.Join(dir, "a.txt"), "deploy key: "+secret)

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWith(srv.URL, &grant, ledger, nil, &knownMatcher{value: secret, name: "github-token"}, nil),
		func(AskEvent) error { return nil })
	var want *EgressRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the egress suspension (not a failure, not a tool result)", err)
	}
	if want.Request == nil {
		t.Fatal("the egress suspension carried no request")
	}
	if len(want.Request.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", want.Request.Findings)
	}
	f0 := want.Request.Findings[0]
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
	interrupted := false
	for _, c := range ledger.calls() {
		if c == "finish:interrupted" {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatalf("the suspended pass's attempt did not close as interrupted: %v", ledger.calls())
	}
}

// ── criterion 2: a heuristic match suspends the same way, distinguishably ─

// TestAsk_EgressHeuristicSuspendsDistinguishably is acceptance criterion 2:
// a recognizer match suspends through the same gate, and the finding is
// distinguishable from the known-material case — the source field names
// which detector fired, and the heuristic finding carries the recognizer's
// own kind where the known finding carried the vault's name.
func TestAsk_EgressHeuristicSuspendsDistinguishably(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const key = "sk-proj-abcdefghijklmnopqrstuvwx"
	writeFile(t, filepath.Join(dir, "a.txt"), "the key is "+key)

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWith(srv.URL, &grant, ledger, nil, &knownMatcher{}, nil), // the vault knows nothing
		func(AskEvent) error { return nil })
	var want *EgressRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the egress suspension", err)
	}
	if len(want.Request.Findings) == 0 {
		t.Fatal("no findings for a recognizer-shaped value")
	}
	f0 := want.Request.Findings[0]
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
// readScreen tool's renderer-request seam fails with a secret-shaped
// message; the suspension reports the finding and that it was an error.
func TestAsk_EgressErrorStringScreened(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	requester := &recordingRequester{err: errors.New("capture failed: token sk-proj-abcdefghijklmnopqrstuvwx")}

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "session.read", args: `{"sessionId":"session-a"}`}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWith(srv.URL, &grant, ledger, nil, &knownMatcher{}, requester),
		func(AskEvent) error { return nil })
	var want *EgressRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the egress suspension (the error string was screened)", err)
	}
	if !want.Request.WasError {
		t.Fatal("the suspension does not say the findings were in an error string")
	}
	if len(want.Request.Findings) == 0 {
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
	decl, ok := mw.registry.Lookup("files.read")
	if !ok {
		t.Fatal("files.read not in the registry")
	}
	cap, capErr := decl.Narrow(grant)
	if capErr != nil {
		t.Fatalf("Narrow: %v", capErr)
	}
	ref, refErr := executors["files.read"](context.Background(), cap, []byte(args), toolSeams{})
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

// ── criterion 5: no finding ever carries the secret material ──────────────

// TestEgressRequest_NeverCarriesMaterial is acceptance criterion 5: a
// finding is kind/source/offset/name only — the value it found is the thing
// being withheld. Asserted on BOTH serializations the request travels in:
// the JSON the surface renders, and the gob form the checkpoint persists.
// Neither may contain the value or a fragment of it.
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
	for name, serialized := range map[string]func() []byte{
		"json": func() []byte {
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			return b
		},
		"gob": func() []byte {
			var buf bytes.Buffer
			if err := gob.NewEncoder(&buf).Encode(req); err != nil {
				t.Fatalf("gob encode: %v", err)
			}
			return buf.Bytes()
		},
	} {
		raw := serialized()
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s form carries the secret value itself", name)
		}
		for _, frag := range []string{"sk-proj", "abcdefghijklmnopqrstuvwx"} {
			if strings.Contains(string(raw), frag) {
				t.Errorf("%s form carries a fragment of the value (%q)", name, frag)
			}
		}
	}

	// And the gob round trip must decode — the checkpoint depends on the
	// registration in init(), and an unregistered type fails the run at
	// the suspension.
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(req); err != nil {
		t.Fatalf("gob encode: %v", err)
	}
	var back EgressRequest
	if err := gob.NewDecoder(&buf).Decode(&back); err != nil {
		t.Fatalf("gob decode: %v", err)
	}
	if len(back.Findings) != 2 || back.Findings[0].Source != EgressFindingHeuristic || back.Findings[1].SecretName != "github-token" {
		t.Fatalf("gob round trip lost the findings: %+v", back.Findings)
	}
}

// ── the batch latch on an egress suspension ───────────────────────────────

// TestAsk_EgressFindingStopsLaterCallsInTheBatch is the egress half of the
// batch-latch invariant: a finding suspends the run, and no call after it in
// that model response runs — asserted by what the ledger records: exactly
// one execution (the first call's), never a second.
func TestAsk_EgressFindingStopsLaterCallsInTheBatch(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "token: sk-proj-abcdefghijklmnopqrstuvwx")
	writeFile(t, filepath.Join(dir, "b.txt"), "clean")

	ledger := &fakeLedger{}
	f, srv := newFakeOpenAI(callThenAnswer(
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))},
		toolCallSpec{name: "files.read", args: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))},
	))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	err := cl.Ask(context.Background(),
		askParamsWith(srv.URL, &grant, ledger, nil, &knownMatcher{}, nil),
		func(AskEvent) error { return nil })
	var want *EgressRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the egress suspension", err)
	}
	if s := ledger.started(); s != 1 {
		t.Fatalf("ledger opened %d executions, want exactly 1 — the second call must not run after the finding", s)
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
	if _, err := newPolicyMiddleware(nil, grant, reg, &fakeLedger{}, NewApprovalStore(), nil, "run-1", 1, "", nil, nil, nil); err == nil {
		t.Fatal("newPolicyMiddleware accepted a run with no egress vault comparison")
	}
}
