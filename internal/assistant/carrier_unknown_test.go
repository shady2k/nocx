package assistant

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func refusedUnknownPolicy() content.EffectPolicy {
	row := content.EffectRow{Decision: content.DecisionRefuse}
	return content.EffectPolicy{Observe: row, MutateReversible: row, MutateDestructive: row, PrivilegeChange: row, Disclose: row, CrossBoundary: row, Delegate: row}
}

// The kernel enforces policy even when a caller bypasses catalogue filtering.
// Unknown names are malformed model output, covered in policy_test.go; the
// removed embedded carrier's available-tool suggestions are not a kernel API.
func TestKernelEntryWithheldToolStillRefusesPolicy(t *testing.T) {
	grant, _ := testDirGrant(t, refusedUnknownPolicy())
	grant.Scopes = append(grant.Scopes, content.GrantScope{Kind: content.ResourceSession, ID: "session-a"})
	kernel := middlewareFor(t, grant, &fakeLedger{}, nil).kernel
	for _, tool := range kernel.registry.ForGrant(kernel.currentGrant()) {
		if tool.Name == "session.list" {
			t.Fatal("all-refused tool was advertised")
		}
	}
	result, err := kernel.invokeClassified(context.Background(), "session.list", "call-1", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	want := modelResult{text: refusalResult("session.list", RefusedByDecision, ""), kind: modelNocxMessage}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
}
