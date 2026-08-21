package agenttools

import "github.com/shady2k/nocx/internal/content"

// The effect lattice is the ledger's vocabulary (ADR-0020 decision 6) —
// content.Effect lives beside content.ResourceKind in internal/content,
// because the durable grant record persists both. This package consumes it,
// never duplicates it: a grant records the effect classes it permits, the
// declaration table classifies each tool with one, and the policy evaluates
// the pair. AD-8: the ledger owns the vocabulary; a member added to
// content.Effect is a member this package sees the moment it compiles.
//
// What this package does own is the list of members it handles.
// supportedEffect's switch is the tripwire: a member a declaration uses but
// nobody has handled here fails assembly, which is how "forgot to classify
// it" stops compiling in the ledger and stops assembling here.
var allEffects = []content.Effect{
	content.EffectObserve,
	content.EffectMutateReversible,
	content.EffectMutateDestructive,
	content.EffectPrivilegeChange,
	content.EffectDisclose,
	content.EffectCrossBoundary,
	content.EffectDelegate,
}

// supportedEffect reports whether e is a member of the lattice this package
// knows how to classify. Exhaustive on purpose: the default is the failure a
// member added to the ledger but not here hits.
func supportedEffect(e content.Effect) bool {
	switch e {
	case content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose, content.EffectCrossBoundary,
		content.EffectDelegate:
		return true
	default:
		return false
	}
}
