package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

func TestFloor_RefusesUnderMostPermissivePolicy(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config", "nocx")
	dataDir := filepath.Join(t.TempDir(), "data", "nocx")
	policy := floorPermissivePolicy()
	grant := policy.WithFloor(content.NewFloor(configDir, dataDir)).AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-floor"},
		{Kind: content.ResourcePath, ID: "/"},
	})
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	k, err := newEffectKernel(nil, grant, reg, &fakeLedger{}, nil, &fakeKnownMaterial{}, "run-floor", "session-floor", 1, "", nil, Attachments{}, nil, nil)
	if err != nil {
		t.Fatalf("newEffectKernel: %v", err)
	}

	for _, tc := range []struct {
		name string
		tool string
		args string
	}{
		{name: "policy document", tool: "files.read", args: `{"path":"` + filepath.Join(configDir, "agent-policy.json") + `"}`},
		{name: "dangerous home erase", tool: "session.run", args: `{"command":"rm -rf $HOME"}`},
		{name: "device format", tool: "session.run", args: `{"command":"mkfs.ext4 /dev/sda"}`},
		{name: "fork bomb", tool: "session.run", args: `{"command":":(){ :|:& };:"}`},
		{name: "compound fork bomb", tool: "session.run", args: `{"command":"echo ok; :(){ :|:& };:"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := k.Invoke(context.Background(), tc.tool, tc.name, tc.args)
			if err != nil {
				t.Fatalf("Invoke returned error: %v", err)
			}
			lower := strings.ToLower(out)
			if !strings.Contains(lower, "refused") || !strings.Contains(lower, "floor") || !strings.Contains(lower, "never") {
				t.Fatalf("refusal = %q, want a human-facing non-overridable floor sentence", out)
			}
		})
	}
}

func floorPermissivePolicy() content.EffectPolicy {
	row := content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/"}},
	}
	return content.EffectPolicy{
		Observe:           row,
		MutateReversible:  row,
		MutateDestructive: row,
		PrivilegeChange:   row,
		Disclose:          row,
		CrossBoundary:     row,
		Delegate:          row,
		Rules: []content.InvocationRule{{
			Selector: content.InvocationSelector{Exact: [][]string{{"*"}}},
			Decision: content.DecisionPermit,
		}},
	}
}

func TestNewClient_InjectsFloorBeforeToolExecution(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config", "nocx")
	_, server := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "files.read",
		args: `{"path":"` + filepath.Join(configDir, "agent-policy.json") + `"}`,
		id:   "floor-call",
	}))
	defer server.Close()

	grant := floorPermissivePolicy().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: "/"},
	})
	client, _, err := NewClientAndRegistry(nil, nil, content.NewFloor(configDir, filepath.Join(t.TempDir(), "data", "nocx")), nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	events := make([]AskEvent, 0)
	if err := client.Ask(context.Background(), askParams(server.URL, &grant, &fakeLedger{}, nil), func(event AskEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, event := range events {
		if event.Kind == AskToolCall {
			t.Fatalf("floor-protected call was announced for execution: %+v", event.Call)
		}
	}
}

func TestFloor_ReasonIsTruthfulForReadAndWrite(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config", "nocx")
	floor := content.NewFloor(configDir, filepath.Join(t.TempDir(), "data", "nocx"))
	grant := floorPermissivePolicy().WithFloor(floor).AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-floor-reason"},
		{Kind: content.ResourcePath, ID: "/"},
	})
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	k, err := newEffectKernel(nil, grant, reg, &fakeLedger{}, nil, &fakeKnownMaterial{}, "run-floor-reason", "session-floor-reason", 1, "", nil, Attachments{}, nil, nil)
	if err != nil {
		t.Fatalf("newEffectKernel: %v", err)
	}
	path := filepath.Join(configDir, "agent-policy.json")
	calls := []struct {
		name string
		tool string
		args string
	}{
		{name: "read", tool: "files.read", args: `{"path":"` + path + `"}`},
		{name: "write", tool: "files.edit", args: `{"path":"` + path + `","revision":"unused","patch":"PUT 1.=1:\n+changed"}`},
	}
	const reasonPhrase = "nocx-controlled state that an agent can never inspect or modify"
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			out, err := k.Invoke(context.Background(), call.tool, call.name, call.args)
			if err != nil {
				t.Fatalf("Invoke returned error: %v", err)
			}
			if !strings.Contains(strings.ToLower(out), reasonPhrase) {
				t.Fatalf("floor refusal = %q, want truthful read/write wording", out)
			}
		})
	}
}
