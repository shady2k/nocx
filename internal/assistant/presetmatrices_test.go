package assistant

// The three presets of the original ADR-0020 §7, expressed AS MATRICES —
// which is the whole contract: the presets remain expressible in the new
// form, and every test that uses one asserts the matrix a person would
// write behaves exactly as the preset did. There are deliberately no
// production constructors (clean-only, no dead vocabulary); the preset is
// its rows (content/effectpolicy.go doc).

import "github.com/shady2k/nocx/internal/content"

func allRows(d content.Decision) content.EffectPolicy {
	r := content.EffectRow{Decision: d}
	return content.EffectPolicy{
		Observe: r, MutateReversible: r, MutateDestructive: r,
		PrivilegeChange: r, Disclose: r, CrossBoundary: r, Delegate: r,
	}
}

func askEveryTimeMatrix() content.EffectPolicy { return allRows(content.DecisionAsk) }

func autonomousMatrix() content.EffectPolicy { return allRows(content.DecisionPermit) }

func askOnMutateMatrix() content.EffectPolicy {
	return content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionPermit},
		MutateReversible:  content.EffectRow{Decision: content.DecisionAsk},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
		PrivilegeChange:   content.EffectRow{Decision: content.DecisionAsk},
		Disclose:          content.EffectRow{Decision: content.DecisionAsk},
		CrossBoundary:     content.EffectRow{Decision: content.DecisionAsk},
		Delegate:          content.EffectRow{Decision: content.DecisionAsk},
	}
}
