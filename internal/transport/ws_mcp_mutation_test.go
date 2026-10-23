package transport

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// This interleaving is another admitted configuration write after the original
// snapshot, before the committing admission. It still uses the real manager's
// barrier and the real guarded aggregate/Vault, rather than echoing a callback.
type mcpMutationInterleaving struct {
	runner mcp.MutationRunner
	once   sync.Once
	before func(context.Context) error
}

func (r *mcpMutationInterleaving) RunServerMutations(ctx context.Context, ids []string, commit func(context.Context) error) error {
	return r.runner.RunServerMutations(ctx, ids, func(ctx context.Context) error {
		var err error
		r.once.Do(func() { err = r.before(ctx) })
		if err != nil {
			return err
		}
		return commit(ctx)
	})
}

func TestMCPSecretMutationsRecheckNewHoldersBeforeCommit(t *testing.T) {
	for _, method := range []string{"vault.replaceSecret", "vault.deleteSecret", "vault.reset", "endpoints.update", "endpoints.delete"} {
		t.Run(method, func(t *testing.T) {
			h := newMCPWireHarness(t)
			params := map[string]any{"name": "first holder", "enabled": true, "transport": "stdio", "http": nil, "limits": mcpWireLimits(), "stdio": map[string]any{
				"command": "/usr/bin/true", "argv": []string{}, "cwd": "", "env": []any{mcpWireBinding("TOKEN", nil, "wire-mcp-only-material")},
			}}
			created := mcpWireServer(t, h.call(t, "mcpServers.create", params))
			stored, readErr := h.store.GetMCPServer(created.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			ref := stored.Stdio.Env[0].Value.SecretRef
			row := vault.RowFor(credential.SecretID(ref))
			endpoint := profile.Endpoint{ID: "endpoint:custom:shared:1", Name: "Shared endpoint", BaseURL: "https://example.test/v1", Schema: profile.EndpointSchemaOpenAICompatible, CredentialRef: ref, Models: []profile.EndpointModel{{Name: "fixture"}}}
			if err := h.store.CreateEndpoint(endpoint); err != nil {
				t.Fatal(err)
			}
			var newHolder profile.MCPServer
			h.server.mcpMutations.runner = &mcpMutationInterleaving{runner: h.runtime, before: func(ctx context.Context) error {
				return h.server.mcpMutations.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
					input := capability.MCPServerConfigInput{
						Name: "new holder", Enabled: true, Transport: profile.MCPTransportStdio, Limits: stored.Limits,
						Stdio: &capability.MCPStdioInput{Command: "/usr/bin/true", Argv: []string{}, Env: []capability.MCPEnvBindingInput{{Name: "TOKEN", Value: capability.MCPValueBindingInput{Kind: profile.MCPBindingSecret, Secret: &row}}}},
					}
					var err error
					newHolder, err = svc.CreateMCPServer(ctx, input)
					return err
				})
			}}
			request := map[string]any{"id": row}
			switch method {
			case "vault.replaceSecret":
				request["value"] = "replaced-material"
			case "vault.reset":
				request = map[string]any{}
			case "endpoints.delete":
				request = map[string]any{"id": endpoint.ID}
			case "endpoints.update":
				request = map[string]any{"id": endpoint.ID, "name": endpoint.Name, "baseUrl": endpoint.BaseURL, "schema": endpoint.Schema, "noKey": false, "key": "replaced-material", "models": []map[string]any{{"name": "fixture", "alias": nil}}}
			}
			_, rpcErr := h.request(t, method, request)
			if rpcErr == nil || rpcErr.Code != -32602 {
				t.Fatalf("new holder did not cause a visible conflict: %+v", rpcErr)
			}
			data, marshalErr := json.Marshal(rpcErr.Data)
			if marshalErr != nil || string(data) != `{"reason":"conflict"}` {
				t.Fatalf("conflict classification: %s, %v", data, marshalErr)
			}
			for _, expected := range []profile.MCPServer{stored, newHolder} {
				actual, err := h.store.GetMCPServer(expected.ID)
				if err != nil {
					t.Fatal(err)
				}
				if actual.Revision != expected.Revision || len(actual.Stdio.Env) != 1 || actual.Stdio.Env[0].Value.SecretRef != ref {
					t.Fatal("conflicted mutation changed a holder")
				}
			}
			material, materialErr := h.vault.Get(t.Context(), credential.SecretID(ref))
			if materialErr != nil {
				t.Fatal("conflicted mutation deleted shared material")
			}
			if err := material.Use(func(value []byte) error {
				if string(value) != "wire-mcp-only-material" {
					t.Error("conflicted mutation rotated shared material")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			endpoints, err := h.store.LoadEndpoints()
			if err != nil || len(endpoints) != 1 || endpoints[0].ID != endpoint.ID {
				t.Fatal("conflicted mutation removed its endpoint target")
			}
		})
	}
}

type mcpPendingAuthorization struct {
	opened    chan struct{}
	cancelled chan struct{}
}

func (p mcpPendingAuthorization) OpenURL(ctx context.Context, _ string) error {
	close(p.opened)
	<-ctx.Done()
	close(p.cancelled)
	return ctx.Err()
}

func TestVaultResetDrainsFirstOAuthAuthorizationWithoutChangingEmptyMetadata(t *testing.T) {
	h := newMCPWireHarness(t)
	fixture, _ := newMCPWireOAuthFixture(t)
	presenter := mcpPendingAuthorization{opened: make(chan struct{}), cancelled: make(chan struct{})}
	h.server.SetUrlOpener(presenter)
	params := map[string]any{"name": "first authorization", "enabled": true, "transport": "streamable-http", "stdio": nil, "limits": mcpWireLimits(), "http": map[string]any{
		"endpoint": fixture.URL + "/mcp", "auth": "oauth", "headers": []any{}, "bearer": nil,
		"oauth": map[string]any{"registration": "dynamic", "clientId": "", "clientSecret": nil, "scopes": []string{"tools"}},
	}}
	server := mcpWireServer(t, h.call(t, "mcpServers.create", params))
	auth := connectWS(t, h.server)
	defer func() { _ = auth.Close() }()
	if err := auth.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "mcpServers.oauthAuthorize", "params": mcpWireRevision(server)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-presenter.opened:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth did not reach its explicit browser wait")
	}
	reset := &mcpWireHarness{conn: connectWS(t, h.server)}
	defer func() { _ = reset.conn.Close() }()
	raw := reset.call(t, "vault.reset", map[string]any{})
	var result vaultResetResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	select {
	case <-presenter.cancelled:
	default:
		t.Fatal("reset responded while credential-producing authorization remained active")
	}
	if result.MCPServerCount != 0 || result.SecretCount != 0 {
		t.Fatal("pending authorization inflated stored-reference impact")
	}
	if h.vault.State() != vault.StateUninitialized {
		t.Fatal("reset did not purge Vault")
	}
	stored, err := h.store.GetMCPServer(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != server.Revision || stored.HTTP.OAuth.SessionRef != nil {
		t.Fatal("reset changed an unbound server or committed cancelled authority")
	}
	reset.call(t, "mcpServers.list", map[string]any{})
	if len(reset.changes) != 0 {
		t.Fatal("reset announced a metadata mutation for an unbound server")
	}
	if err := auth.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var response struct {
			ID    int       `json:"id"`
			Error *RPCError `json:"error"`
		}
		if err := auth.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response.ID != 1 {
			continue
		}
		if response.Error == nil {
			t.Fatal("cancelled authorization reported success")
		}
		break
	}
}
