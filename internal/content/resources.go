package content

// ResourceVerb describes what a parsed command does to one named resource.
type ResourceVerb string

const (
	ResourceRead    ResourceVerb = "read"
	ResourceWrite   ResourceVerb = "write"
	ResourceDelete  ResourceVerb = "delete"
	ResourceNetwork ResourceVerb = "network"
	ResourceExecute ResourceVerb = "execute"
	ResourceSource  ResourceVerb = "source"
	ResourceUnknown ResourceVerb = "unknown"
)

// Resource is one resource whose access was determined by the command parser.
// Path also carries non-filesystem targets such as a URL or SSH destination.
type Resource struct {
	Path string       `json:"path"`
	Verb ResourceVerb `json:"verb"`
}

// UnresolvedResource is a resource-bearing command part that cannot be
// resolved without executing shell state. Reason is intentionally retained so
// an approval surface can explain what the parser could not determine.
type UnresolvedResource struct {
	Path   string       `json:"path"`
	Verb   ResourceVerb `json:"verb"`
	Reason string       `json:"reason"`
}

// ResourceReport is the parser's structural account of resource access.
type ResourceReport struct {
	Resources  []Resource           `json:"resources,omitempty"`
	Unresolved []UnresolvedResource `json:"unresolved,omitempty"`

	// Features are semantic facts the parser established about the
	// invocation, as opposed to the resources it named. A narrowing rule
	// matches a feature rather than the spelling of a token, because -o,
	// --output, --output=file and an attached short option are the same
	// fact written four ways (ADR-0028 decision 4 is untouched: a feature
	// names a command's behaviour, never a tool).
	Features []string `json:"features,omitempty"`
}

// EffectSelection is what a resource report says about one call: the single
// row that governs it, and every effect its resources derived.
//
// One row governs — ADR-0020 §7 gives one decision per call and this does not
// reopen it — but a command that reached a host AND wrote a file did both, and
// a person answering the row is entitled to see both. Candidates carries that
// second fact out of the classification without splitting the decision.
type EffectSelection struct {
	// Effect is the row that governs. It is empty only when the declaration
	// set is empty, which means the tool reaches no class at all.
	Effect Effect
	// Candidates are the effects the report's resources derived, in the order
	// those resources appear, deduplicated. It is a COMPLETE account or it is
	// empty: a report carrying an unresolved part or a verb with no mapping
	// derives no candidates at all, because a partial list read as a whole one
	// is the dishonesty this field exists to remove. What the parser could not
	// resolve is already carried, with its human reason, in Unresolved.
	Candidates []Effect
}

// Effect selects the one class from the declaration set that governs this call.
// It is the row half of SelectEffect and never a second computation.
func (r ResourceReport) Effect(declared []Effect) Effect {
	return r.SelectEffect(declared).Effect
}

// SelectEffect selects one class from the declaration set and reports the
// candidates behind it.
//
// A report whose resources derive SEVERAL candidates takes the worst DERIVED
// candidate, not the worst DECLARED member (nocx-jxq97). `curl -o /tmp/x
// https://y` reaches a host and writes a file; the worst of those two is
// cross-boundary, which is a row the call belongs in. Taking the declaration
// set's worst instead filed it under `delegate` — "hand work to another agent",
// which it never did — because session.run declares delegate and effectOrder
// ranks it highest. That answer was strict only by accident of enum order:
// effectOrder is an evaluation lattice, not a risk ranking, so it could as
// easily have been arbitrary in the other direction.
//
// This is deliberately a WEAKENING for one configuration — delegate refused,
// cross-boundary permitted — and the trade is stated in
// TestResourceReportEffectMixedNetworkAndWriteNoLongerLandsInDelegate.
//
// Unresolved or unknown access still takes the declaration set's worst member:
// uncertainty tightens, because there is no honest candidate to name. And the
// declaration set remains the CEILING — a derived candidate the tool never
// declared is not selected, and an unrepresented class falls back to the
// declared worst rather than to a permissive result.
func (r ResourceReport) SelectEffect(declared []Effect) EffectSelection {
	if len(declared) == 0 {
		return EffectSelection{}
	}
	candidates, complete := r.derivedCandidates()
	if !complete {
		return EffectSelection{Effect: WorstEffect(declared)}
	}
	selection := EffectSelection{Candidates: candidates}
	if len(candidates) == 0 {
		if containsEffect(declared, EffectObserve) {
			selection.Effect = EffectObserve
			return selection
		}
		selection.Effect = WorstEffect(declared)
		return selection
	}
	worst := WorstEffect(candidates)
	if containsEffect(declared, worst) {
		selection.Effect = worst
		return selection
	}
	selection.Effect = WorstEffect(declared)
	return selection
}

// derivedCandidates maps every resolved resource to its effect candidate, in
// the order the resources appear, deduplicated. complete is false when the
// report carries an unresolved part or a verb with no mapping: the report is
// then not an account of what the call does but an admission that the parser
// could not determine it, and it has no candidate set to offer.
func (r ResourceReport) derivedCandidates() (candidates []Effect, complete bool) {
	if len(r.Unresolved) != 0 {
		return nil, false
	}
	for _, resource := range r.Resources {
		candidate, ok := effectForVerb(resource.Verb)
		if !ok {
			return nil, false
		}
		if !containsEffect(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, true
}

// effectForVerb is the one verb-to-class mapping. ResourceUnknown, and any verb
// added without a class, deliberately map to nothing rather than to a default:
// an unmapped verb must tighten through the declared worst, never pick a row.
func effectForVerb(verb ResourceVerb) (Effect, bool) {
	switch verb {
	case ResourceRead:
		return EffectObserve, true
	case ResourceWrite:
		return EffectMutateReversible, true
	case ResourceDelete:
		return EffectMutateDestructive, true
	case ResourceNetwork:
		return EffectCrossBoundary, true
	case ResourceExecute:
		return EffectDelegate, true
	case ResourceSource:
		// source and . change the current shell's environment, unlike
		// subprocess execution, so they use the privilege-change row.
		return EffectPrivilegeChange, true
	default:
		return "", false
	}
}

func containsEffect(effects []Effect, want Effect) bool {
	for _, effect := range effects {
		if effect == want {
			return true
		}
	}
	return false
}

func WorstEffect(effects []Effect) Effect {
	if len(effects) == 0 {
		return ""
	}
	worst := effects[0]
	for _, candidate := range effects[1:] {
		if effectOrder(candidate) > effectOrder(worst) {
			worst = candidate
		}
	}
	return worst
}

// effectOrder ranks the effect enum for WorstEffect. It is an EVALUATION
// LATTICE — it decides which single row governs when several are in play — and
// it is NOT A RISK RANKING. `delegate` is 6 and `mutate-destructive` is 2
// because of where they sit in the enum of ADR-0020 §7, not because handing
// work to another agent is more dangerous than deleting a file.
//
// So do not read a comparison here as a statement about danger, and do not add
// a rule that treats the higher number as the safer default. That reasoning is
// exactly what filed `curl -o file url` under "hand work to another agent"
// (nocx-jxq97): conservative in the ordering, and arbitrary in the product.
func effectOrder(effect Effect) int {
	switch effect {
	case EffectObserve:
		return 0
	case EffectMutateReversible:
		return 1
	case EffectMutateDestructive:
		return 2
	case EffectPrivilegeChange:
		return 3
	case EffectDisclose:
		return 4
	case EffectCrossBoundary:
		return 5
	case EffectDelegate:
		return 6
	default:
		return -1
	}
}
