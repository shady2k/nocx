package agenttools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/profile"
)

// Exercise the identity Settings actually persists, rather than an invented
// server-a ID that cannot expose disagreement with profile's ID generator.
func TestMCPGeneratedStdioIdentitySurvivesPolicyParseAndNarrow(t *testing.T) {
	repo := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	fixture := mcpServerFixture(profile.MCPCatalogFresh)
	server, err := repo.CreateMCPServer(profile.MCPServer{
		Name: "Local catalog", Enabled: true, Transport: profile.MCPTransportStdio,
		Stdio: fixture.Stdio, Limits: fixture.Limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(server.ID, "mcp:") {
		t.Fatalf("unexpected generated identity %q", server.ID)
	}
	server, err = repo.RefreshMCPServerCatalog(server.ID, server.Revision, profile.MCPCatalog{Tools: fixture.Catalog.Tools})
	if err != nil {
		t.Fatal(err)
	}
	server, err = repo.SetMCPToolsEnabled(server.ID, server.Revision, []string{"echo"})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := server.CanonicalDestination()
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "mcp+stdio:"+server.ID {
		t.Fatalf("opaque identity rewritten: %q", canonical)
	}
	snapshot, err := NewMCPCatalogSnapshot(server)
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := snapshot.GrantScopes()
	if err != nil || len(scopes) != 1 || scopes[0].ID != canonical {
		t.Fatalf("snapshot scopes = %+v, %v", scopes, err)
	}
	if scopeErr := content.ValidateGrantScope(scopes[0]); scopeErr != nil {
		t.Fatalf("generated destination refused: %v", scopeErr)
	}

	ask := content.EffectRow{Decision: content.DecisionAsk}
	policy := content.EffectPolicy{
		Observe: ask, MutateReversible: ask, MutateDestructive: ask,
		PrivilegeChange: ask, Disclose: ask, CrossBoundary: ask,
		Delegate: content.EffectRow{Decision: content.DecisionPermit, Scopes: scopes},
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := content.ParseEffectPolicy(encoded)
	if err != nil {
		t.Fatalf("Settings policy round trip rejected generated destination: %v", err)
	}
	if len(parsed.Delegate.Scopes) != 1 || parsed.Delegate.Scopes[0] != scopes[0] {
		t.Fatal("policy parsing changed opaque identity")
	}
	grant := parsed.AsGrant(scopes)
	registry, err := (Registry{}).WithMCP([]MCPCatalogSnapshot{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	offered := registry.ForGrant(grant)
	if len(offered) != 1 {
		t.Fatalf("generated server's tool not offered: %v", toolNames(offered))
	}
	resources, err := offered[0].ResolveResources(nil, RunContext{})
	if err != nil || len(resources) != 1 || resources[0].ID != canonical {
		t.Fatalf("resolved destination changed: %+v %v", resources, err)
	}
	capability, err := offered[0].Narrow(grant, resources, RunContext{RunID: "generated-id-run"})
	if err != nil {
		t.Fatalf("generated destination could not narrow: %v", err)
	}
	scope, ok := capability.(*MCPScope)
	if !ok {
		t.Fatalf("capability = %T, want *MCPScope", capability)
	}
	invocation, err := scope.Invocation(json.RawMessage(`{}`))
	if err != nil || invocation.Activation.ServerID != server.ID {
		t.Fatalf("invocation changed identity: %+v %v", invocation, err)
	}

	for _, parent := range []content.GrantScope{
		{Kind: content.ResourceDestination, ID: "*"},
		{Kind: content.ResourceDestination, ID: "mcp+stdio:mcp:local-catalog"},
		{Kind: content.ResourceDestination, ID: canonical + ":other"},
		{Kind: content.ResourceDestination, ID: canonical, IncludeSubdomains: true},
	} {
		if parent.Contains(scopes[0]) || scopes[0].Contains(parent) {
			t.Errorf("nonidentical scope matched in either direction: %+v", parent)
		}
	}
	for _, id := range []string{
		canonical + "/*", canonical + "?tool=echo", canonical + "#echo",
		canonical + "%3Aother", "mcp+stdio:https://example.test/mcp", "mcp+stdio://example.test",
	} {
		invalid := content.GrantScope{Kind: content.ResourceDestination, ID: id}
		if content.ValidateGrantScope(invalid) == nil {
			t.Errorf("accepted URL/path/query or encoded alias %q", id)
		}
		if scopes[0].Contains(invalid) || invalid.Contains(scopes[0]) {
			t.Errorf("malformed identity matched in either direction: %q", id)
		}
	}
	if !scopes[0].Contains(scopes[0]) {
		t.Fatal("exact generated identity does not match itself")
	}
}
