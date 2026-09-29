package transport

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
)

type blockingVaultMCPRuntime struct {
	gate            sync.RWMutex
	mu              sync.Mutex
	start           chan struct{}
	mutationAttempt chan struct{}
	release         chan struct{}
	closed          map[string]bool
}

func newBlockingVaultMCPRuntime() *blockingVaultMCPRuntime {
	return &blockingVaultMCPRuntime{
		start: make(chan struct{}), mutationAttempt: make(chan struct{}),
		release: make(chan struct{}), closed: make(map[string]bool),
	}
}

func (*blockingVaultMCPRuntime) Refresh(context.Context, mcp.Activation) (mcp.Catalog, error) {
	return mcp.Catalog{}, nil
}

func (r *blockingVaultMCPRuntime) Invoke(_ context.Context, invocation mcp.Invocation) (mcp.Result, error) {
	r.gate.RLock()
	defer r.gate.RUnlock()
	r.mu.Lock()
	closed := r.closed[invocation.Activation.ServerID]
	r.mu.Unlock()
	if closed {
		return mcp.Result{}, mcp.ErrClosed
	}
	select {
	case <-r.start:
	default:
		close(r.start)
	}
	<-r.release
	return mcp.Result{}, nil
}

func (*blockingVaultMCPRuntime) CloseRun(string) {}

func (r *blockingVaultMCPRuntime) CloseServer(id string) {
	r.mu.Lock()
	r.closed[id] = true
	r.mu.Unlock()
}

func (*blockingVaultMCPRuntime) Close() error { return nil }

func (r *blockingVaultMCPRuntime) RunServerMutation(id string, mutation func() error) error {
	close(r.mutationAttempt)
	r.gate.Lock()
	defer r.gate.Unlock()
	r.CloseServer(id)
	return mutation()
}

func TestVaultDeleteSecret_DrainsMCPCallBeforeSuccessAndInvalidatesActivation(t *testing.T) {
	runtime := newBlockingVaultMCPRuntime()
	h := newMCPWireHarness(t, WithMCPRuntime(runtime))
	createdRaw := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.create", mcpStdioParams("Secret-bound", false, "mcp-secret-value")))
	var created mcpServerResult
	if err := json.Unmarshal(createdRaw, &created); err != nil {
		t.Fatal(err)
	}

	unaffected, unaffectedErr := h.store.CreateMCPServer(profile.MCPServer{
		Name:      "unaffected",
		Enabled:   true,
		Transport: profile.MCPTransportStdio,
		Stdio:     &profile.MCPStdioConfig{Command: "/bin/echo", Env: []profile.MCPEnvBinding{}},
		Limits: profile.MCPLimits{
			StartupTimeoutMS: 15_000, CallTimeoutMS: 60_000,
			IdleTimeoutMS: 30_000, MaxResultBytes: 262_144,
		},
	})
	if unaffectedErr != nil {
		t.Fatalf("create unrelated MCP server: %v", unaffectedErr)
	}

	inventoryRaw := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "vault.inventory", map[string]any{}))
	var inventory struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(inventoryRaw, &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Entries) != 1 {
		t.Fatalf("vault inventory contains %d entries, want the MCP-owned secret", len(inventory.Entries))
	}

	activation := mcp.Activation{ServerID: created.Server.ID}
	invokeDone := make(chan error, 1)
	go func() {
		_, err := runtime.Invoke(context.Background(), mcp.Invocation{Activation: activation})
		invokeDone <- err
	}()
	select {
	case <-runtime.start:
	case <-time.After(time.Second):
		t.Fatal("blocking MCP call did not start")
	}

	if err := h.conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0", "id": 8101, "method": "vault.deleteSecret",
		"params": map[string]any{"id": inventory.Entries[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	response := make(chan json.RawMessage, 1)
	responseErr := make(chan error, 1)
	go func() {
		for {
			var frame struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := h.conn.ReadJSON(&frame); err != nil {
				responseErr <- err
				return
			}
			if string(frame.ID) == "8101" {
				if len(frame.Error) != 0 {
					responseErr <- errors.New(string(frame.Error))
				} else {
					response <- frame.Result
				}
				return
			}
		}
	}()
	select {
	case <-runtime.mutationAttempt:
	case <-time.After(time.Second):
		t.Fatal("Vault deletion did not enter the per-server lifecycle barrier")
	}
	select {
	case <-response:
		t.Fatal("vault.deleteSecret succeeded while an MCP call was still active")
	case err := <-responseErr:
		t.Fatalf("reading pending delete response: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(runtime.release)
	if err := <-invokeDone; err != nil {
		t.Fatalf("blocking Invoke: %v", err)
	}
	select {
	case result := <-response:
		if string(result) != "{}" {
			t.Fatalf("delete result = %s, want {}", result)
		}
	case err := <-responseErr:
		t.Fatalf("vault.deleteSecret response: %v", err)
	case <-time.After(time.Second):
		t.Fatal("vault.deleteSecret did not respond after the MCP call drained")
	}

	updated, err := h.store.GetMCPServer(created.Server.ID)
	if err != nil {
		t.Fatalf("read invalidated MCP server: %v", err)
	}
	if updated.Revision != created.Server.Revision+1 || updated.Enabled {
		t.Fatalf("MCP server after secret deletion = revision %d enabled %v, want revision %d disabled",
			updated.Revision, updated.Enabled, created.Server.Revision+1)
	}
	runtime.mu.Lock()
	closed := runtime.closed[created.Server.ID]
	runtime.mu.Unlock()
	if !closed {
		t.Fatal("successful vault.deleteSecret left the MCP server session open")
	}
	if _, err := runtime.Invoke(context.Background(), mcp.Invocation{Activation: activation}); !errors.Is(err, mcp.ErrClosed) {
		t.Fatalf("new Invoke using the pre-delete activation error = %v, want ErrClosed", err)
	}

	var notification struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := h.conn.ReadJSON(&notification); err != nil {
		t.Fatalf("read mcpServers.changed: %v", err)
	}
	if notification.Method != "mcpServers.changed" {
		t.Fatalf("notification method = %q, want mcpServers.changed", notification.Method)
	}
	validateJSON(t, loadSchema(t, "mcpServers.changed.schema.json"), notification.Params, "vault.deleteSecret mcpServers.changed")
	var changed mcpServersChangedParams
	if err := json.Unmarshal(notification.Params, &changed); err != nil {
		t.Fatal(err)
	}
	if changed.ID != created.Server.ID || changed.Change != "updated" || changed.Revision <= created.Server.Revision {
		t.Fatalf("changed notification = %+v, want only updated server %q at a newer revision", changed, created.Server.ID)
	}
	runtime.mu.Lock()
	unaffectedClosed := runtime.closed[unaffected.ID]
	runtime.mu.Unlock()
	if unaffectedClosed {
		t.Fatalf("unreferenced server %s was included in Vault deletion mutation", unaffected.ID)
	}
	if err := h.conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var extra map[string]any
	if err := h.conn.ReadJSON(&extra); err == nil {
		t.Fatalf("unexpected additional notification after exact target %s: %v", created.Server.ID, extra)
	}
}

func TestVaultReplaceSecret_DrainsMCPCallAndClosesOldSession(t *testing.T) {
	runtime := newBlockingVaultMCPRuntime()
	h := newMCPWireHarness(t, WithMCPRuntime(runtime))
	createdRaw := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.create", mcpStdioParams("Replace-bound", false, "old-mcp-secret")))
	var created mcpServerResult
	if err := json.Unmarshal(createdRaw, &created); err != nil {
		t.Fatal(err)
	}
	inventoryRaw := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "vault.inventory", map[string]any{}))
	var inventory struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(inventoryRaw, &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Entries) != 1 {
		t.Fatalf("vault inventory contains %d entries, want one MCP-owned secret", len(inventory.Entries))
	}

	activation := mcp.Activation{ServerID: created.Server.ID}
	invokeDone := make(chan error, 1)
	go func() {
		_, err := runtime.Invoke(context.Background(), mcp.Invocation{Activation: activation})
		invokeDone <- err
	}()
	select {
	case <-runtime.start:
	case <-time.After(time.Second):
		t.Fatal("blocking MCP call did not start")
	}
	if err := h.conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0", "id": 8102, "method": "vault.replaceSecret",
		"params": map[string]any{"id": inventory.Entries[0].ID, "value": "new-mcp-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	response := make(chan json.RawMessage, 1)
	responseErr := make(chan error, 1)
	notifications := make(chan string, 4)
	go func() {
		for {
			var frame struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := h.conn.ReadJSON(&frame); err != nil {
				responseErr <- err
				return
			}
			if string(frame.ID) == "8102" {
				if len(frame.Error) != 0 {
					responseErr <- errors.New(string(frame.Error))
				} else {
					response <- frame.Result
				}
				return
			}
			notifications <- frame.Method
		}
	}()
	select {
	case <-runtime.mutationAttempt:
	case <-time.After(time.Second):
		t.Fatal("Vault replacement did not enter the per-server lifecycle barrier")
	}
	select {
	case <-response:
		t.Fatal("vault.replaceSecret succeeded while an MCP call was still active")
	case err := <-responseErr:
		t.Fatalf("reading pending replace response: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(runtime.release)
	if err := <-invokeDone; err != nil {
		t.Fatalf("blocking Invoke: %v", err)
	}
	select {
	case result := <-response:
		if string(result) != "{}" {
			t.Fatalf("replace result = %s, want {}", result)
		}
	case err := <-responseErr:
		t.Fatalf("vault.replaceSecret response: %v", err)
	case <-time.After(time.Second):
		t.Fatal("vault.replaceSecret did not respond after the MCP call drained")
	}
	runtime.mu.Lock()
	closed := runtime.closed[created.Server.ID]
	runtime.mu.Unlock()
	if !closed {
		t.Fatal("successful vault.replaceSecret left the old MCP session open")
	}
	if _, err := runtime.Invoke(context.Background(), mcp.Invocation{Activation: activation}); !errors.Is(err, mcp.ErrClosed) {
		t.Fatalf("Invoke after replacement error = %v, want ErrClosed for the old pooled session", err)
	}
	updated, err := h.store.GetMCPServer(created.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != created.Server.Revision || !updated.Enabled {
		t.Fatalf("replacement changed MCP record: revision=%d enabled=%v, want revision=%d enabled=true",
			updated.Revision, updated.Enabled, created.Server.Revision)
	}
	for {
		select {
		case method := <-notifications:
			if method == "mcpServers.changed" {
				t.Fatal("vault.replaceSecret emitted mcpServers.changed without changing the MCP record")
			}
		default:
			return
		}
	}
}
