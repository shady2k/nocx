package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

func TestAgentHarnessEgressDeclineEndsRunDespiteAgentContinuing(t *testing.T) {
	policy := askObserveStore(t)
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte("api key: sk-proj-abcdefghijklmnopqrstuvwx"), 0o600); err != nil {
		t.Fatal(err)
	}
	permit := policy.Policy()
	permit.Observe.Decision = content.DecisionPermit
	permit.Observe.Scopes = nil
	script := approvalToolScript("files.read", map[string]any{"path": path})
	script.Steps[0].ContinueOnRPCError = true
	h := suspendedRunWith(t, egressPolicy{GlobalPolicy: policy, policy: permit}, script)
	h.deny(t, "once")
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.State != string(content.RunFailed) {
		t.Fatalf("a declined result must end the run, even if the agent continues: %s", raw)
	}
	if state.Error != "the person declined to send the result to the model" {
		t.Fatalf("screen outcome lost: %s", raw)
	}
}

type unavailableEgressMaterial struct{}

func (unavailableEgressMaterial) FindKnown(context.Context, string) ([]assistant.KnownMatch, error) {
	return nil, errors.New("screen unavailable")
}

func TestAgentHarnessEgressScreeningFailureEndsRun(t *testing.T) {
	script := approvalToolScript("snippets.list", map[string]any{})
	script.Steps[0].ContinueOnRPCError = true
	h := newAskHarnessWithOpts(t, script,
		WithAgentPolicy(autonomousPolicyStore(t)), WithAgentKnownMaterial(unavailableEgressMaterial{}))
	sid := openLocalSession(t, h.conn)
	_, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "failed-screen", "sessionId": sid, "question": "list snippets", "cwd": "/repo"}, 1)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.State != string(content.RunFailed) || state.Error != "nocx could not check the tool result for secrets, so it was not sent to the agent. Unlock the vault and ask again." {
		t.Fatalf("screen failure lost: %s", raw)
	}
}
