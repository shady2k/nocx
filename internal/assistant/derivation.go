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

// minDerivedTokenLen is the shortest token that may stand as evidence.
//
// THE BIAS IS DELIBERATE AND IT IS TOWARDS MISSING EDGES. The two errors are
// not symmetric: a missed edge understates dependency depth, while a spurious
// one INFLATES exactly the figure the experiment is decided on, and would fund
// a carrier on a number nobody could later reproduce. Six characters keeps file
// names, paths and identifiers and drops the words every command line and every
// output share — ls, cat, grep, total, exit.
const minDerivedTokenLen = 6

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
	// callID is the MODEL's id for the invocation. The ledger joins on the
	// entry id; the renderer keys its rows on the call id, so both are kept
	// rather than resolved twice from one of them.
	callID string
	text   string
}

// Derivation is one invocation's provenance, in both vocabularies at once:
// entry ids for the ledger record, call ids for the surface that draws it.
type Derivation struct {
	CandidateEntries []string
	Edges            []string
	// EdgeCalls is Edges in the renderer's vocabulary, same order.
	EdgeCalls []string
}

// record notes what one invocation returned. An empty entry id (an un-bound
// caller causes entries that belong to no turn) is not recorded: an edge
// pointing at nothing is a worse answer than no edge.
func (d *derivationLog) record(entryID, callID, text string) {
	if d == nil || entryID == "" || text == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.results = append(d.results, recordedResult{entryID: entryID, callID: callID, text: text})
}

// check reads the arguments of an invocation about to run and returns two
// things: every earlier invocation of this run whose result was AVAILABLE to be
// copied from (the candidates), and the subset whose result actually contains
// one of those argument values verbatim (the edges), each in the order those
// invocations completed. Distinct: an argument matching two values of one
// earlier result is still one edge.
//
// The candidates are recorded because "no edge" is two different facts. A run
// with one call has no dependency because there was nothing to depend on; a run
// with six calls and no edges is a genuinely flat run. A reader given only the
// edges reads the first as the second, and the number nocx-d6gn4.10 is gated on
// is exactly that distinction.
//
// skipArg is the declaration's ResourceArg and is excluded on purpose. The
// resource is what the GRANT scoped and what the run was told; the model
// holds it before any result exists, so it can never be evidence of
// derivation — and because a result commonly echoes the resource it was asked
// about, including it would draw an edge from almost every call to almost
// every earlier one.
func (d *derivationLog) check(rawArgs, skipArg string) Derivation {
	var out Derivation
	if d == nil {
		return out
	}
	values := derivableValues(rawArgs, skipArg)

	d.mu.Lock()
	defer d.mu.Unlock()

	matched := make(map[string]bool, len(d.results))
	listed := make(map[string]bool, len(d.results))
	for _, prior := range d.results {
		if !listed[prior.entryID] {
			listed[prior.entryID] = true
			out.CandidateEntries = append(out.CandidateEntries, prior.entryID)
		}
		if matched[prior.entryID] {
			continue
		}
		for _, v := range values {
			if strings.Contains(prior.text, v) {
				matched[prior.entryID] = true
				out.Edges = append(out.Edges, prior.entryID)
				out.EdgeCalls = append(out.EdgeCalls, prior.callID)
				break
			}
		}
	}
	return out
}

// derivableValues is the TOKENS of one call's string arguments that are long
// enough to stand as evidence, minus the resource argument.
//
// TOKENS AND NOT WHOLE VALUES, and this was found the hard way against a real
// session (nocx-d6gn4.9, 2026-08-26). The first implementation compared the
// whole argument value, which works for session.read's `id` and can never work
// for run's `command`: a complete command line does not appear inside a
// previous command's output. So for the one tool the experiment cares about
// most, every edge was empty — and an instrument that cannot see a dependency
// reports the same thing as a task that has none. Blindness and independence
// must not look alike.
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
	seen := map[string]bool{}
	for key, raw := range obj {
		if key == skipArg {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			continue
		}
		for _, tok := range strings.FieldsFunc(s, isTokenBreak) {
			// A flag is vocabulary, not a value: -l and --recursive say
			// nothing about where the argument came from.
			if strings.HasPrefix(tok, "-") {
				continue
			}
			// The token AND its path segments, because the two halves of a
			// derivation rarely agree on shape: `ls /var/log` prints base
			// names, and the model then writes the whole path. Matching only
			// the whole token misses every file discovered by listing its
			// directory — which is the commonest dependent pair there is.
			add(&values, seen, tok)
			if strings.ContainsRune(tok, '/') {
				for _, seg := range strings.Split(tok, "/") {
					add(&values, seen, seg)
				}
			}
		}
	}
	return values
}

// add keeps a token if it is long enough to be evidence and not already held.
func add(values *[]string, seen map[string]bool, tok string) {
	if len(tok) < minDerivedTokenLen || seen[tok] {
		return
	}
	seen[tok] = true
	*values = append(*values, tok)
}

// isTokenBreak splits an argument the way a reader would: on whitespace and on
// the shell punctuation that joins commands, so a path stays whole while the
// operators around it fall away.
func isTokenBreak(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', '|', '&', ';', '(', ')', '<', '>', '$', '{', '}', '=', ',':
		return true
	}
	return false
}

// derivationBlock is what goes onto the attempt payload. It is written on
// EVERY attempt, including when nothing matched: "we looked and found
// nothing" and "this record predates the field" are different facts, and a
// reader that cannot tell them apart will read the second as the first.
type derivationBlock struct {
	Method string `json:"method"`
	// Candidates is every earlier invocation of this run whose result the
	// model could have copied from — an exact upper bound on what this call
	// may derive from, and the denominator that makes Edges readable.
	Candidates []string `json:"candidates"`
	Edges      []string `json:"edges"`
}

func newDerivationBlock(d Derivation) derivationBlock {
	candidates, edges := d.CandidateEntries, d.Edges
	if candidates == nil {
		candidates = []string{}
	}
	if edges == nil {
		edges = []string{}
	}
	return derivationBlock{Method: derivationMethod, Candidates: candidates, Edges: edges}
}
