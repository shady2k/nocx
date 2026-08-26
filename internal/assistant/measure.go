package assistant

// The measurement read (nocx-d6gn4.9, consumed by nocx-d6gn4.10): the figures
// the program-carrier experiment is decided on, computed from what the ledger
// already recorded rather than from a second instrumentation pass.
//
// THE NUMBER THAT DECIDES THE EPIC IS DEPTH, and it is not the invocation
// count. Six calls that owe each other nothing are a flat run: a carrier whose
// entire benefit is composing long dependent chains has nothing to act on
// there, and reporting six would fund it for work nobody does. Depth is the
// LONGEST PATH through the derivation edges — a diamond is three deep, not
// four, because two branches of one fork are not a longer chain.
//
// Candidates are reported beside edges on purpose: "no dependency" is two
// different facts, and a run with one call has none because there was nothing
// to depend on. Only the second — prior results available and none used — is
// evidence about our tasks.
//
// This reads through the ledger's own query (content.QueryEntries), never a
// second SQL path: one owner for what an entry is.

import (
	"encoding/json"
	"sort"

	"github.com/shady2k/nocx/internal/content"
)

// RunMeasurement is one run's figures.
type RunMeasurement struct {
	RunID string
	// Invocations is every recorded tool call of the run, escalations
	// included: a proposal that was asked about is a call the task needed.
	Invocations int
	// ApprovalsAsked is how many of them stopped to ask a person. Prompt
	// fatigue is a safety failure rather than a UX cost (nocx-d6gn4.7), so
	// this is a first-class figure and not a footnote.
	ApprovalsAsked int
	// Edges is the count of derivation edges recorded across the run, and
	// Candidates the count of prior results those calls were checked
	// against — the denominator without which Edges cannot be read.
	Edges      int
	Candidates int
	// MaxDependencyDepth is the longest chain of dependent calls. One means
	// every call stood alone.
	MaxDependencyDepth int
	// Descriptors is every distinct tool version seen in the run, sorted.
	// More than one digest for one tool inside a comparison means the tool
	// changed underneath it and the cohorts are not comparable.
	Descriptors []string
}

// envelopeRecord is the part of an action entry's payload this read needs.
type envelopeRecord struct {
	RunID       string `json:"runId"`
	Descriptor  string `json:"descriptor"`
	DerivedFrom *struct {
		Candidates []string `json:"candidates"`
		Edges      []string `json:"edges"`
	} `json:"derivedFrom"`
	// Approval is present exactly on an entry that ASKED somebody
	// (recordProposal writes it; a permitted call's attempt does not).
	Approval map[string]any `json:"approval"`
}

// MeasureRuns groups action entries by the run they happened in and reports
// each run's figures. Entries that are not the assistant's tool calls, and
// entries whose payload predates the envelope, are skipped rather than
// counted as flat: a record that cannot answer must not be read as a record
// that answered zero.
func MeasureRuns(entries []content.LedgerEntrySummary) []RunMeasurement {
	type runState struct {
		m           RunMeasurement
		edgesByID   map[string][]string
		descriptors map[string]bool
		order       []string
	}
	runs := map[string]*runState{}
	var order []string

	for _, e := range entries {
		if e.Kind != content.EntryAction || e.Source != content.SourceAssistant {
			continue
		}
		var rec envelopeRecord
		if err := json.Unmarshal([]byte(e.Payload), &rec); err != nil {
			continue
		}
		// No derivation block at all means the entry predates the envelope.
		// Counting it would report a chain of one for a call whose
		// dependencies were never looked for.
		if rec.DerivedFrom == nil {
			continue
		}
		st, ok := runs[rec.RunID]
		if !ok {
			st = &runState{
				m:           RunMeasurement{RunID: rec.RunID},
				edgesByID:   map[string][]string{},
				descriptors: map[string]bool{},
			}
			runs[rec.RunID] = st
			order = append(order, rec.RunID)
		}
		st.m.Invocations++
		if rec.Approval != nil {
			st.m.ApprovalsAsked++
		}
		st.m.Edges += len(rec.DerivedFrom.Edges)
		st.m.Candidates += len(rec.DerivedFrom.Candidates)
		st.edgesByID[e.ID] = rec.DerivedFrom.Edges
		st.order = append(st.order, e.ID)
		if rec.Descriptor != "" {
			st.descriptors[rec.Descriptor] = true
		}
	}

	out := make([]RunMeasurement, 0, len(runs))
	for _, id := range order {
		st := runs[id]
		st.m.MaxDependencyDepth = longestChain(st.edgesByID, st.order)
		for d := range st.descriptors {
			st.m.Descriptors = append(st.m.Descriptors, d)
		}
		sort.Strings(st.m.Descriptors)
		out = append(out, st.m)
	}
	return out
}

// longestChain is the longest path through the derivation edges, in
// invocations. An edge naming an entry this run does not hold contributes
// nothing — an unresolvable edge is not evidence of a longer chain.
func longestChain(edges map[string][]string, ids []string) int {
	depth := make(map[string]int, len(ids))
	// visiting guards against a cycle. Edges only ever point backwards in
	// time, so a cycle means the data is malformed — which is a reason to
	// stop, not to hang.
	visiting := make(map[string]bool, len(ids))

	var depthOf func(id string) int
	depthOf = func(id string) int {
		if d, ok := depth[id]; ok {
			return d
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		best := 0
		for _, prior := range edges[id] {
			if _, known := edges[prior]; !known {
				continue
			}
			if d := depthOf(prior); d > best {
				best = d
			}
		}
		visiting[id] = false
		depth[id] = best + 1
		return depth[id]
	}

	longest := 0
	for _, id := range ids {
		if d := depthOf(id); d > longest {
			longest = d
		}
	}
	return longest
}
