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
}

// Effect derives the safest effect from this report without exceeding the
// tool's declared ceiling. Unresolved access retains that ceiling; a resolved
// report with one known verb may choose its mapped row only when that row is
// explicitly below the declaration. Mixed verbs remain declared because this
// lattice has no implicit total ordering for combining unlike effects.
func (r ResourceReport) Effect(declared Effect) Effect {
	if len(r.Unresolved) != 0 {
		return declared
	}

	derived := Effect("")
	for _, resource := range r.Resources {
		var candidate Effect
		switch resource.Verb {
		case ResourceRead:
			candidate = EffectObserve
		case ResourceWrite:
			candidate = EffectMutateReversible
		case ResourceDelete:
			candidate = EffectMutateDestructive
		case ResourceNetwork:
			candidate = EffectCrossBoundary
		case ResourceExecute:
			candidate = EffectDelegate
		case ResourceSource:
			// source and . change the current shell's environment, unlike
			// subprocess execution, so they use the privilege-change row.
			candidate = EffectPrivilegeChange
		default:
			return declared
		}
		if derived == "" {
			derived = candidate
			continue
		}
		if derived != candidate {
			return declared
		}
	}
	if derived == "" {
		return EffectObserve
	}
	if !effectBelowCeiling(declared, derived) {
		return declared
	}
	return derived
}

// effectBelowCeiling states the only safe lowering relations. Observe is
// already the historical lowering for a read-only report; every other
// relation is named instead of inferred from the effect strings' order.
func effectBelowCeiling(declared, derived Effect) bool {
	if derived == EffectObserve {
		return true
	}
	switch declared {
	case EffectMutateReversible:
		return derived == EffectMutateReversible
	case EffectMutateDestructive:
		return derived == EffectMutateReversible || derived == EffectMutateDestructive
	case EffectPrivilegeChange:
		return derived == EffectPrivilegeChange
	case EffectDisclose:
		return derived == EffectPrivilegeChange || derived == EffectDisclose
	case EffectCrossBoundary:
		return derived == EffectPrivilegeChange || derived == EffectDisclose ||
			derived == EffectCrossBoundary
	case EffectDelegate:
		return derived == EffectMutateReversible || derived == EffectMutateDestructive ||
			derived == EffectPrivilegeChange || derived == EffectDisclose ||
			derived == EffectCrossBoundary || derived == EffectDelegate
	default:
		return false
	}
}
