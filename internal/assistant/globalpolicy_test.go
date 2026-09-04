package assistant

// The ONE global policy store: persistence, the fail-toward-asking load (an
// absent, empty or UNPARSEABLE document is a policy that asks — including a
// hand-edited document that tries to name a tool), and the live read the
// run mint goes through (criterion 5, asserted end to end for the
// unparseable case).

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
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

// ── one rule at a time ────────────────────────────────────────────────────

// exactRule is a rule over one literal command line: the shape a person's
// answer to a prompt saves, and the only one this file needs.
func exactRule(command ...string) content.InvocationRule {
	return content.InvocationRule{
		Selector: content.InvocationSelector{Exact: [][]string{command}},
		Decision: content.DecisionPermit,
	}
}

// seededStore is a store holding two rules and a matrix nobody would call
// default — so a write that quietly rewrote the document would be visible.
func seededStore(t *testing.T) (*GlobalPolicyStore, storage.DocumentStore, []content.InvocationRule) {
	t.Helper()
	store, doc := testPolicyStore(t, "agent-policy.json")
	if err := store.SetPolicy(content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionPermit,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
		},
		MutateDestructive: content.EffectRow{Decision: content.DecisionRefuse},
		Delegate:          content.EffectRow{Decision: content.DecisionRefuse},
	}); err != nil {
		t.Fatalf("seed matrix: %v", err)
	}
	var seeded []content.InvocationRule
	for _, command := range [][]string{{"df", "-h"}, {"uname", "-a"}} {
		saved, err := store.SetRule(exactRule(command...))
		if err != nil {
			t.Fatalf("seed rule %v: %v", command, err)
		}
		seeded = append(seeded, saved)
	}
	return store, doc, seeded
}

func ruleIDs(rules []content.InvocationRule) []string {
	ids := make([]string, 0, len(rules))
	for _, r := range rules {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestGlobalPolicyStore_SetRuleAddsAndTouchesNothingElse — criterion 1. The
// document is not a thing you rewrite to add one rule to it: every other
// rule keeps its place and every effect row is byte-for-byte what it was.
func TestGlobalPolicyStore_SetRuleAddsAndTouchesNothingElse(t *testing.T) {
	store, _, seeded := seededStore(t)
	before := store.Policy()

	third, err := store.SetRule(exactRule("free", "-m"))
	if err != nil {
		t.Fatalf("SetRule: %v", err)
	}

	after := store.Policy()
	if got, want := ruleIDs(after.Rules), append(ruleIDs(seeded), third.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("rules after the add = %v, want %v in document order", got, want)
	}
	for _, e := range []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose, content.EffectCrossBoundary,
		content.EffectDelegate,
	} {
		if got, want := after.DecisionFor(e), before.DecisionFor(e); got != want {
			t.Fatalf("row %s decides %s after adding a rule, want %s untouched", e, got, want)
		}
		if got, want := after.RowScopes(e), before.RowScopes(e); !reflect.DeepEqual(got, want) {
			t.Fatalf("row %s scopes = %v after adding a rule, want %v untouched", e, got, want)
		}
	}
}

// TestGlobalPolicyStore_SetRuleReplacesByIDInPlace — criterion 2. A rule is
// taken back and restated by its id, and restating it must not move it: a
// list that reorders itself when you edit a row is a list a person loses
// their place in, and the count is what says nothing was duplicated.
func TestGlobalPolicyStore_SetRuleReplacesByIDInPlace(t *testing.T) {
	store, _, seeded := seededStore(t)

	replacement := exactRule("df", "-k")
	replacement.ID = seeded[0].ID
	replacement.Decision = content.DecisionRefuse
	stored, err := store.SetRule(replacement)
	if err != nil {
		t.Fatalf("SetRule replace: %v", err)
	}
	if stored.ID != seeded[0].ID {
		t.Fatalf("replacement id = %q, want the id it named %q", stored.ID, seeded[0].ID)
	}

	rules := store.Policy().Rules
	if len(rules) != 2 {
		t.Fatalf("rules after a replace = %d, want 2 — a replacement is not an addition", len(rules))
	}
	if rules[0].ID != seeded[0].ID {
		t.Fatalf("rules[0] = %q, want the replaced rule to keep position 0", rules[0].ID)
	}
	if rules[0].Decision != content.DecisionRefuse ||
		!reflect.DeepEqual(rules[0].Selector.Exact, [][]string{{"df", "-k"}}) {
		t.Fatalf("rules[0] = %+v, want the replacement's selector and decision", rules[0])
	}
	if rules[1].ID != seeded[1].ID {
		t.Fatalf("rules[1] = %q, want the untouched %q", rules[1].ID, seeded[1].ID)
	}
}

// TestGlobalPolicyStore_SetRuleMintsTheIDAndRefusesAnInventedOne — criterion
// 3, both halves. Ids are server-authoritative (AD-7): a rule with none is
// given one and told which, and a rule carrying an id that names nothing is
// refused rather than quietly becoming a new rule under a name the caller
// chose.
func TestGlobalPolicyStore_SetRuleMintsTheIDAndRefusesAnInventedOne(t *testing.T) {
	store, _, _ := seededStore(t)

	minted, err := store.SetRule(exactRule("free", "-m"))
	if err != nil {
		t.Fatalf("SetRule: %v", err)
	}
	if minted.ID == "" {
		t.Fatalf("the store answered with no id; a caller that sent none has no other way to learn it")
	}
	if minted.Source != content.SourceWritten {
		t.Fatalf("minted source = %q, want written — a document IS written", minted.Source)
	}
	if minted.CreatedAt.IsZero() {
		t.Fatalf("minted rule records no creation time")
	}

	invented := exactRule("free", "-m")
	invented.ID = "a-name-the-caller-chose"
	if _, err := store.SetRule(invented); !errors.Is(err, ErrNoSuchRule) {
		t.Fatalf("SetRule with an invented id = %v, want ErrNoSuchRule", err)
	}
	if got := len(store.Policy().Rules); got != 3 {
		t.Fatalf("rules after the refusal = %d, want the 3 that were there — a refusal writes nothing", got)
	}
}

// TestGlobalPolicyStore_ForgetRuleRemovesOneAndUnknownIsNotAnError —
// criterion 4. Forgetting is by id and it is idempotent: the second forget
// is a success that says nothing was there, because "stop applying this
// rule" is already satisfied.
func TestGlobalPolicyStore_ForgetRuleRemovesOneAndUnknownIsNotAnError(t *testing.T) {
	store, doc, seeded := seededStore(t)

	removed, err := store.ForgetRule(seeded[0].ID)
	if err != nil || !removed {
		t.Fatalf("ForgetRule(%q) = %v, %v; want true, nil", seeded[0].ID, removed, err)
	}
	if got := ruleIDs(store.Policy().Rules); !reflect.DeepEqual(got, []string{seeded[1].ID}) {
		t.Fatalf("rules after the forget = %v, want only %q left", got, seeded[1].ID)
	}

	again, err := store.ForgetRule(seeded[0].ID)
	if err != nil {
		t.Fatalf("forgetting an unknown id raised %v; the rule is already not there", err)
	}
	if again {
		t.Fatalf("forgetting an unknown id reported removed=true")
	}

	// And it is on disk, not only in memory: a restart must not bring the
	// rule back.
	reloaded := NewGlobalPolicyStore(doc, "agent-policy.json")
	if got := ruleIDs(reloaded.Policy().Rules); !reflect.DeepEqual(got, []string{seeded[1].ID}) {
		t.Fatalf("reloaded rules = %v, want only %q — the forget did not reach the document", got, seeded[1].ID)
	}
}

// TestGlobalPolicyStore_SetRuleRefusesWhatTheGateRefuses: there is no second
// validator. A loose selector that permits is the asymmetry content owns
// (a hasFeature rule may never permit), and the one-rule write path is
// refused by the same gate a whole document crosses — with the document
// left exactly as it was.
func TestGlobalPolicyStore_SetRuleRefusesWhatTheGateRefuses(t *testing.T) {
	store, _, seeded := seededStore(t)

	_, err := store.SetRule(content.InvocationRule{
		Selector: content.InvocationSelector{
			HasFeature: &content.FeatureRef{Program: "tar", Feature: content.FeatureWritesOptionNamedPath},
		},
		Decision: content.DecisionPermit,
	})
	if !errors.Is(err, content.ErrPolicySyntax) {
		t.Fatalf("SetRule with a permitting hasFeature rule = %v, want the content gate's refusal", err)
	}
	if got := ruleIDs(store.Policy().Rules); !reflect.DeepEqual(got, ruleIDs(seeded)) {
		t.Fatalf("rules after the refusal = %v, want the seeded %v untouched", got, ruleIDs(seeded))
	}
}

// TestGlobalPolicyStore_SetRuleKeepsProvenanceAcrossAReplace: an edit changes
// what a rule SAYS. It cannot change that a person answered it into being,
// nor when — provenance the replacement does not restate is the stored
// rule's, so a page that edits a decision cannot silently relabel an
// answered rule as a written one.
func TestGlobalPolicyStore_SetRuleKeepsProvenanceAcrossAReplace(t *testing.T) {
	store, _ := testPolicyStore(t, "agent-policy.json")
	answered := exactRule("df", "-h")
	answered.Source = content.SourceAnswered
	saved, err := store.SetRule(answered)
	if err != nil {
		t.Fatalf("SetRule: %v", err)
	}

	edit := exactRule("df", "-h")
	edit.ID = saved.ID
	edit.Decision = content.DecisionRefuse
	replaced, err := store.SetRule(edit)
	if err != nil {
		t.Fatalf("SetRule replace: %v", err)
	}
	if replaced.Source != content.SourceAnswered {
		t.Fatalf("source after an edit = %q, want answered preserved", replaced.Source)
	}
	if !replaced.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("createdAt after an edit = %v, want the original %v", replaced.CreatedAt, saved.CreatedAt)
	}
}

// TestGlobalPolicyStore_RuleWriteFailureLeavesTheServedValueAlone: for every
// external call there is a test where that call fails. The document store
// refuses the write; the store must not serve a rule it could not persist,
// because a policy that was not written is a policy that vanishes on
// restart — and a page that saw it would report a standing answer nobody
// holds.
func TestGlobalPolicyStore_RuleWriteFailureLeavesTheServedValueAlone(t *testing.T) {
	store, _ := testPolicyStore(t, "agent-policy.json")
	seeded, err := store.SetRule(exactRule("df", "-h"))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.doc = refusingDocumentStore{inner: store.doc, err: errors.New("disk is full")}

	if _, err := store.SetRule(exactRule("uname", "-a")); err == nil {
		t.Fatalf("SetRule over a refusing document store returned no error")
	}
	if got := ruleIDs(store.Policy().Rules); !reflect.DeepEqual(got, []string{seeded.ID}) {
		t.Fatalf("rules after a failed write = %v, want only the persisted %q", got, seeded.ID)
	}
	if _, err := store.ForgetRule(seeded.ID); err == nil {
		t.Fatalf("ForgetRule over a refusing document store returned no error")
	}
	if got := ruleIDs(store.Policy().Rules); !reflect.DeepEqual(got, []string{seeded.ID}) {
		t.Fatalf("rules after a failed forget = %v, want %q still there", got, seeded.ID)
	}
}

// refusingDocumentStore reads like the real one and refuses every write.
type refusingDocumentStore struct {
	inner storage.DocumentStore
	err   error
}

func (r refusingDocumentStore) Read(name string, into any) (bool, error) {
	return r.inner.Read(name, into)
}

func (r refusingDocumentStore) Write(string, any) error { return r.err }

func (r refusingDocumentStore) Delete(name string) error { return r.inner.Delete(name) }

// TestGlobalPolicyStore_ConcurrentRuleWritesAllLand: the whole point of
// putting the read-modify-write inside the lock. Ten callers each add one
// rule at the same time; a caller that read, edited and wrote back outside
// the lock would lose most of them to last-write-wins.
func TestGlobalPolicyStore_ConcurrentRuleWritesAllLand(t *testing.T) {
	store, _ := testPolicyStore(t, "agent-policy.json")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := store.SetRule(exactRule("echo", strconv.Itoa(i))); err != nil {
				t.Errorf("SetRule %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if got := len(store.Policy().Rules); got != 10 {
		t.Fatalf("rules after ten concurrent one-rule writes = %d, want 10", got)
	}
}

// SetRowDecision is SetRule's twin (nocx-2019q): ONE row moves, and the other
// six rows, this row's scopes and every stored rule are exactly what they
// were. Before it, the approval prompt answered a non-command question by
// reading the whole policy, editing its copy and writing the document back —
// so a rule another prompt saved in between went back to not existing.
func TestGlobalPolicyStore_SetRowDecisionMovesOneRowAndTouchesNothingElse(t *testing.T) {
	store, _, seeded := seededStore(t)
	before := store.Policy()

	if err := store.SetRowDecision(content.EffectDisclose, content.DecisionRefuse); err != nil {
		t.Fatalf("SetRowDecision: %v", err)
	}

	after := store.Policy()
	if got := after.DecisionFor(content.EffectDisclose); got != content.DecisionRefuse {
		t.Fatalf("disclose = %q, want the refuse that was written", got)
	}
	if got, want := ruleIDs(after.Rules), ruleIDs(seeded); !reflect.DeepEqual(got, want) {
		t.Fatalf("rules after a ROW write = %v, want %v — a row write is not a rule write", got, want)
	}
	for _, e := range []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectCrossBoundary, content.EffectDelegate,
	} {
		if got, want := after.DecisionFor(e), before.DecisionFor(e); got != want {
			t.Fatalf("row %s decides %s after another row was written, want %s untouched", e, got, want)
		}
	}
	if got, want := after.RowScopes(content.EffectDisclose), before.RowScopes(content.EffectDisclose); !reflect.DeepEqual(got, want) {
		t.Fatalf("disclose scopes = %v after its DECISION moved, want %v untouched", got, want)
	}
}

// The row write survives a restart, like every other one: a decision that was
// not persisted is a decision that vanished.
func TestGlobalPolicyStore_SetRowDecisionPersists(t *testing.T) {
	store, doc := testPolicyStore(t, "agent-policy.json")
	if err := store.SetRowDecision(content.EffectMutateDestructive, content.DecisionRefuse); err != nil {
		t.Fatalf("SetRowDecision: %v", err)
	}
	reloaded := NewGlobalPolicyStore(doc, "agent-policy.json")
	if got := reloaded.Policy().DecisionFor(content.EffectMutateDestructive); got != content.DecisionRefuse {
		t.Fatalf("after reload mutate-destructive = %q, want the refuse that was written", got)
	}
}

// A store that cannot be written leaves the SERVED value alone — the same
// interval SetRule closes, asserted for the row seam too. A policy that
// reported a decision it failed to persist would be one the next start
// disagrees with.
func TestGlobalPolicyStore_RowWriteFailureLeavesTheServedValueAlone(t *testing.T) {
	store, _, _ := seededStore(t)
	before := store.Policy().DecisionFor(content.EffectDisclose)
	store.doc = refusingDocumentStore{inner: store.doc, err: errors.New("disk is full")}

	if err := store.SetRowDecision(content.EffectDisclose, content.DecisionRefuse); err == nil {
		t.Fatal("SetRowDecision on a store that cannot be written returned no error")
	}
	if got := store.Policy().DecisionFor(content.EffectDisclose); got != before {
		t.Fatalf("disclose = %q after a refused write, want the unchanged %q", got, before)
	}
}

// Ten row writes and ten rule writes at once, and every one of them lands.
// This is the shape two prompts and a settings page make of one document.
func TestGlobalPolicyStore_ConcurrentRowAndRuleWritesAllLand(t *testing.T) {
	store, _ := testPolicyStore(t, "agent-policy.json")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if _, err := store.SetRule(exactRule("echo", strconv.Itoa(i))); err != nil {
				t.Errorf("SetRule %d: %v", i, err)
			}
		}(i)
		go func() {
			defer wg.Done()
			if err := store.SetRowDecision(content.EffectDisclose, content.DecisionRefuse); err != nil {
				t.Errorf("SetRowDecision: %v", err)
			}
		}()
	}
	wg.Wait()

	after := store.Policy()
	if got := len(after.Rules); got != 10 {
		t.Fatalf("rules after ten rule writes racing ten row writes = %d, want 10", got)
	}
	if got := after.DecisionFor(content.EffectDisclose); got != content.DecisionRefuse {
		t.Fatalf("disclose = %q, want the refuse every row write asked for", got)
	}
}
