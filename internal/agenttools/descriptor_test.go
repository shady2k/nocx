package agenttools

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// The descriptor digest (nocx-d6gn4.9) identifies WHICH version of a tool the
// model was shown when it made a call. The experiment compares two carriers
// across many runs; if a tool's description or schema changes midway the two
// cohorts stop being comparable, and nothing recorded today would say so.
//
// A hand-maintained version number is the wrong shape: it is bumped by
// remembering to, and the one time it matters is the time somebody forgot. A
// digest over the declaration's own content cannot drift from it.

func toolFor(description string, schema string) Tool {
	return Tool{
		Declaration: Declaration{
			Name:        "files.read",
			Description: description,
			Effect:      content.EffectObserve,
			Resources:   []content.ResourceKind{content.ResourcePath},
			ResourceArg: "path",
			Executes:    InGo,
			Params:      "files.read.schema.json",
		},
		ParamsSchema: json.RawMessage(schema),
	}
}

func TestDescriptorDigest_IsStableForOneDeclaration(t *testing.T) {
	a := toolFor("read a file", `{"type":"object"}`)
	b := toolFor("read a file", `{"type":"object"}`)
	if a.DescriptorDigest() != b.DescriptorDigest() {
		t.Fatalf("the same declaration digests differently: %q vs %q", a.DescriptorDigest(), b.DescriptorDigest())
	}
	if a.DescriptorDigest() == "" {
		t.Fatal("digest is empty; a record that cannot name the descriptor cannot say the cohorts matched")
	}
}

func TestDescriptorDigest_ChangesWithWhatTheModelIsShown(t *testing.T) {
	base := toolFor("read a file", `{"type":"object"}`)

	changed := map[string]Tool{
		"description": toolFor("read a file and return a window of it", `{"type":"object"}`),
		"schema":      toolFor("read a file", `{"type":"object","required":["path"]}`),
	}
	for what, tool := range changed {
		if tool.DescriptorDigest() == base.DescriptorDigest() {
			t.Errorf("changing the %s left the digest unchanged; the cohort break would be invisible", what)
		}
	}
}

// TestDescriptorDigest_ChangesWithWhatThePolicyDecidesOn is the half that is
// easy to miss: effect and resources are not shown to the model, but a change
// to either changes what the call MEANS, and a comparison across it is a
// comparison of two different things.
func TestDescriptorDigest_ChangesWithWhatThePolicyDecidesOn(t *testing.T) {
	base := toolFor("read a file", `{"type":"object"}`)

	effect := toolFor("read a file", `{"type":"object"}`)
	effect.Effect = content.EffectMutateDestructive
	if effect.DescriptorDigest() == base.DescriptorDigest() {
		t.Error("changing the effect left the digest unchanged")
	}

	resources := toolFor("read a file", `{"type":"object"}`)
	resources.Resources = []content.ResourceKind{content.ResourceSession}
	if resources.DescriptorDigest() == base.DescriptorDigest() {
		t.Error("changing the resource kinds left the digest unchanged")
	}
}
