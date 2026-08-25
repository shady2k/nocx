package content_test

// The three presets of the original ADR-0020 §7, expressed AS MATRICES:
// "remain expressible in the new form" is asserted for real by the tests
// that use these — the rows a person writes, never a production constructor
// (clean-only: no preset vocabulary rides the wire or the store).

import "github.com/shady2k/nocx/internal/content"

func presetAllRows(d content.Decision) content.EffectPolicy {
	r := content.EffectRow{Decision: d}
	return content.EffectPolicy{
		Observe: r, MutateReversible: r, MutateDestructive: r,
		PrivilegeChange: r, Disclose: r, CrossBoundary: r, Delegate: r,
	}
}

func presetAskEveryTime() content.EffectPolicy { return presetAllRows(content.DecisionAsk) }

func presetAutonomous() content.EffectPolicy { return presetAllRows(content.DecisionPermit) }

func presetAskOnMutate() content.EffectPolicy {
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
