package app

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

type appMCPRuntime struct {
	closed int
}

func (*appMCPRuntime) Refresh(context.Context, mcp.Activation) (mcp.Catalog, error) {
	return mcp.Catalog{}, nil
}

func (*appMCPRuntime) Invoke(context.Context, mcp.Invocation) (mcp.Result, error) {
	return mcp.Result{}, nil
}

func (*appMCPRuntime) CloseRun(string)    {}
func (*appMCPRuntime) CloseServer(string) {}
func (r *appMCPRuntime) Close() error {
	r.closed++
	return nil
}

type mcpMaterialStore struct {
	got credential.SecretID
}

func (*mcpMaterialStore) Create(context.Context, credential.Secret) (credential.SecretID, error) {
	return "", nil
}
func (*mcpMaterialStore) Delete(context.Context, credential.SecretID) error { return nil }
func (*mcpMaterialStore) Exists(context.Context, credential.SecretID) (bool, error) {
	return true, nil
}

func (s *mcpMaterialStore) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	s.got = id
	return credential.NewSecret("resolved-material"), nil
}

type mcpUnsealer struct {
	reason string
}

func (u *mcpUnsealer) EnsureUnsealed(_ context.Context, reason string) error {
	u.reason = reason
	return nil
}

func TestMCPSecretMaterialUsesOperationStance(t *testing.T) {
	store := &mcpMaterialStore{}
	unsealer := &mcpUnsealer{}
	resolver := credential.NewResolver(store, func(error) bool { return false }, unsealer)
	secret, err := (mcpSecretMaterial{resolver: resolver}).ResolveSecret(t.Context(), "secrow:mcp-token")
	if err != nil {
		t.Fatalf("ResolveSecret: %v", err)
	}
	if store.got != credential.SecretID("secrow:mcp-token") {
		t.Fatalf("resolved id = %q, want opaque MCP binding", store.got)
	}
	if unsealer.reason != "connect to an MCP server" {
		t.Fatalf("unlock reason = %q, want the MCP operation named", unsealer.reason)
	}
	if secret.IsEmpty() {
		t.Fatal("stanced resolver returned empty material")
	}
}

func TestMCPCompositionRootExposesPersistedServersWithoutDiscovery(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.mcpRuntime == nil {
		t.Fatal("composition root constructed no MCP runtime")
	}
	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer a.Shutdown(ctx)
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()
	response := callAppWS(t, conn, "mcpServers.list", map[string]any{}, 1)
	if response.Error != nil {
		t.Fatalf("mcpServers.list is not wired at the composition root: %+v", response.Error)
	}
	if string(response.Result) != `{"servers":[]}` {
		t.Fatalf("mcpServers.list = %s, want dormant empty persisted catalog", response.Result)
	}
}

func TestShutdownClosesMCPRuntime(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	original := a.mcpRuntime
	fake := &appMCPRuntime{}
	a.mcpRuntime = fake
	a.Shutdown(context.Background())
	if original != nil {
		_ = original.Close()
	}
	if fake.closed != 1 {
		t.Fatalf("MCP runtime Close calls = %d, want one at app shutdown", fake.closed)
	}
}

var (
	_ credential.MaterialStore = (*mcpMaterialStore)(nil)
	_ credential.Unsealer      = (*mcpUnsealer)(nil)
	_ mcp.Runtime              = (*appMCPRuntime)(nil)
)
