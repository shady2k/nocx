package content

// EffectPolicy.WithRowWrite and ChangedByRowWrite: what ONE edit to the seven
// effect rows would change about what a policy decides.
//
// It exists for the runs already in flight, for the reason rulewrite.go states
// once and this file does not restate: a run's grant is minted from the policy
// as it was when the run started and is immutable for the run (ADR-0020
// decision 5), so an answer taken back on the settings page does not reach it,
// and the person who took it back is owed the number of live runs still
// deciding under the old answer. The rule half of that question is
// ChangedByRuleWrite; this is the row half, and internal/transport's
// runsUnreachedByRowWrite is the one caller.
//
// WHY IT IS NOT ChangedByRuleWrite WIDENED. A rule write names a SELECTOR, so
// the invocations it can possibly change are bounded by that selector, and
// ChangedByRuleWrite probes one representative invocation per selector. A row
// write names no selector at all. Its subject is every invocation that
// classifies as its effect, which is not a set anything here can enumerate —
// so the probe is not the shape of this question, and a function that borrowed
// it would be answering about the two command lines it happened to invent.
//
// WHAT THE QUESTION IS INSTEAD. Read EvaluateInvocation's composition order:
// for ONE effect the evaluator reads exactly four things — that effect row's
// decision, the rules, that effect row's scopes, and the fence. A matrix write
// moves rows and nothing else. So if the two authorities state the same row
// for an effect, there is no invocation under that effect they can answer
// apart; and if they state a different decision, they answer apart about every
// command line no standing answer speaks about, which is never an empty set
// because a selector is always bounded to one command word. Comparing the rows
// IS comparing the decisions, exactly, and no probe can be more precise than
// exact. (Both directions of that claim are asserted against the evaluator
// itself, in TestARowWriteThatMovesNoRowMovesNoDecision and its twin — the
// argument is in a test rather than in a second implementation here.)
//
// THAT ARGUMENT HAS ONE PRECONDITION, and it is the caller's to meet: the
// policy passed in must be THIS RUN'S OWN AUTHORITY RE-MINTED from the
// document the write leaves behind, not the document itself. A run's rows have
// had the session overlay resolved into them and the run fence folded into
// them (ResolvePolicy, WithRunScopes), so comparing a minted row against a
// stored one would report every fence-narrowed run as changed by a write that
// moved nothing. Minting is the transport's — it owns the run fence — which is
// why WithRowWrite is exported and this comparison takes a whole policy.
//
// HOW PRECISE THE ANSWER IS, said plainly. It is exact, and exactness does not
// make it small: a row is global, so a matrix write that moves one really does
// reach every live run whose authority still states the old row. What it
// correctly counts OUT is a run carrying no grant, a run whose session
// overlay already answers that effect, a run whose fence had already refused
// it, a run minted after somebody else made the same change, and every write
// that states what the rows already say. What it cannot do is what the rule
// count does — bound the question to a handful of command lines — because a
// row has no such bound to offer. The transport says so on screen rather than
// dressing a global number as a narrow one; runsUnreachedByRowWrite carries
// that sentence.

// WithRowWrite returns the document ONE policy.set leaves behind: the seven
// rows the write states, over everything else this document holds.
//
// The standing answers are the whole reason it is a merge rather than a
// replacement. A matrix write may not name rules at all (policySetNamesRules),
// because a whole-document write made against a document read a minute ago
// deleted every answer a person had approved once already (nocx-39bly). This
// keeps them, and GlobalPolicyStore.SetPolicy keeps them the same way under
// its own lock — TestPolicySet_TheDocumentCountedAgainstIsTheDocumentStored is
// what holds the two together, because the count is only worth anything if it
// was taken against the document the store ends up holding.
//
// It is EXPORTED where withRuleWrite is not, and the difference is the caller.
// A rule write's "after" never leaves this package: the comparison is made
// against a frozen grant and reaches no store. A row write's "after" has to be
// carried to the run mint, which lives in internal/transport with the run
// fence, so it must have a name outside this file. It is still not a second
// answer to "what does the DOCUMENT become" — the store's write is the answer,
// and the test above is what says so.
func (p EffectPolicy) WithRowWrite(matrix EffectPolicy) EffectPolicy {
	out := p
	for _, e := range latticeEffects {
		out.setRow(e, matrix.rowFor(e))
	}
	return out
}

// ChangedByRowWrite reports whether this authority and the one a matrix write
// would mint in its place decide differently about anything.
//
// `after` is THIS RUN'S authority re-minted, not the stored document — see the
// precondition at the top of this file. The comparison is per effect and stops
// at the first row that moved, because one moved row is already the whole
// answer.
func (p EffectPolicy) ChangedByRowWrite(after EffectPolicy) bool {
	for _, e := range latticeEffects {
		if p.rowMoved(e, after) {
			return true
		}
	}
	return false
}

// RowsMovedByRowWrite names the effects whose rows the two state differently,
// in the lattice's order. It is ChangedByRowWrite's own predicate asked for
// every row instead of stopping at the first, so the sentence a stopped run
// records cannot name a different set of rows from the one the count was taken
// over.
func (p EffectPolicy) RowsMovedByRowWrite(after EffectPolicy) []Effect {
	var out []Effect
	for _, e := range latticeEffects {
		if p.rowMoved(e, after) {
			out = append(out, e)
		}
	}
	return out
}

// rowMoved is the whole comparison, for ONE effect: the answer, and the places
// it applies within. Both, because the row states both and the evaluator reads
// both — the decision at the effect layer and the scopes at the resource one.
func (p EffectPolicy) rowMoved(e Effect, after EffectPolicy) bool {
	if p.DecisionFor(e) != after.DecisionFor(e) {
		return true
	}
	return !sameScopeSet(p.rowFor(e).Scopes, after.rowFor(e).Scopes)
}

// sameScopeSet compares two rows' places as a SET — order and repetition carry
// no meaning to a row, and the settings page rewrites the list whole, so a
// person who takes a place off and puts it back must not be told their runs
// are affected by a row that says exactly what it said before.
//
// It compares scopes BY VALUE and deliberately not by containment. Two sets
// that bound the same resources without being equal — `[/home]` beside
// `[/home, /home/x]` — are reported as a change, and that is the one place
// this answer is conservative rather than exact. The alternative is a
// containment-equivalence of two scope sets, which is the resource layer's own
// reasoning (firstOutside, GrantScope.Contains) written a second time to
// answer a question nothing asks: no gesture on the settings page can produce
// such a pair, because a row's places are picked from a fixed list of
// disjoint choices. One owner for containment, and an erring here that can
// only ever ask a person a question they can answer.
func sameScopeSet(a, b []GrantScope) bool {
	return scopeSetCovers(a, b) && scopeSetCovers(b, a)
}

// scopeSetCovers reports that every member of a appears in b.
func scopeSetCovers(a, b []GrantScope) bool {
	for _, want := range a {
		found := false
		for _, have := range b {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
