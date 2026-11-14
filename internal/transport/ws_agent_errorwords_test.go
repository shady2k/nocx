package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
)

var frameworkWords = []string{"NodeRunError", "GraphRunError", "node path", "ToolNode", "node_1", "call_"}

func assertNoFrameworkWords(t *testing.T, sentence string) {
	t.Helper()
	for _, word := range frameworkWords {
		if strings.Contains(sentence, word) {
			t.Errorf("wire sentence contains framework identifier %q: %q", word, sentence)
		}
	}
}

type failingKnownMaterial struct{ err error }

func (f failingKnownMaterial) FindKnown(context.Context, string) ([]assistant.KnownMatch, error) {
	return nil, f.err
}

func TestAskFailure_EgressScreeningNamesSealedVault(t *testing.T) {
	script := protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepTool, Name: "session.list", Arguments: map[string]any{}}, {Kind: protocol.StepEnd}}}
	h := newAskHarnessWithOpts(t, script, WithAgentPolicy(autonomousPolicyStore(t)), WithAgentKnownMaterial(failingKnownMaterial{err: vault.ErrVaultSealed}))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "sealed-egress", "sessionId": sid, "question": "list sessions", "cwd": "/repo"}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	raw := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "failed" || state.Error == "" {
		t.Fatalf("state: %+v", state)
	}
	assertNoFrameworkWords(t, state.Error)
	stored, err := h.db.Ledger().RunState(t.Context(), res.RunID)
	if err != nil || stored == nil || *stored != content.RunFailed {
		t.Fatalf("ledger: %v %v", stored, err)
	}
}

func refusingPolicyStore(t *testing.T) *assistant.GlobalPolicyStore {
	t.Helper()
	refuse := content.EffectRow{Decision: content.DecisionRefuse}
	policy := content.EffectPolicy{Observe: refuse, MutateReversible: refuse, MutateDestructive: refuse, PrivilegeChange: refuse, Disclose: refuse, CrossBoundary: refuse, Delegate: refuse}
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	if err := store.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	return store
}

func unknownToolRun(t *testing.T, policy *assistant.GlobalPolicyStore, name string, args map[string]any) (*askHarness, agentRunState) {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), "refused-tool-result")
	script := protocol.Script{ResultFile: resultPath, Steps: []protocol.Step{{Kind: protocol.StepTool, Name: name, Arguments: args, ResultFile: resultPath}, {Kind: protocol.StepAnswer, Text: "The action was refused."}, {Kind: protocol.StepEnd}}}
	h := newAskHarnessWithOpts(t, script, WithAgentPolicy(policy))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "withheld-tool", "sessionId": sid, "question": "try this action", "cwd": "/repo"}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	raw := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "completed" || state.Error != "" {
		t.Fatalf("wire: %+v", state)
	}
	if got := proseBodyOf(t, h.db.Ledger(), res.EntryID); got != "The action was refused." {
		t.Fatalf("prose: %q", got)
	}
	if _, requested := inboxOf(h.conn).take(isNotification("agent.approvalRequested")); requested {
		t.Fatal("refused call raised approval")
	}
	return h, state
}

// The MCP helper refuses names absent from its authenticated catalogue before
// dispatch. The embedded carrier's richer policy sentences are separately held
// by TestCallsCarrier_Unknown* in internal/assistant.
func assertUnadvertisedCallWasNotRun(t *testing.T, h *askHarness, name string) {
	t.Helper()
	if result := h.toolResult(); !strings.Contains(result, name) || !strings.Contains(result, "not offered by the endpoint") {
		t.Fatalf("unadvertised call refusal: %s", result)
	}
	entries, err := h.db.Ledger().ListEntries(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Kind == content.EntryAction {
			t.Fatalf("unadvertised call reached execution: %+v", entry)
		}
	}
}

func TestAsk_StandingNeverUnknownCallCompletes(t *testing.T) {
	h, _ := unknownToolRun(t, refusingPolicyStore(t), "session.read", map[string]any{"sessionId": "not-this-run"})
	assertUnadvertisedCallWasNotRun(t, h, "session.read")
}

func TestAsk_WithheldToolNamesEveryRefusedEffect(t *testing.T) {
	h, _ := unknownToolRun(t, refusingPolicyStore(t), "session.run", map[string]any{})
	assertUnadvertisedCallWasNotRun(t, h, "session.run")
}

func TestAsk_InventedUnknownCallCompletes(t *testing.T) {
	h, _ := unknownToolRun(t, autonomousPolicyStore(t), "model.invented.tool", map[string]any{})
	assertUnadvertisedCallWasNotRun(t, h, "model.invented.tool")
}

func TestAsk_EgressSealedVaultUnlocksAndContinues(t *testing.T) {
	const secret = "egress-secret-value"
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	script := protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepTool, Name: "files.read", Arguments: map[string]any{"path": path}}, {Kind: protocol.StepAnswer, Text: "ok"}, {Kind: protocol.StepEnd}}}
	h := newAskHarnessWithOpts(t, script, WithAgentPolicy(autonomousPolicyStore(t)))
	// Seed known material, then seal the actual vault before the result's gate.
	createSecret(t, h.v, "egress-secret", secret)
	h.v.Seal()
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "egress-unlock", "sessionId": sid, "question": "read the file", "cwd": filepath.Dir(path)}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	frame := readUnlockRequestFrame(t, h.conn)
	if frame.Reason != "screen the tool result" {
		t.Fatalf("unlock reason: %q", frame.Reason)
	}
	unseal := jsonrpcCallWithID(t, h.conn, "vault.unseal", map[string]any{"means": "passphrase", "secret": "test"}, 3)
	if isErrorResponse(t, unseal) {
		t.Fatalf("unseal: %s", unseal)
	}
	answerUnlock(t, h.conn, frame.RequestID, "unsealed")
	raw := readNotification(t, h.conn, "agent.approvalRequested", 10*time.Second)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("approval leaked secret: %s", raw)
	}
	var approval agentApprovalRequested
	if err := json.Unmarshal(raw, &approval); err != nil {
		t.Fatal(err)
	}
	if approval.Reason != "egress" || len(approval.Findings) != 1 || approval.Findings[0].SecretName != "egress-secret" {
		t.Fatalf("approval: %+v", approval)
	}
	if _, rpcErr := approveOverWire(t, h.conn, map[string]any{"runId": approval.RunID, "attempt": approval.Attempt, "tool": approval.Tool, "callId": approval.CallID, "argHash": approval.ArgHash, "approved": true, "scope": "once"}, 4); rpcErr != nil {
		t.Fatalf("approve: %+v", rpcErr)
	}
	raw = readNotification(t, h.conn, "agent.runState", 10*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "completed" || state.Error != "" {
		t.Fatalf("state: %+v", state)
	}
	if got := proseBodyOf(t, h.db.Ledger(), res.EntryID); got != "ok" {
		t.Fatalf("answer: %q", got)
	}
	if result := h.toolResult(); !strings.Contains(result, secret) {
		t.Fatalf("released result missing held bytes: %s", result)
	}
}

func TestAskFailure_CausesKeepTheirOwnSentences(t *testing.T) {
	seen := map[string]bool{}
	for _, step := range []protocol.Step{{Kind: protocol.StepFail, Reason: "the provider is down"}, {Kind: protocol.StepExit, Code: 7}} {
		h := newAskHarness(t, protocol.Script{Steps: []protocol.Step{step}})
		sid := openLocalSession(t, h.conn)
		res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "failure-words", "sessionId": sid, "question": "answer", "cwd": "/repo"}, 2)
		if rpcErr != nil {
			t.Fatalf("ask: %+v", rpcErr)
		}
		raw := readNotification(t, h.conn, "agent.runState", 10*time.Second)
		var state agentRunState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if state.State != "failed" || state.Error == "" || seen[state.Error] {
			t.Fatalf("failure words: %+v; seen %v", state, seen)
		}
		assertNoFrameworkWords(t, state.Error)
		seen[state.Error] = true
		stored, err := h.db.Ledger().RunState(t.Context(), res.RunID)
		if err != nil || stored == nil || *stored != content.RunFailed {
			t.Fatalf("ledger: %v %v", stored, err)
		}
	}
}

// These are classification units, not agent.ask journeys. The embedded
// provider's typed failures still have a production sentence mapping.
func TestAskFailure_MalformedModelOutputKeepsItsOwnSentence(t *testing.T) {
	reason, sentence := classifyAskFailure(fmt.Errorf("jsonschema https://nocx.local additional properties: %w", assistant.ErrMalformedModelOutput), "model")
	if reason != content.TermFailed {
		t.Fatalf("reason: %s", reason)
	}
	assertNoFrameworkWords(t, sentence)
	if strings.Contains(strings.ToLower(sentence), "polic") || !strings.Contains(strings.ToLower(sentence), "tool call") {
		t.Fatalf("malformed sentence: %q", sentence)
	}
	for _, word := range []string{"jsonschema", "https://nocx.local", "additional properties"} {
		if strings.Contains(sentence, word) {
			t.Errorf("validator text leaked: %q", sentence)
		}
	}
}

func TestAskFailure_AnUnreachableEndpointKeepsItsOwnSentence(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "http://127.0.0.1:1/v1/chat/completions", Err: syscall.ECONNREFUSED}
	reason, sentence := classifyAskFailure(err, "model")
	if reason != content.TermFailed {
		t.Fatalf("reason: %s", reason)
	}
	assertNoFrameworkWords(t, sentence)
	if !strings.Contains(strings.ToLower(sentence), "reach") || strings.Contains(strings.ToLower(sentence), "polic") {
		t.Fatalf("endpoint sentence: %q", sentence)
	}
	for _, word := range []string{"127.0.0.1", "dial tcp", "connection refused", "chat/completions"} {
		if strings.Contains(sentence, word) {
			t.Errorf("transport text leaked: %q", sentence)
		}
	}
}
