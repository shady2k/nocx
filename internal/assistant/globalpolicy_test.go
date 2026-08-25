package assistant

// The ONE global policy store: persistence, the fail-toward-asking load (an
// absent, empty or UNPARSEABLE document is a policy that asks — including a
// hand-edited document that tries to name a tool), and the live read the
// run mint goes through (criterion 5, asserted end to end for the
// unparseable case).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
)

func testPolicyStore(t *testing.T, docName string) (*GlobalPolicyStore, storage.DocumentStore) {
	t.Helper()
	doc := storage.NewDocumentStore(t.TempDir())
	return NewGlobalPolicyStore(doc, docName), doc
}

func TestGlobalPolicyStore_AbsentDocumentAsks(t *testing.T) {
	store, _ := testPolicyStore(t, "agent-policy.json")
	// Absent — the fresh-install state — is the zero matrix: asks.
	if got := store.Policy().DecisionFor(content.EffectObserve); got != content.DecisionAsk {
		t.Fatalf("absent document policy decides observe = %s, want ask", got)
	}
}

func TestGlobalPolicyStore_UnparseableDocumentsAsk(t *testing.T) {
	// Criterion 5, unparseable asserted through the actual load path: a
	// corrupt document, and a document that names a TOOL as a policy key
	// (the criterion-3 impossibility, enforced at the store's door).
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "agent-policy.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt doc: %v", err)
	}
	corrupt := NewGlobalPolicyStore(doc, "agent-policy.json")
	if got := corrupt.Policy().DecisionFor(content.EffectObserve); got != content.DecisionAsk {
		t.Fatalf("corrupt document decides observe = %s, want ask", got)
	}

	dir2 := t.TempDir()
	doc2 := storage.NewDocumentStore(dir2)
	if err := os.WriteFile(filepath.Join(dir2, "agent-policy.json"), []byte(`{"readScreen":{"decision":"permit"}}`), 0o600); err != nil {
		t.Fatalf("write tool-named doc: %v", err)
	}
	toolNamed := NewGlobalPolicyStore(doc2, "agent-policy.json")
	if got := toolNamed.Policy().DecisionFor(content.EffectObserve); got != content.DecisionAsk {
		t.Fatalf("a tool-named document parsed into a policy (observe = %s, want ask)", got)
	}
}

func TestGlobalPolicyStore_UnparseableDocumentRunAsks(t *testing.T) {
	// The unparseable end of criterion 5 through the pipeline: a corrupt
	// document loads as the zero matrix, the mint AsGrant's it with the
	// run's bound, and an in-scope real tool call is ASKED — never
	// permitted, and never refused out from under a person.
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "agent-policy.json"), []byte(`{"observe":{"decision":"permit"}`), 0o600); err != nil {
		t.Fatalf("write truncated doc: %v", err)
	}
	store := NewGlobalPolicyStore(doc, "agent-policy.json")
	runDir := t.TempDir()
	grant := store.Policy().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: runDir}})
	tool := filesReadTool(t)
	if got := decideOutcome(t, grant, tool, map[string]any{"path": filepath.Join(runDir, "a.txt")}); got != policyAsk {
		t.Fatalf("run under an unparseable policy = %v, want ask", got)
	}
}

func TestGlobalPolicyStore_SetPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	const name = "agent-policy.json"
	store := NewGlobalPolicyStore(doc, name)

	var p content.EffectPolicy
	home := "/home/someone"
	p.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: home}},
	}
	if err := store.SetPolicy(p); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	// The live read reflects the set — the run mint sees the new policy
	// without a restart.
	if got := store.Policy().DecisionFor(content.EffectObserve); got != content.DecisionPermit {
		t.Fatalf("live policy decides observe = %s, want permit after Set", got)
	}
	// A fresh store over the same document reloads it: the policy survived.
	reloaded := NewGlobalPolicyStore(doc, name)
	if got := reloaded.Policy().DecisionFor(content.EffectObserve); got != content.DecisionPermit {
		t.Fatalf("reloaded policy decides observe = %s, want permit", got)
	}
}
