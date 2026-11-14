package assistant

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// The embedded carrier owns policy-specific unknown-call sentences. The MCP
// helper instead refuses unadvertised names without forwarding them; transport
// tests hold that boundary and completion separately.
func unknownCarrierFor(t *testing.T, policy content.EffectPolicy) *callsCarrier {
	t.Helper()
	grant, _ := testDirGrant(t, policy)
	grant.Scopes = append(grant.Scopes, content.GrantScope{Kind: content.ResourceSession, ID: "session-a"})
	mw := middlewareFor(t, grant, &fakeLedger{}, nil)
	return &callsCarrier{effectKernel: mw.kernel}
}

func refusedUnknownPolicy() content.EffectPolicy {
	row := content.EffectRow{Decision: content.DecisionRefuse}
	return content.EffectPolicy{Observe: row, MutateReversible: row, MutateDestructive: row, PrivilegeChange: row, Disclose: row, CrossBoundary: row, Delegate: row}
}

func TestCallsCarrier_UnknownStandingNeverNamesThePolicyRefusal(t *testing.T) {
	c := unknownCarrierFor(t, refusedUnknownPolicy())
	result, err := c.UnknownTool("session.read", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "this kind of action is refused by the policy") {
		t.Fatalf("tool result: %s", result)
	}
}

func TestCallsCarrier_UnknownWithheldToolNamesEveryRefusedEffect(t *testing.T) {
	c := unknownCarrierFor(t, refusedUnknownPolicy())
	result, err := c.UnknownTool("session.run", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range []string{"observe", "mutate-reversible", "mutate-destructive", "cross-boundary", "delegate"} {
		if !strings.Contains(result, effect) {
			t.Errorf("result lacks refused effect %q: %s", effect, result)
		}
	}
	for _, tool := range c.registry.ForGrant(c.currentGrant()) {
		if tool.Name == "session.run" {
			t.Fatal("all-refused tool was advertised")
		}
	}
}

func TestCallsCarrier_UnknownInventedToolNamesTheAvailableTools(t *testing.T) {
	c := unknownCarrierFor(t, autonomousMatrix())
	result, err := c.UnknownTool("model.invented.tool", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "There is no such tool") || !strings.Contains(result, "model.invented.tool") {
		t.Fatalf("result: %s", result)
	}
	for _, tool := range c.registry.ForGrant(c.currentGrant()) {
		if !strings.Contains(result, tool.Name) {
			t.Errorf("available tool %q missing: %s", tool.Name, result)
		}
	}
}
