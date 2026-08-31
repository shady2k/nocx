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

// Effect selects one class from the declaration set. Unresolved, unknown or
// mixed access takes the set's worst member; a resolved report selects its
// mapped member only when that member is declared. This keeps uncertainty
// tightening and never turns an unrepresented class into a permissive result.
func (r ResourceReport) Effect(declared []Effect) Effect {
	if len(declared) == 0 {
		return ""
	}
	if len(r.Unresolved) != 0 {
		return WorstEffect(declared)
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
			return WorstEffect(declared)
		}
		if derived == "" {
			derived = candidate
			continue
		}
		if derived != candidate {
			return WorstEffect(declared)
		}
	}
	if derived == "" {
		if containsEffect(declared, EffectObserve) {
			return EffectObserve
		}
		return WorstEffect(declared)
	}
	if containsEffect(declared, derived) {
		return derived
	}
	return WorstEffect(declared)
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
	worst := effects[0]
	for _, candidate := range effects[1:] {
		if effectOrder(candidate) > effectOrder(worst) {
			worst = candidate
		}
	}
	return worst
}

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
