package transport

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vaultreset"
)

// Reset must remain useful while sealed and without a lifecycle surface wired:
// the real service removes MCP references before purging the real file provider.
func TestVaultReset_OverTheWireConformsToContract(t *testing.T) {
	ctx, logger := logtest.New(t)
	v := adapterVault(t)
	id, err := v.CreateNamed(ctx, credential.NewSecret("disposable-mcp-reset-token"), vault.SecretMeta{Name: "MCP fixture", Kind: vault.KindPassword})
	if err != nil {
		t.Fatal(err)
	}
	store := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	mode := "local"
	input := profile.MCPServer{
		Name: "credential holder", Enabled: true, Transport: profile.MCPTransportStdio,
		Stdio: &profile.MCPStdioConfig{Command: "/usr/bin/true", Argv: []string{}, Env: []profile.MCPEnvBinding{
			{Name: "MCP_TOKEN", Value: profile.MCPValueBinding{Kind: profile.MCPBindingSecret, SecretRef: string(id), Owned: true}},
			{Name: "MODE", Value: profile.MCPValueBinding{Kind: profile.MCPBindingLiteral, Literal: &mode}},
		}},
		Limits: profile.MCPLimits{StartupTimeoutMS: 1000, CallTimeoutMS: 1000, IdleTimeoutMS: 0, MaxResultBytes: 1024},
	}
	affected, err := store.CreateMCPServer(input)
	if err != nil {
		t.Fatal(err)
	}
	if attachErr := v.AttachTarget(ctx, id, affected.ID); attachErr != nil {
		t.Fatal(attachErr)
	}
	if commitErr := v.CommitMetadata(ctx, id); commitErr != nil {
		t.Fatal(commitErr)
	}
	input.Name = "no credentials"
	input.Stdio.Env = input.Stdio.Env[1:]
	unaffected, err := store.CreateMCPServer(input)
	if err != nil {
		t.Fatal(err)
	}
	v.Seal()

	service := vaultreset.New(v, store, logtest.Slog(t))
	ws := NewWSServer(logger, newRegWithStub(logger), WithVaultReset(service))
	if startErr := ws.Start(ctx); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)

	previewResp := vaultCall(t, conn, "vault.resetPreview", map[string]any{}, 1)
	if previewResp.Error != nil {
		t.Fatalf("vault.resetPreview: %+v", previewResp.Error)
	}
	validateJSON(t, loadSchema(t, "vault.resetPreview.schema.json"), previewResp.Result, "real MCP reset preview")
	var preview vaultResetPreviewResponse
	if decodeErr := json.Unmarshal(previewResp.Result, &preview); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if preview.MCPServerCount != 1 || preview.SecretCount != 1 || !preview.VaultInitialized {
		t.Fatalf("preview omitted the credential holder: %+v", preview)
	}

	resetResp := vaultCall(t, conn, "vault.reset", map[string]any{}, 2)
	if resetResp.Error != nil {
		t.Fatalf("vault.reset: %+v", resetResp.Error)
	}
	validateJSON(t, loadSchema(t, "vault.reset.schema.json"), resetResp.Result, "real MCP reset result")
	var result vaultResetResponse
	if decodeErr := json.Unmarshal(resetResp.Result, &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if result.MCPServerCount != 1 || result.SecretCount != 1 || len(result.Residue) != 0 {
		t.Fatalf("reset misreported the destroyed material: %+v", result)
	}
	if v.State() != vault.StateUninitialized {
		t.Fatal("reset response preceded vault destruction")
	}
	cleared, err := store.GetMCPServer(affected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Revision != affected.Revision+1 || cleared.Enabled || cleared.Stdio.Command != affected.Stdio.Command || len(cleared.Stdio.Env) != 1 || cleared.Stdio.Env[0].Name != "MODE" || *cleared.Stdio.Env[0].Value.Literal != mode {
		t.Fatalf("reset retained authority or destroyed literal configuration: %+v", cleared)
	}
	kept, err := store.GetMCPServer(unaffected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Revision != unaffected.Revision || !kept.Enabled {
		t.Fatal("reset changed a server with no secret bindings")
	}
}
