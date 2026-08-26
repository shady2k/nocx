package assistant

// The invocation envelope's derivation edge (nocx-d6gn4.9): which EARLIER
// invocation of this run a later invocation's arguments came out of.
//
// It exists for one question — how deep are the dependent chains our real
// tasks need — and nocx-d6gn4.10 is gated on the answer. It may never be read
// off adjacency: ADR-0019 §2 says ingest order is commit order and not
// causality, and two calls in a row are routinely independent.
//
// WHAT WE CAN AND CANNOT KNOW HERE. Under this carrier the model authors the
// arguments, so the exact fact — "I used the id I just read" — exists only
// inside the model. Recording it exactly would mean a derivedFrom field in
// every tool's schema, and that is rejected on VALIDITY rather than cost: the
// question would alter the very behaviour the experiment exists to measure,
// and a comparison whose instrument changes one arm is not a comparison. So
// what is recorded is host-observable EVIDENCE — an argument value that
// appears verbatim in an earlier result of the same run — stamped with the
// method that produced it, so a reader can tell evidence from certainty.
//
// The asymmetry this creates is written down in the bead and belongs to
// whoever scores the two cohorts: a program carrier's interpreter sees the
// edge exactly, this one infers it. Scoring both arms by the sharper method
// would hand the program carrier a win it did not earn.

import (
	"encoding/json"
	"strings"
	"sync"
)

// derivationMethod names how the edges below were arrived at. It is stored
// with every record: a bare list of ids cannot say whether it is a fact or a
// reading, and the difference is the whole point of the field.
const derivationMethod = "verbatim-argument-in-earlier-result"

// minDerivedValueLen is the shortest argument value that may stand as
// evidence. Short values collide by accident — a state, an exit code, a
// one-word command — and an edge drawn from a collision is worse than no
// edge, because it inflates exactly the number the experiment turns on.
const minDerivedValueLen = 4

// derivationLog is one RUN's completed invocations and what they returned.
// It lives for the run and is never persisted: the durable fact is the edge
// written onto the attempt, not this working set.
type derivationLog struct {
	mu      sync.Mutex
	results []recordedResult
}

// recordedResult is one completed invocation: the ledger entry it was
// recorded under, and the text the MODEL was given back — which is what a
// later argument can have been copied from. Text the model never saw cannot
// be evidence of what the model did.
type recordedResult struct {
	entryID string
	text    string
}

// record notes what one invocation returned. An empty entry id (an un-bound
// caller causes entries that belong to no turn) is not recorded: an edge
// pointing at nothing is a worse answer than no edge.
func (d *derivationLog) record(entryID, text string) {
	if d == nil || entryID == "" || text == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.results = append(d.results, recordedResult{entryID: entryID, text: text})
}

// edgesFor reads the arguments of an invocation about to run and returns the
// earlier invocations whose results contain one of those values verbatim, in
// the order those invocations completed. Distinct: an argument matching two
// values of one earlier result is still one edge.
//
// skipArg is the declaration's ResourceArg and is excluded on purpose. The
// resource is what the GRANT scoped and what the run was told; the model
// holds it before any result exists, so it can never be evidence of
// derivation — and because a result commonly echoes the resource it was asked
// about, including it would draw an edge from almost every call to almost
// every earlier one.
func (d *derivationLog) edgesFor(rawArgs, skipArg string) []string {
	if d == nil {
		return nil
	}
	values := derivableValues(rawArgs, skipArg)
	if len(values) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	var edges []string
	seen := make(map[string]bool, len(d.results))
	for _, prior := range d.results {
		if seen[prior.entryID] {
			continue
		}
		for _, v := range values {
			if strings.Contains(prior.text, v) {
				seen[prior.entryID] = true
				edges = append(edges, prior.entryID)
				break
			}
		}
	}
	return edges
}

// derivableValues is the string arguments of one call that are long enough to
// stand as evidence, minus the resource argument.
//
// STRINGS ONLY, and the omission is deliberate rather than an oversight: a
// number carried out of an earlier result (a line total, an offset) is a real
// derivation and it is also the shape that collides most — "3" occurs in
// almost any output. Numeric evidence needs a rule of its own and does not
// get one by being cheap to add here.
func derivableValues(rawArgs, skipArg string) []string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &obj); err != nil {
		return nil
	}
	var values []string
	for key, raw := range obj {
		if key == skipArg {
			continue
		}
		s, ok := raw.(string)
		if !ok || len(s) < minDerivedValueLen {
			continue
		}
		values = append(values, s)
	}
	return values
}

// derivationBlock is what goes onto the attempt payload. It is written on
// EVERY attempt, including when nothing matched: "we looked and found
// nothing" and "this record predates the field" are different facts, and a
// reader that cannot tell them apart will read the second as the first.
type derivationBlock struct {
	Method string   `json:"method"`
	Edges  []string `json:"edges"`
}

func newDerivationBlock(edges []string) derivationBlock {
	if edges == nil {
		edges = []string{}
	}
	return derivationBlock{Method: derivationMethod, Edges: edges}
}
