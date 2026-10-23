package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/waittest"
)

type agentMCPRuntime struct {
	mu          sync.Mutex
	refreshes   int
	invocations int
	closedRuns  []string
}

func (r *agentMCPRuntime) Refresh(context.Context, mcp.Activation) (mcp.Catalog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshes++
	return mcp.Catalog{}, nil
}

func (r *agentMCPRuntime) Invoke(context.Context, mcp.Invocation) (mcp.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invocations++
	return mcp.Result{}, nil
}

func (r *agentMCPRuntime) CloseRun(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closedRuns = append(r.closedRuns, runID)
}

func (*agentMCPRuntime) CloseServer(string) {}
func (*agentMCPRuntime) Close() error       { return nil }

func (r *agentMCPRuntime) state() (int, int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshes, r.invocations, append([]string(nil), r.closedRuns...)
}

func createAskMCPServer(t *testing.T, repo profile.MCPServerRepository) {
	t.Helper()
	created, err := repo.CreateMCPServer(profile.MCPServer{
		Name:      "Dormant tools",
		Enabled:   true,
		Transport: profile.MCPTransportStdio,
		Stdio:     &profile.MCPStdioConfig{Command: "/bin/false", Argv: []string{}, Env: []profile.MCPEnvBinding{}},
		Limits: profile.MCPLimits{
			StartupTimeoutMS: 15_000, CallTimeoutMS: 60_000,
			IdleTimeoutMS: 30_000, MaxResultBytes: 262_144,
		},
	})
	if err != nil {
		t.Fatalf("create MCP server: %v", err)
	}
	refreshed, err := repo.RefreshMCPServerCatalog(created.ID, created.Revision, profile.MCPCatalog{
		Tools: []profile.MCPTool{{
			Name:        "echo",
			Description: "untrusted remote description",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		}},
	})
	if err != nil {
		t.Fatalf("refresh MCP catalog fixture: %v", err)
	}
	if _, err := repo.SetMCPToolsEnabled(refreshed.ID, refreshed.Revision, []string{"echo"}); err != nil {
		t.Fatalf("enable MCP tool fixture: %v", err)
	}
}

func TestAgentAsk_MCPSetupUsesPersistedSnapshotWithoutActivationAndClosesTerminalRun(t *testing.T) {
	repo := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	createAskMCPServer(t, repo)
	runtime := &agentMCPRuntime{}
	client := &scriptedAssistantClient{}
	h := newAskHarnessWithOpts(t, client,
		WithProfileRepository(repo),
		WithMCPServices(runtime, runtime, runtime),
	)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "mcp-dormant-ask", "sessionId": sid, "question": "what tools are configured?", "cwd": "/repo", "attachedContent": []any{},
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	state := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if string(state) == "" {
		t.Fatal("agent run did not terminalize")
	}

	client.mu.Lock()
	params := client.receivedParams
	client.mu.Unlock()
	if len(params.MCPCatalogs) != 1 {
		t.Fatalf("assistant MCP snapshots = %d, want one persisted snapshot", len(params.MCPCatalogs))
	}
	if params.MCPRuntime == nil {
		t.Fatal("assistant received no on-demand MCP runtime")
	}
	if params.RunID != fmt.Sprintf("%d", res.RunID) {
		t.Fatalf("assistant RunID = %q, want %d", params.RunID, res.RunID)
	}
	refreshes, invocations, closed := runtime.state()
	if refreshes != 0 || invocations != 0 {
		t.Fatalf("agent.ask setup activated MCP: refreshes=%d invocations=%d", refreshes, invocations)
	}
	if len(closed) == 0 || closed[0] != fmt.Sprintf("%d", res.RunID) {
		t.Fatalf("closed MCP runs = %v, want [%d]", closed, res.RunID)
	}
}

func TestAgentCancel_ClosesMCPRun(t *testing.T) {
	runtime := &agentMCPRuntime{}
	client := newCancelBlockingClient()
	h := newAskHarnessWithOpts(t, client, WithMCPServices(runtime, runtime, runtime))
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "mcp-cancel-ask", "sessionId": sid, "question": "stop this", "cwd": "/repo", "attachedContent": []any{},
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	select {
	case <-client.emitted:
	case <-time.After(time.Second):
		t.Fatal("assistant did not enter its cancellable stream")
	}
	resp, _ := cancelCallPreservingNotifications(t, h.conn, res.RunID, 2)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode agent.cancel: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("agent.cancel: %+v", envelope.Error)
	}
	refreshes, invocations, closed := runtime.state()
	if refreshes != 0 || invocations != 0 {
		t.Fatalf("cancelled dormant run activated MCP: refreshes=%d invocations=%d", refreshes, invocations)
	}
	if len(closed) == 0 || closed[0] != fmt.Sprintf("%d", res.RunID) {
		t.Fatalf("closed MCP runs = %v, want [%d]", closed, res.RunID)
	}
}

var (
	_ assistant.Client = (*scriptedAssistantClient)(nil)
	_ mcp.Runtime      = (*agentMCPRuntime)(nil)
)

func (*agentMCPRuntime) RunServerMutations(ctx context.Context, _ []string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*agentMCPRuntime) Authorize(context.Context, mcp.Activation, mcp.URLPresenter) (mcp.OAuthStatus, error) {
	return mcp.OAuthStatus{}, errors.New("unexpected OAuth authorization")
}

func (*agentMCPRuntime) Forget(context.Context, string) error {
	return errors.New("unexpected OAuth forget")
}

func (*agentMCPRuntime) DiscardOAuthSession(context.Context, string) error {
	return errors.New("unexpected OAuth discard")
}

func TestAgentMCPResumeKeepsPinnedCatalogAndDestinationFence(t *testing.T) {
	repo := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	createAskMCPServer(t, repo)
	servers, err := repo.ListMCPServers()
	if err != nil {
		t.Fatal(err)
	}
	server := servers[0]
	destination := content.GrantScope{Kind: content.ResourceDestination, ID: "mcp+stdio:" + server.ID}
	name := agenttools.MCPModelName(server.ID, "echo")
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: func(runID string) error {
			return &assistant.ApprovalRequestedError{Request: &assistant.ApprovalRequest{
				RunID: runID, Attempt: 1, Tool: name, CallID: "mcp-call", Arguments: `{}`, ArgHash: "mcp-hash",
				Effect: content.EffectDelegate, Resource: &destination,
			}}
		}},
		{deltas: []string{"done"}},
	}}
	runtime := &agentMCPRuntime{}
	policy := askPolicyStore(t)
	h := newAskHarnessWithOpts(t, client, WithProfileRepository(repo), WithAgentPolicy(policy), WithMCPServices(runtime, runtime, runtime))
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "mcp-resume", "sessionId": sid, "question": "use this tool", "cwd": "/repo", "attachedContent": []any{}}, 1)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	var asked agentApprovalRequested
	if err := json.Unmarshal(raw, &asked); err != nil {
		t.Fatal(err)
	}
	_, _, closed := runtime.state()
	if len(closed) == 0 {
		t.Fatal("approval retained MCP sessions")
	}
	// A policy comparison uses the held catalog fence, not current metadata.
	if runs, rows := h.ws.RunsUnreachedByRowWrite(policy.Policy()); len(runs) != 0 || len(rows) != 0 {
		t.Fatalf("unchanged policy moved pinned MCP run: %v %v", runs, rows)
	}
	server.Enabled = false
	if _, err := repo.UpdateMCPServer(server); err != nil {
		t.Fatal(err)
	}
	harness := &scopeHarness{askHarness: h, policy: policy, sid: sid, runID: res.RunID, entryID: res.EntryID, asked: asked}
	harness.approve(t, "once")
	waittest.WaitForTimeout(t, "MCP approval resume", 5*time.Second, func() bool { return client.askCount() == 2 })
	params := client.params()
	if len(params) != 2 {
		t.Fatalf("received %d drives", len(params))
	}
	for _, p := range params {
		if len(p.MCPCatalogs) != 1 || p.Grant == nil {
			t.Fatalf("resume lost catalog/grant: %+v", p)
		}
		scopes, err := p.MCPCatalogs[0].GrantScopes()
		if err != nil || len(scopes) != 1 || scopes[0] != destination {
			t.Fatalf("snapshot changed: %+v %v", scopes, err)
		}
		if !hasGrantScope(p.Grant.Scopes, content.ResourceDestination, destination.ID) {
			t.Fatal("resume lost exact process fence")
		}
	}
	refreshes, invocations, _ := runtime.state()
	if refreshes != 0 || invocations != 0 {
		t.Fatal("snapshot/approval/resume activated MCP")
	}
}
