package content

// ResourceVerb describes what a parsed command does to one named resource.
type ResourceVerb string

const (
	ResourceRead    ResourceVerb = "read"
	ResourceWrite   ResourceVerb = "write"
	ResourceDelete  ResourceVerb = "delete"
	ResourceNetwork ResourceVerb = "network"
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

// Effect derives the safest lowerable effect from this report. A report with
// only reads can lower the declared worst case; writes, deletes, network
// access and every unresolved part retain it.
func (r ResourceReport) Effect(declared Effect) Effect {
	if len(r.Unresolved) != 0 {
		return declared
	}
	for _, resource := range r.Resources {
		if resource.Verb != ResourceRead {
			return declared
		}
	}
	return EffectObserve
}
