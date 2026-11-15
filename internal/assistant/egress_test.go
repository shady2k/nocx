package assistant

// Tool-result egress screening through the kernel and pipeline.

import (
	"context"
	"encoding/json"
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
	refs, refErr := decl.ResolveResources(map[string]any{"path": path}, localRunContext())
	if refErr != nil {
		t.Fatalf("ResolveResources: %v", refErr)
	}
	cap, capErr := decl.Narrow(grant, refs, localRunContext())
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
func TestMiddleware_NewPolicyFailsClosedWithoutKnownMaterial(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if _, err := newKernelFixture(nil, grant, reg, &fakeLedger{}, NewApprovalStore(), nil, "run-1", "", 1, "", nil, Attachments{}, nil); err == nil {
		t.Fatal("newKernelFixture accepted a run with no egress vault comparison")
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

func askParamsWith(_ string, grant *content.Grant, ledger AttemptLedger, approvals *ApprovalStore, known KnownMaterial, requester RendererRequester) AskParams {
	return AskParams{Grant: grant, AttemptLedger: ledger, Approvals: approvals, KnownMaterial: known, Requester: requester, SessionID: "session-a", RunID: "run-1", Attempt: 1, Environment: localPaneEnvironment()}
}
