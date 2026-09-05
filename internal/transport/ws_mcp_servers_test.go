package transport

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

type mcpWireHarness struct {
	store *profile.JSONStore
	conn  *websocket.Conn
}

func newMCPWireHarness(t *testing.T, extra ...WSServerOption) mcpWireHarness {
	t.Helper()
	dir := t.TempDir()
	documents := storage.NewDocumentStore(dir)
	providers, err := vault.NewRegistry(file.New(documents, "vault.json"))
	if err != nil {
		t.Fatalf("vault registry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	secrets, err := vault.New(documents, providers, logger)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	t.Cleanup(secrets.Close)
	if _, err := secrets.Setup(t.Context(), vault.SetupRequest{Passphrase: "test passphrase"}); err != nil {
		t.Fatalf("setup vault: %v", err)
	}
	store := profile.NewJSONStore(filepath.Join(dir, "profiles.json"))
	options := []WSServerOption{
		WithProfileRepository(store), WithGroupRepository(store), WithMCPServerRepository(store),
		WithCredentialStore(secrets), WithVaultLifecycle(secrets),
	}
	options = append(options, extra...)
	server := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), options...)
	if err := server.Start(t.Context()); err != nil {
		t.Fatalf("start websocket server: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(t.Context()) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	return mcpWireHarness{store: store, conn: conn}
}

func mcpStdioParams(name string, keep bool, secretValue any) map[string]any {
	return map[string]any{
		"name": name, "enabled": true, "transport": "stdio", "http": nil,
		"stdio": map[string]any{
			"command": "/usr/bin/printf", "argv": []string{"ok"}, "cwd": "",
			"env": []map[string]any{{
				"name":  "TOKEN",
				"value": map[string]any{"kind": "secret", "literal": nil, "secret": nil, "secretValue": secretValue, "keep": keep},
			}},
		},
		"limits": map[string]any{"startupTimeoutMs": 15000, "callTimeoutMs": 60000, "idleTimeoutMs": 30000, "maxResultBytes": 262144},
	}
}

func mcpResultEnvelope(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode JSON-RPC envelope: %v\n%s", err, raw)
	}
	if envelope.Error != nil {
		t.Fatalf("JSON-RPC error: %+v\n%s", *envelope.Error, raw)
	}
	return envelope.Result
}

func assertMCPWireHasNoSecrets(t *testing.T, raw []byte) {
	t.Helper()
	text := string(raw)
	for _, forbidden := range []string{"wire-secret-material", "secretRef", "sec:v1:", "secrow:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("MCP wire result leaked %q: %s", forbidden, text)
		}
	}
}

func TestMCPServers_CRUDOverWireUsesCASAndNeverReturnsSecrets(t *testing.T) {
	h := newMCPWireHarness(t)

	create := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.create", mcpStdioParams("Local tools", false, "wire-secret-material")))
	validateJSON(t, loadSchema(t, "mcpServers.create.schema.json"), create, "mcpServers.create result")
	assertMCPWireHasNoSecrets(t, create)
	var created mcpServerResult
	if err := json.Unmarshal(create, &created); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	if created.Server.ID == "" || created.Server.Revision != 1 || created.Server.Catalog.State != profile.MCPCatalogMissing {
		t.Fatalf("created identity/revision/catalog = %q/%d/%q", created.Server.ID, created.Server.Revision, created.Server.Catalog.State)
	}
	if created.Server.Stdio == nil || len(created.Server.Stdio.Env) != 1 || !created.Server.Stdio.Env[0].Value.SecretSet {
		t.Fatalf("sanitized secret presence missing: %+v", created.Server.Stdio)
	}

	get := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.get", map[string]any{"id": created.Server.ID}))
	validateJSON(t, loadSchema(t, "mcpServers.get.schema.json"), get, "mcpServers.get result")
	assertMCPWireHasNoSecrets(t, get)

	updateParams := mcpStdioParams("Renamed tools", true, nil)
	updateParams["id"], updateParams["revision"] = created.Server.ID, created.Server.Revision
	update := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.update", updateParams))
	validateJSON(t, loadSchema(t, "mcpServers.update.schema.json"), update, "mcpServers.update result")
	assertMCPWireHasNoSecrets(t, update)
	var updated mcpServerResult
	if err := json.Unmarshal(update, &updated); err != nil {
		t.Fatalf("decode update result: %v", err)
	}
	if updated.Server.Revision != 2 || updated.Server.Name != "Renamed tools" {
		t.Fatalf("updated revision/name = %d/%q", updated.Server.Revision, updated.Server.Name)
	}

	conflictRaw := jsonrpcCall(t, h.conn, "mcpServers.delete", map[string]any{"id": created.Server.ID, "revision": 1})
	var conflict struct {
		Error *struct {
			Code int          `json:"code"`
			Data mcpErrorData `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(conflictRaw, &conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if conflict.Error == nil || conflict.Error.Code != -32602 || conflict.Error.Data.Reason != "conflict" {
		t.Fatalf("stale delete error = %s", conflictRaw)
	}

	list := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.list", nil))
	validateJSON(t, loadSchema(t, "mcpServers.list.schema.json"), list, "mcpServers.list result")
	assertMCPWireHasNoSecrets(t, list)
	if strings.Contains(string(list), "inputSchema") {
		t.Fatalf("bounded list returned catalog schemas: %s", list)
	}

	deleted := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.delete", map[string]any{"id": created.Server.ID, "revision": 2}))
	validateJSON(t, loadSchema(t, "mcpServers.delete.schema.json"), deleted, "mcpServers.delete result")
	assertMCPWireHasNoSecrets(t, deleted)
	servers, err := h.store.ListMCPServers()
	if err != nil || len(servers) != 0 {
		t.Fatalf("servers after delete = %+v, err=%v", servers, err)
	}
}

func TestMCPServers_RegistrationValidationAndUnavailableRuntime(t *testing.T) {
	h := newMCPWireHarness(t)
	for _, method := range []string{
		"mcpServers.list", "mcpServers.get", "mcpServers.create", "mcpServers.update", "mcpServers.delete",
		"mcpServers.refresh", "mcpServers.setToolsEnabled", "mcpServers.oauthAuthorize", "mcpServers.oauthForget",
	} {
		if _, ok := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithMCPServerRepository(h.store)).methods[method]; !ok {
			t.Errorf("method %q is not registered", method)
		}
	}

	invalid := jsonrpcCall(t, h.conn, "mcpServers.get", map[string]any{"id": "missing", "unknown": true})
	var invalidEnvelope struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(invalid, &invalidEnvelope); err != nil {
		t.Fatal(err)
	}
	if invalidEnvelope.Error == nil || invalidEnvelope.Error.Code != -32602 {
		t.Fatalf("unknown params were not rejected: %s", invalid)
	}

	create := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.create", mcpStdioParams("Refresh target", false, "wire-secret-material")))
	var created mcpServerResult
	if err := json.Unmarshal(create, &created); err != nil {
		t.Fatal(err)
	}
	raw := jsonrpcCall(t, h.conn, "mcpServers.refresh", map[string]any{"id": created.Server.ID, "revision": created.Server.Revision})
	var unavailable struct {
		Error *struct {
			Code int          `json:"code"`
			Data mcpErrorData `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &unavailable); err != nil {
		t.Fatal(err)
	}
	if unavailable.Error == nil || unavailable.Error.Code != -32603 || unavailable.Error.Data.Reason != "runtime-unavailable" {
		t.Fatalf("refresh unavailable error = %s", raw)
	}
}

type mcpRefreshStub struct {
	catalog mcp.Catalog
	calls   int
}

func (s *mcpRefreshStub) Refresh(_ context.Context, _ mcp.Activation) (mcp.Catalog, error) {
	s.calls++
	return s.catalog, nil
}

func TestMCPServers_RefreshDiscoversAndCASCommitsCatalog(t *testing.T) {
	refresher := &mcpRefreshStub{catalog: mcp.Catalog{
		ServerName:      "fixture",
		ServerVersion:   "1.0",
		ProtocolVersion: "2025-11-25",
		RefreshedAt:     time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
		Digest:          "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tools:           []mcp.ToolDescriptor{},
	}}
	h := newMCPWireHarness(t, WithMCPServerRefresher(refresher))
	create := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.create", mcpStdioParams("Refreshable", false, "wire-secret-material")))
	var created mcpServerResult
	if err := json.Unmarshal(create, &created); err != nil {
		t.Fatal(err)
	}
	refresh := mcpResultEnvelope(t, jsonrpcCall(t, h.conn, "mcpServers.refresh", map[string]any{"id": created.Server.ID, "revision": 1}))
	validateJSON(t, loadSchema(t, "mcpServers.refresh.schema.json"), refresh, "mcpServers.refresh result")
	assertMCPWireHasNoSecrets(t, refresh)
	var refreshed mcpServerResult
	if err := json.Unmarshal(refresh, &refreshed); err != nil {
		t.Fatal(err)
	}
	if refresher.calls != 1 || refreshed.Server.Revision != 2 || refreshed.Server.Catalog.State != profile.MCPCatalogFresh {
		t.Fatalf("refresh calls/revision/state = %d/%d/%q", refresher.calls, refreshed.Server.Revision, refreshed.Server.Catalog.State)
	}
}
