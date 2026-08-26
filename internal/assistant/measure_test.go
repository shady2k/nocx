package assistant

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// The measurement read (nocx-d6gn4.9 / nocx-d6gn4.10). The figure the whole
// experiment turns on is DEPENDENCY DEPTH: if our real tasks are chains of one
// or two, a carrier whose whole benefit is composing long chains has nothing to
// act on, and the epic dies on that number rather than on an argument.
//
// Depth is not the invocation count. Six independent calls are a depth of one.

func actionEntry(id, runID, tool string, edges, candidates []string, approval bool) content.LedgerEntrySummary {
	body := map[string]any{
		"tool":       tool,
		"runId":      runID,
		"descriptor": "d000",
		"derivedFrom": map[string]any{
			"method":     derivationMethod,
			"candidates": candidates,
			"edges":      edges,
		},
	}
	if approval {
		body["approval"] = map[string]any{"runId": runID, "tool": tool}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return content.LedgerEntrySummary{
		ID:      id,
		Kind:    content.EntryAction,
		Intent:  tool,
		Source:  content.SourceAssistant,
		Payload: string(payload),
	}
}

func TestMeasureRuns_DepthIsTheChainNotTheCount(t *testing.T) {
	none := []string{}
	// a → b → c: each call's arguments came out of the one before it.
	chain := []content.LedgerEntrySummary{
		actionEntry("a", "run-1", "session.list", none, none, false),
		actionEntry("b", "run-1", "session.read", []string{"a"}, []string{"a"}, false),
		actionEntry("c", "run-1", "run", []string{"b"}, []string{"a", "b"}, false),
	}
	got := MeasureRuns(chain)
	if len(got) != 1 {
		t.Fatalf("runs = %d, want 1", len(got))
	}
	if got[0].MaxDependencyDepth != 3 {
		t.Errorf("depth = %d, want 3 — a→b→c is a chain of three", got[0].MaxDependencyDepth)
	}
	if got[0].Invocations != 3 {
		t.Errorf("invocations = %d, want 3", got[0].Invocations)
	}
	if got[0].Edges != 2 {
		t.Errorf("edges = %d, want 2", got[0].Edges)
	}
}

// TestMeasureRuns_IndependentCallsAreDepthOne is the half that decides the
// epic: a run of six calls that owe each other nothing is a FLAT run, and
// reporting it as depth six would fund a carrier for work nobody does.
func TestMeasureRuns_IndependentCallsAreDepthOne(t *testing.T) {
	none := []string{}
	flat := []content.LedgerEntrySummary{
		actionEntry("a", "run-1", "session.list", none, none, false),
		actionEntry("b", "run-1", "files.read", none, []string{"a"}, false),
		actionEntry("c", "run-1", "git.status", none, []string{"a", "b"}, false),
	}
	got := MeasureRuns(flat)
	if len(got) != 1 {
		t.Fatalf("runs = %d, want 1", len(got))
	}
	if got[0].MaxDependencyDepth != 1 {
		t.Errorf("depth = %d, want 1 — three calls, no dependency", got[0].MaxDependencyDepth)
	}
	if got[0].Candidates != 3 {
		t.Errorf("candidates = %d, want 3 — the denominator that says the run HAD prior results and used none", got[0].Candidates)
	}
}

// TestMeasureRuns_ADiamondIsNotCountedTwice: two calls both derived from the
// same first call, and a third from both. The depth is 3, not 4 — a longest
// path, never a sum of edges.
func TestMeasureRuns_ADiamondIsMeasuredByItsLongestPath(t *testing.T) {
	none := []string{}
	diamond := []content.LedgerEntrySummary{
		actionEntry("a", "run-1", "session.list", none, none, false),
		actionEntry("b", "run-1", "session.read", []string{"a"}, []string{"a"}, false),
		actionEntry("c", "run-1", "session.read", []string{"a"}, []string{"a", "b"}, false),
		actionEntry("d", "run-1", "run", []string{"b", "c"}, []string{"a", "b", "c"}, false),
	}
	got := MeasureRuns(diamond)
	if got[0].MaxDependencyDepth != 3 {
		t.Errorf("depth = %d, want 3", got[0].MaxDependencyDepth)
	}
	// Four EDGES (b←a, c←a, d←b, d←c) and a depth of three: the count of
	// edges and the length of the longest chain are different questions, and
	// conflating them is how a fork gets reported as a chain.
	if got[0].Edges != 4 {
		t.Errorf("edges = %d, want 4", got[0].Edges)
	}
}

func TestMeasureRuns_SeparatesRunsAndCountsApprovals(t *testing.T) {
	none := []string{}
	entries := []content.LedgerEntrySummary{
		actionEntry("a", "run-1", "run", none, none, true),
		actionEntry("b", "run-1", "session.list", none, []string{"a"}, false),
		actionEntry("c", "run-2", "files.read", none, none, false),
	}
	got := MeasureRuns(entries)
	if len(got) != 2 {
		t.Fatalf("runs = %d, want 2", len(got))
	}
	byID := map[string]RunMeasurement{}
	for _, m := range got {
		byID[m.RunID] = m
	}
	if byID["run-1"].ApprovalsAsked != 1 {
		t.Errorf("run-1 approvals = %d, want 1", byID["run-1"].ApprovalsAsked)
	}
	if byID["run-2"].Invocations != 1 {
		t.Errorf("run-2 invocations = %d, want 1", byID["run-2"].Invocations)
	}
}

// TestMeasureRuns_ReportsTheDescriptorsSeen is the cohort-integrity check: two
// different descriptor digests inside one comparison means the tool changed
// underneath it, and the comparison is of two different things.
func TestMeasureRuns_ReportsTheDescriptorsSeen(t *testing.T) {
	none := []string{}
	a := actionEntry("a", "run-1", "session.list", none, none, false)
	b := actionEntry("b", "run-1", "session.list", none, []string{"a"}, false)
	b.Payload = replaceDescriptor(t, b.Payload, "d999")

	got := MeasureRuns([]content.LedgerEntrySummary{a, b})
	if len(got[0].Descriptors) != 2 {
		t.Fatalf("descriptors = %v, want both seen", got[0].Descriptors)
	}
}

func replaceDescriptor(t *testing.T, payload, digest string) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatal(err)
	}
	body["descriptor"] = digest
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
