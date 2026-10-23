package transport

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vaultreset"
)

type mcpWireSecrets struct {
	resolver credential.Resolver
	vault    *vault.Vault
}

func (s mcpWireSecrets) ResolveSecret(ctx context.Context, ref string) (credential.Secret, error) {
	return s.resolver.Resolve(ctx, credential.SecretID(ref), credential.Operation("connect to an MCP server"))
}

func (s mcpWireSecrets) ReplaceOAuthToken(ctx context.Context, ref string, value credential.Secret) error {
	return s.vault.ReplaceSystemSecret(ctx, credential.SecretID(ref), value)
}

type mcpWireHarness struct {
	store   *profile.JSONStore
	vault   *vault.Vault
	runtime *mcp.Manager
	server  *WSServer
	conn    *websocket.Conn
	next    int
	changes []mcpServersChangedParams
}

func newMCPWireHarness(t *testing.T) *mcpWireHarness {
	t.Helper()
	ctx, logger := logtest.New(t)
	v := adapterVault(t)
	store := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	secrets := mcpWireSecrets{resolver: credential.NewResolver(v, func(err error) bool { return errors.Is(err, vault.ErrVaultSealed) }, v), vault: v}
	var ws *WSServer
	runtime := mcp.NewManager(secrets, mcp.WithActivationVerifier(func(ctx context.Context, a mcp.Activation) error { return ws.VerifyMCPActivation(ctx, a) }))
	oauth := mcp.NewOAuthManager(v, secrets, runtime)
	// Deliberately install MCP before its repositories: option order is not wiring.
	ws = NewWSServer(logger, newRegWithStub(logger), WithMCPServices(runtime, runtime, oauth), WithProfileRepository(store), WithGroupRepository(store), WithCredentialStore(v), WithVaultLifecycle(v), WithVaultUnsealer(v), WithVaultReset(vaultreset.New(v, store, logtest.Slog(t))))
	v.SetUnlockRequester(ws)
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ws.Stop(closeCtx)
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &mcpWireHarness{store: store, vault: v, runtime: runtime, server: ws, conn: conn}
}

func (h *mcpWireHarness) request(t *testing.T, method string, params any) (json.RawMessage, *RPCError) {
	t.Helper()
	h.next++
	if err := h.conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": h.next, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
	if err := h.conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		_, raw, err := h.conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Method == "mcpServers.changed" {
			validateJSON(t, loadSchema(t, "mcpServers.changed.schema.json"), envelope.Params, "real notification")
			var change mcpServersChangedParams
			if err := json.Unmarshal(envelope.Params, &change); err != nil {
				t.Fatal(err)
			}
			h.changes = append(h.changes, change)
		}
		for _, secret := range []string{"wire-mcp-only-material", "wire-oauth-access", "wire-oauth-refresh", "wire-oauth-client", "sec:v1:", "secretRef"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("MCP response/notification exposed %q", secret)
			}
		}
		if envelope.ID == h.next {
			return envelope.Result, envelope.Error
		}
	}
}

func (h *mcpWireHarness) call(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	raw, rpcErr := h.request(t, method, params)
	if rpcErr != nil {
		t.Fatalf("%s: %+v", method, rpcErr)
	}
	validateJSON(t, loadSchema(t, method+".schema.json"), raw, method+" real result")
	return raw
}

func mcpWireServer(t *testing.T, raw json.RawMessage) mcpServerDTO {
	t.Helper()
	var result mcpServerResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result.Server
}

func mcpWireRevision(server mcpServerDTO) map[string]any {
	return map[string]any{"id": server.ID, "revision": server.Revision}
}

func mcpWireLimits() map[string]any {
	return map[string]any{"startupTimeoutMs": 5000, "callTimeoutMs": 5000, "idleTimeoutMs": 0, "maxResultBytes": 4096}
}

func mcpWireBinding(name string, literal, material any) map[string]any {
	kind := "literal"
	if material != nil {
		kind = "secret"
	}
	return map[string]any{"name": name, "value": map[string]any{"kind": kind, "literal": literal, "secret": nil, "secretValue": material, "keep": false}}
}

// A real child implements only the protocol this management path needs. No
// shell, network service, or developer profile participates in this fixture.
func TestMCPWireStdioProcess(t *testing.T) {
	if os.Getenv("NOCX_MCP_WIRE_HELPER") != "1" {
		return
	}
	events, err := os.OpenFile(os.Getenv("NOCX_MCP_EVENTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = events.Close() }()
	if os.Getenv("TOKEN") != "wire-mcp-only-material" {
		os.Exit(3)
	}
	_, _ = events.WriteString("start\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(4)
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": req.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "wire-fixture", "version": "1"}}
		case "tools/list":
			_, _ = events.WriteString("list\n")
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "fixture", "inputSchema": map[string]any{"type": "object", "additionalProperties": false}}}}
		default:
			if len(req.ID) != 0 {
				if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}}); err != nil {
					os.Exit(5)
				}
			}
			continue
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			os.Exit(5)
		}
	}
	_, _ = events.WriteString("stop\n")
	_ = events.Close()
	os.Exit(0)
}

func TestMCPServers_AllManagementResultsOverRealWire(t *testing.T) {
	h := newMCPWireHarness(t)
	var empty mcpServersListResult
	if err := json.Unmarshal(h.call(t, "mcpServers.list", map[string]any{}), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Servers) != 0 {
		t.Fatal("a new profile already contains MCP servers")
	}
	executable, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}
	events := filepath.Join(t.TempDir(), "events")
	secretEnv := mcpWireBinding("TOKEN", nil, "wire-mcp-only-material")
	params := map[string]any{
		"name": "stdio fixture", "enabled": true, "transport": "stdio", "http": nil,
		"stdio": map[string]any{"command": executable, "argv": []string{"-test.run=^TestMCPWireStdioProcess$"}, "cwd": "", "env": []any{
			mcpWireBinding("NOCX_MCP_WIRE_HELPER", "1", nil), mcpWireBinding("NOCX_MCP_EVENTS", events, nil), secretEnv,
		}}, "limits": mcpWireLimits(),
	}
	created := mcpWireServer(t, h.call(t, "mcpServers.create", params))
	got := mcpWireServer(t, h.call(t, "mcpServers.get", map[string]any{"id": created.ID}))
	if got.ID != created.ID || got.Catalog.State != profile.MCPCatalogMissing {
		t.Fatal("create/get lost dormant record")
	}
	if _, err := os.Stat(events); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("create/get activated stdio")
	}
	refreshed := mcpWireServer(t, h.call(t, "mcpServers.refresh", mcpWireRevision(got)))
	if len(refreshed.Catalog.Tools) != 1 || refreshed.Catalog.Tools[0].Enabled {
		t.Fatal("discovery did not install a disabled tool")
	}
	trace, err := os.ReadFile(events) // #nosec G304 -- the event file belongs to this test's private temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	if string(trace) != "start\nlist\nstop\n" {
		t.Fatalf("discovery did not close its one child: %q", trace)
	}
	toggle := mcpWireRevision(refreshed)
	toggle["tools"] = []string{"echo"}
	enabled := mcpWireServer(t, h.call(t, "mcpServers.setToolsEnabled", toggle))
	if !enabled.Catalog.Tools[0].Enabled {
		t.Fatal("tool could not be enabled")
	}
	params["id"], params["revision"], params["name"] = enabled.ID, enabled.Revision, "renamed"
	binding, ok := secretEnv["value"].(map[string]any)
	if !ok {
		t.Fatal("secret binding fixture has no value object")
	}
	binding["secretValue"], binding["keep"] = nil, true
	updated := mcpWireServer(t, h.call(t, "mcpServers.update", params))
	if updated.Name != "renamed" {
		t.Fatal("configuration update did not persist")
	}
	if _, conflict := h.request(t, "mcpServers.update", params); conflict == nil || conflict.Code != -32602 || !strings.Contains(string(mustMarshal(conflict.Data)), "conflict") {
		t.Fatalf("stale update did not report conflict: %+v", conflict)
	}
	h.call(t, "mcpServers.delete", mcpWireRevision(updated))
	h.call(t, "mcpServers.list", map[string]any{}) // queue ordering drains the delete notification, not a timing assertion.
	if len(h.changes) != 5 {
		t.Fatalf("mutations announced %d changes, want create/refresh/toggle/update/delete", len(h.changes))
	}
	if _, missing := h.request(t, "mcpServers.get", map[string]any{"id": created.ID}); missing == nil || missing.Code != -32602 {
		t.Fatal("deleted server remained readable")
	}

	fixture, presented := newMCPWireOAuthFixture(t)
	h.server.SetUrlOpener(mcpWireURLOpener{presented: presented})
	httpParams := map[string]any{"name": "oauth fixture", "enabled": true, "transport": "streamable-http", "stdio": nil, "limits": mcpWireLimits(), "http": map[string]any{
		"endpoint": fixture.URL + "/mcp", "auth": "oauth", "headers": []any{}, "bearer": nil,
		"oauth": map[string]any{"registration": "dynamic", "clientId": "", "clientSecret": nil, "scopes": []string{"tools"}},
	}}
	httpServer := mcpWireServer(t, h.call(t, "mcpServers.create", httpParams))
	connected := mcpWireServer(t, h.call(t, "mcpServers.oauthAuthorize", mcpWireRevision(httpServer)))
	if connected.HTTP.OAuth.Status != profile.MCPOAuthConnected || presented.Load() != 1 {
		t.Fatal("explicit OAuth did not connect exactly once")
	}
	stored, err := h.store.GetMCPServer(connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	sessionRef := stored.HTTP.OAuth.SessionRef.SecretRef
	if _, err := h.vault.Get(t.Context(), credential.SecretID(sessionRef)); err != nil {
		t.Fatal("connected OAuth material is unavailable")
	}
	inventory := h.call(t, "vault.inventory", map[string]any{})
	if strings.Contains(string(inventory), vault.RowFor(credential.SecretID(sessionRef))) {
		t.Fatal("system-only OAuth material entered ordinary inventory")
	}
	httpCatalog := mcpWireServer(t, h.call(t, "mcpServers.refresh", mcpWireRevision(connected)))
	if len(httpCatalog.Catalog.Tools) != 1 || presented.Load() != 1 {
		t.Fatal("authorized HTTP discovery failed or opened another browser")
	}
	h.vault.Seal()
	forgotten := mcpWireServer(t, h.call(t, "mcpServers.oauthForget", mcpWireRevision(httpCatalog)))
	if forgotten.HTTP.OAuth.Status != profile.MCPOAuthMissing {
		t.Fatal("forget retained connected status")
	}
	if h.vault.State() != vault.StateSealed || presented.Load() != 1 {
		t.Fatal("forget unlocked Vault or started authorization")
	}
	if err := h.vault.Unseal(t.Context(), vault.UnsealRequest{Passphrase: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.vault.Get(t.Context(), credential.SecretID(sessionRef)); err == nil {
		t.Fatal("forget retained its persisted token")
	}
}

type mcpWireURLOpener struct{ presented *atomic.Int32 }

func (p mcpWireURLOpener) OpenURL(ctx context.Context, target string) error {
	p.presented.Add(1)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request) // fixture URL only, native presenter stand-in
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, err = io.Copy(io.Discard, response.Body)
	return err
}

func newMCPWireOAuthFixture(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	remote := sdk.NewServer(&sdk.Implementation{Name: "wire-http", Version: "1"}, nil)
	remote.AddTool(&sdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "fixture result"}}}, nil
	})
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return remote }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	var base string
	var challenge string
	var mu sync.Mutex
	presented := &atomic.Int32{}
	writeJSON := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("OAuth fixture request: %s %s", r.Method, r.URL.Path)
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
			writeJSON(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "registration_endpoint": base + "/register", "code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true})
		case "/register":
			writeJSON(w, map[string]any{"client_id": "wire-client", "client_secret": "wire-oauth-client"})
		case "/authorize":
			if r.URL.Query().Get("code_challenge_method") != "S256" || r.URL.Query().Get("state") == "" {
				http.Error(w, "missing PKCE/state", 400)
				return
			}
			mu.Lock()
			challenge = r.URL.Query().Get("code_challenge")
			mu.Unlock()
			query := url.Values{"code": {"fixture-code"}, "state": {r.URL.Query().Get("state")}, "iss": {base}}
			http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?"+query.Encode(), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid form", 400)
				return
			}
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			mu.Lock()
			expected := challenge
			mu.Unlock()
			if r.Form.Get("code") != "fixture-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != expected {
				http.Error(w, "PKCE mismatch", 400)
				return
			}
			writeJSON(w, map[string]any{"access_token": "wire-oauth-access", "refresh_token": "wire-oauth-refresh", "token_type": "Bearer", "expires_in": 3600})
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer wire-oauth-access" {
				http.Error(w, "unauthorized", 401)
				return
			}
			mcpHandler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	base = server.URL
	t.Cleanup(server.Close)
	return server, presented
}
