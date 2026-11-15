package mcpstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/toolendpoint"
)

type assistantTurnAuthorizer struct{ tools *assistant.RunTools }

func (a assistantTurnAuthorizer) Admit(_ toolendpoint.Peer, publish func(string, toolendpoint.AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	inv, err := a.tools.Admit(context.Background())
	if err != nil {
		return inv, nil, err
	}
	if !publish(inv.RunContext.Session, 1) {
		return assistant.ToolInvocation{}, nil, toolendpoint.ErrNotEnrolled
	}
	return inv, func() {}, nil
}

type noKnownMaterial struct{}

func (noKnownMaterial) FindKnown(context.Context, string) ([]assistant.KnownMatch, error) {
	return nil, nil
}

func TestAssistantMCPPrefixIsIndependentOfGrantAndCallsRemainEnforced(t *testing.T) {
	_, logger := logtest.New(t)
	client, registry, err := assistant.NewClientAndRegistry(logger, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	permit := content.EffectRow{Decision: content.DecisionPermit}
	refuse := content.EffectRow{Decision: content.DecisionRefuse}
	policy := content.EffectPolicy{Observe: permit, MutateReversible: refuse, MutateDestructive: refuse, PrivilegeChange: refuse, Disclose: refuse, CrossBoundary: refuse, Delegate: refuse}
	first := policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: t.TempDir()}})
	policy.Delegate = permit
	second := policy.AsGrant(nil)
	var initialized, listed json.RawMessage
	for i, grant := range []content.Grant{first, second} {
		tools, err := assistant.BindRunTools(context.Background(), client, assistant.AskParams{
			Grant: &grant, FreshGrant: func() *content.Grant { return &grant }, KnownMaterial: noKnownMaterial{}, RunID: "run", SessionID: "pane",
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		endpoint := startRealEndpoint(t, assistantTurnAuthorizer{tools}, tools)
		driver := startStdio(t, endpoint.SocketPath())
		driver.send(0, "initialize", `{"protocolVersion":"2025-11-25"}`)
		init := driver.nextID(0)["result"]
		driver.send(1, "tools/list", `{}`)
		list := driver.nextID(1)["result"]
		if i == 0 {
			initialized, listed = init, list
		} else {
			if !bytes.Equal(initialized, init) {
				t.Errorf("initialize changed with grant:\n%s\n%s", initialized, init)
			}
			if !bytes.Equal(listed, list) {
				t.Errorf("tools/list changed with grant:\n%s\n%s", listed, list)
			}
		}
		var catalogue struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(list, &catalogue); err != nil {
			t.Fatal(err)
		}
		offered := map[string]bool{}
		for _, tool := range catalogue.Tools {
			offered[tool.Name] = true
		}
		for _, tool := range registry.All() {
			if tool.Narrow != nil && !offered[tool.Name] {
				t.Errorf("turn %d does not offer executable tool %s", i, tool.Name)
			}
		}
		driver.send(2, "tools/call", `{"name":"files.read","arguments":{"path":"/outside-the-grant"}}`)
		if reason := toolRefusal(t, driver.nextID(2)); reason == "" {
			t.Fatal("out-of-grant call has no refusal reason")
		}
	}
}
