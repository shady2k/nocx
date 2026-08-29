package capability

import (
	"fmt"
	"strings"
)

// DispositionKind says whether an operation can be projected into the
// assistant surface directly, after an assistant-owned adaptation, or not at
// all. The zero value is deliberately invalid: omitting the disposition must
// be caught before an operation can be offered to the assistant.
type DispositionKind string

const (
	DispositionDirect   DispositionKind = "direct"
	DispositionAdapted  DispositionKind = "adapted"
	DispositionExcluded DispositionKind = "excluded"
)

// Disposition is the assistant projection contract carried by every
// capability operation. Direct operations carry metadata for the future
// projection; adapted operations name that projection and explain the seam it
// must own; excluded operations explain why no projection exists.
type Disposition struct {
	Kind               DispositionKind
	Metadata           string
	AgentOperationName string
	Reason             string
}

// Direct marks an operation whose callback has no transport-owned dependency.
func Direct(metadata string) Disposition {
	return Disposition{Kind: DispositionDirect, Metadata: metadata}
}

// Adapted marks an operation that needs an assistant-owned façade before it is
// exposed as an assistant action.
func Adapted(agentOperationName, reason string) Disposition {
	return Disposition{
		Kind:               DispositionAdapted,
		AgentOperationName: agentOperationName,
		Reason:             reason,
	}
}

// Excluded marks an operation that must not be projected into the assistant.
func Excluded(reason string) Disposition {
	return Disposition{Kind: DispositionExcluded, Reason: reason}
}

// Validate refuses an omitted or malformed disposition. The variant-specific
// fields are checked here so a row cannot carry a kind while silently omitting
// the data its consumer needs.
func (d Disposition) Validate() error {
	switch d.Kind {
	case DispositionDirect:
		if strings.TrimSpace(d.Metadata) == "" {
			return fmt.Errorf("direct disposition has no metadata")
		}
		if strings.TrimSpace(d.AgentOperationName) != "" || strings.TrimSpace(d.Reason) != "" {
			return fmt.Errorf("direct disposition has adapted or excluded fields")
		}
	case DispositionAdapted:
		if strings.TrimSpace(d.AgentOperationName) == "" {
			return fmt.Errorf("adapted disposition has no agent operation name")
		}
		if strings.TrimSpace(d.Reason) == "" {
			return fmt.Errorf("adapted disposition has no reason")
		}
		if strings.TrimSpace(d.Metadata) != "" {
			return fmt.Errorf("adapted disposition has direct metadata")
		}
	case DispositionExcluded:
		if strings.TrimSpace(d.Reason) == "" {
			return fmt.Errorf("excluded disposition has no reason")
		}
		if strings.TrimSpace(d.Metadata) != "" || strings.TrimSpace(d.AgentOperationName) != "" {
			return fmt.Errorf("excluded disposition has direct or adapted fields")
		}
	default:
		return fmt.Errorf("unsupported or missing disposition kind %q", d.Kind)
	}
	return nil
}

// AssistantOperation is the common inspection surface for every operation in
// this package. Keeping the accessor on the operation itself means a façade
// does not need to infer ownership from the transport binding that called it.
type AssistantOperation interface {
	Disposition() Disposition
}
