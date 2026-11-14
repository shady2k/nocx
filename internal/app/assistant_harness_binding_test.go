package app

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/toolendpoint"
)

type assistantEndpointStub struct{}

func (assistantEndpointStub) SocketPath() string { return "/private/tools.sock" }
func (assistantEndpointStub) SettleAdmissionsContext(context.Context, string, toolendpoint.AdmissionEpoch) error {
	return nil
}

func (assistantEndpointStub) PendingAdmissions(string, toolendpoint.AdmissionEpoch) bool {
	return false
}

func TestAssistantToolsRequirePublishedEndpoint(t *testing.T) {
	var binding assistantToolBinding
	_, _, err := binding.newSession(context.Background(), harness.SessionInfo{ConversationID: "conversation", Generation: 1})
	if err == nil {
		t.Fatal("agent tools admitted without an endpoint")
	}
	if _, ok := binding.Lookup("unknown"); ok {
		t.Fatal("unknown bearer admitted")
	}
}

func TestAssistantToolsMintPrivateSessionBridge(t *testing.T) {
	var binding assistantToolBinding
	binding.bind(assistantEndpointStub{}, "/private/nocx-helper")
	tools, bridge, err := binding.newSession(context.Background(), harness.SessionInfo{ConversationID: "conversation", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(bridge.Command) != 4 || bridge.Command[0] != "/private/nocx-helper" || bridge.Command[3] != "/private/tools.sock" {
		t.Fatalf("bridge command: %v", bridge.Command)
	}
	if len(bridge.Env) != 1 {
		t.Fatalf("private bearer environment: %v", bridge.Env)
	}
	if err := tools.Retire(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantToolsPublishAuthorityToAuthorizer(t *testing.T) {
	var binding assistantToolBinding
	binding.bind(assistantEndpointStub{}, "/private/nocx-helper")
	tools, bridge, err := binding.newSession(context.Background(), harness.SessionInfo{ConversationID: "conversation", Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tools.Retire(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	authority := &testTurnTools{ctx: agenttools.RunContext{RunID: "run", Session: "pane-session"}}
	if err := tools.BeginTurn(authority); err != nil {
		t.Fatal(err)
	}
	_, token, _ := strings.Cut(bridge.Env[0], "=")
	admitted, ok := binding.Lookup(token)
	if !ok || admitted.Tools.RunContext().RunID != "run" {
		t.Fatalf("authority missing: %v", ok)
	}
	if err := tools.EndTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := binding.Lookup(token); ok {
		t.Fatal("ended turn admitted")
	}
}
