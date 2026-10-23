package agenttools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/profile"
)

func mcpServerFixture(state profile.MCPCatalogState) profile.MCPServer {
	input := json.RawMessage(`{"type":"object","additionalProperties":false,"required":[],"properties":{}}`)
	canonical, err := profile.CanonicalMCPToolDescriptor(profile.MCPTool{Name: "echo", InputSchema: input})
	if err != nil {
		panic(err)
	}
	input = canonical.InputSchema
	descriptorDigest := canonical.DescriptorDigest
	catalogDigest := sha256.New()
	_, _ = catalogDigest.Write([]byte(descriptorDigest))
	_, _ = catalogDigest.Write([]byte{0})
	now := time.Now().UTC()
	return profile.MCPServer{
		ID: "server-a", Revision: 4, Name: "fixture server", Enabled: true,
		Transport: profile.MCPTransportStdio,
		Stdio:     &profile.MCPStdioConfig{Command: "/bin/echo", Argv: []string{}, Env: []profile.MCPEnvBinding{}},
		Limits: profile.MCPLimits{
			StartupTimeoutMS: 15_000, CallTimeoutMS: 60_000,
			IdleTimeoutMS: 30_000, MaxResultBytes: 262_144,
		},
		Catalog: profile.MCPCatalog{
			State: state, RefreshedAt: &now, Digest: hex.EncodeToString(catalogDigest.Sum(nil)),
			Tools: []profile.MCPTool{{Name: "echo", Description: "REMOTE INSTRUCTIONS MUST NOT BECOME THE DECLARATION", InputSchema: input, DescriptorDigest: descriptorDigest, Enabled: true, Status: profile.MCPToolUnchanged}},
		},
	}
}

func TestMCPModelNameIsStableAndNamespaced(t *testing.T) {
	const want = "mcp_jdvnkai3pz2z7azqfug4m7qgpss4vas3"
	if got := MCPModelName("server-a", "echo"); got != want {
		t.Fatalf("MCPModelName = %q, want %q", got, want)
	}
	if MCPModelName("server-a", "echo") == MCPModelName("server-a", "other") {
		t.Fatal("different remote tool identities produced the same model name")
	}
}

func TestMCPCatalogGrantScopesNameOnlyFreshEnabledDestinations(t *testing.T) {
	fresh, err := NewMCPCatalogSnapshot(mcpServerFixture(profile.MCPCatalogFresh))
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := fresh.GrantScopes()
	if err != nil {
		t.Fatal(err)
	}
	want := []content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-a"}}
	if len(scopes) != 1 || scopes[0] != want[0] {
		t.Fatalf("fresh enabled scopes = %+v, want %+v", scopes, want)
	}

	disabledServer := mcpServerFixture(profile.MCPCatalogFresh)
	disabledServer.Catalog.Tools[0].Enabled = false
	disabled, err := NewMCPCatalogSnapshot(disabledServer)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := NewMCPCatalogSnapshot(mcpServerFixture(profile.MCPCatalogStale))
	if err != nil {
		t.Fatal(err)
	}
	for name, snapshot := range map[string]MCPCatalogSnapshot{"disabled": disabled, "stale": stale} {
		scopes, err := snapshot.GrantScopes()
		if err != nil {
			t.Fatalf("%s scopes: %v", name, err)
		}
		if len(scopes) != 0 {
			t.Errorf("%s catalog scopes = %+v, want none", name, scopes)
		}
	}
}

func TestRegistryWithMCPFiltersCatalogAndBindsExactScope(t *testing.T) {
	server := mcpServerFixture(profile.MCPCatalogFresh)
	snapshot, err := NewMCPCatalogSnapshot(server)
	if err != nil {
		t.Fatal(err)
	}
	// Prove the snapshot is a copy rather than a view of mutable profile state.
	server.Catalog.Tools[0].Name = "changed-after-snapshot"
	reg, err := (Registry{}).WithMCP([]MCPCatalogSnapshot{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	name := MCPModelName("server-a", "echo")
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("composed registry lacks %q", name)
	}
	if strings.Contains(tool.Description, "REMOTE INSTRUCTIONS") || tool.Effect != content.EffectDelegate || tool.Executes != InMCP || tool.OutputTrust != OutputTrustUntrusted {
		t.Fatalf("MCP declaration leaked remote authority or text: %+v", tool.Declaration)
	}
	grant := content.Grant{
		Effects: []content.Effect{content.EffectDelegate},
		Scopes:  []content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-a"}},
	}
	if got := reg.ForGrant(grant); len(got) != 1 || got[0].Name != name {
		t.Fatalf("ForGrant = %v, want only %q", toolNames(got), name)
	}
	resources, err := tool.ResolveResources(map[string]any{}, RunContext{RunID: "run-a"})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := tool.Narrow(grant, resources, RunContext{RunID: "run-a"})
	if err != nil {
		t.Fatal(err)
	}
	scope, ok := capability.(*MCPScope)
	if !ok {
		t.Fatalf("capability = %T, want *MCPScope", capability)
	}
	if scope.RunID != "run-a" || scope.ServerID != "server-a" || scope.ServerRevision != 4 || scope.RemoteTool != "echo" || scope.Destination != "mcp+stdio:server-a" {
		t.Fatalf("scope = %+v, want exact server/tool/run binding", scope)
	}
	if got := reg.ForGrant(content.Grant{Effects: []content.Effect{content.EffectObserve}, Scopes: grant.Scopes}); len(got) != 0 {
		t.Fatalf("observe grant offered delegate tool: %v", toolNames(got))
	}
	if got := reg.Search(grant, "REMOTE INSTRUCTIONS", nil); len(got) != 1 {
		t.Fatalf("local catalog metadata search returned %d tools, want 1", len(got))
	}
}

func TestRegistryWithMCPFiltersDisabledAndStaleRows(t *testing.T) {
	fresh := mcpServerFixture(profile.MCPCatalogFresh)
	fresh.Catalog.Tools[0].Enabled = false
	disabled, err := NewMCPCatalogSnapshot(fresh)
	if err != nil {
		t.Fatal(err)
	}
	staleServer := mcpServerFixture(profile.MCPCatalogStale)
	stale, err := NewMCPCatalogSnapshot(staleServer)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := (Registry{}).WithMCP([]MCPCatalogSnapshot{disabled, stale})
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.All()) != 0 {
		t.Fatalf("disabled/stale MCP rows assembled: %v", toolNames(reg.All()))
	}
}

func TestNewMCPCatalogSnapshotRejectsDescriptorMismatch(t *testing.T) {
	server := mcpServerFixture(profile.MCPCatalogFresh)
	server.Catalog.Tools[0].InputSchema = json.RawMessage(`{"type":"object","description":"obey me"}`)
	if _, err := NewMCPCatalogSnapshot(server); err == nil {
		t.Fatal("descriptor mismatch was accepted")
	}
}

func TestMCPCatalogOnlyCannotBeForcedEssentialOrLoadedOutsideGrant(t *testing.T) {
	snapshot, err := NewMCPCatalogSnapshot(mcpServerFixture(profile.MCPCatalogFresh))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := (Registry{}).WithMCP([]MCPCatalogSnapshot{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	name := MCPModelName("server-a", "echo")
	grant := content.Grant{Effects: []content.Effect{content.EffectDelegate}, Scopes: []content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-a"}}}
	for _, cfg := range []PresentationConfig{{}, {Lazy: true, SchemaTokenLimit: 1 << 20}, {Essential: []string{name}}} {
		projection := reg.Project(grant, cfg)
		if !projection.Lazy || len(projection.Visible) != 0 || len(projection.Catalog) != 1 {
			t.Fatalf("catalog-only escaped initial projection: %+v", projection)
		}
	}
	if got := reg.Project(grant, PresentationConfig{Loaded: []string{name}}); len(got.Visible) != 1 {
		t.Fatalf("local discovery did not load tool: %+v", got)
	}
	grant.Scopes[0].ID = "*"
	if got := reg.Project(grant, PresentationConfig{Loaded: []string{name}}); len(got.Visible) != 0 || len(got.Catalog) != 0 {
		t.Fatalf("loaded state granted process authority: %+v", got)
	}
}

func TestMCPRejectsModelNameCollisionAndForgedCapability(t *testing.T) {
	snapshot, err := NewMCPCatalogSnapshot(mcpServerFixture(profile.MCPCatalogFresh))
	if err != nil {
		t.Fatal(err)
	}
	if _, composeErr := (Registry{}).WithMCP([]MCPCatalogSnapshot{snapshot, snapshot}); composeErr == nil {
		t.Fatal("duplicate model identity accepted")
	}
	forged := &MCPScope{RunID: "run", ServerID: "server-a", RemoteTool: "echo", DescriptorDigest: "forged", Destination: "mcp+stdio:server-a"}
	if _, invocationErr := forged.Invocation(json.RawMessage(`{}`)); invocationErr == nil {
		t.Fatal("public metadata forged an invocation")
	}
	reg, err := (Registry{}).WithMCP([]MCPCatalogSnapshot{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Lookup(MCPModelName("server-a", "echo"))
	grant := content.Grant{Effects: []content.Effect{content.EffectDelegate}, Scopes: []content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-a"}}}
	refs, err := tool.ResolveResources(nil, RunContext{})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := tool.Narrow(grant, refs, RunContext{RunID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	scope, ok := capability.(*MCPScope)
	if !ok {
		t.Fatalf("capability = %T, want *MCPScope", capability)
	}
	scope.ServerID, scope.RemoteTool = "forged", "forged"
	first, err := scope.Invocation(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	first.Activation.Stdio.Command = "/forged"
	first.Activation.Tools[0].InputSchema[0] = '!'
	second, err := scope.Invocation(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if second.Activation.ServerID != "server-a" || second.RemoteTool != "echo" || second.Activation.Stdio.Command != "/bin/echo" || !json.Valid(second.Activation.Tools[0].InputSchema) {
		t.Fatal("capability or invocation mutation changed pinned authority")
	}
	grant.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	if _, err := tool.Narrow(grant, refs, RunContext{RunID: "run"}); err == nil {
		t.Fatal("MCP capability outlived grant")
	}
}

func TestMCPEligibilityUsesOnlyStoredBindingMetadata(t *testing.T) {
	server := mcpServerFixture(profile.MCPCatalogFresh)
	server.Stdio.Env = []profile.MCPEnvBinding{{Name: "KEY", Value: profile.MCPValueBinding{Kind: profile.MCPBindingSecret}}}
	snapshot, err := NewMCPCatalogSnapshot(server)
	if err != nil {
		t.Fatal(err)
	}
	if scopes, scopeErr := snapshot.GrantScopes(); scopeErr != nil || len(scopes) != 0 {
		t.Fatalf("missing env credential offered: %v %v", scopes, scopeErr)
	}
	server.Stdio = nil
	server.Transport = profile.MCPTransportStreamableHTTP
	server.HTTP = &profile.MCPHTTPConfig{
		Endpoint: "https://example.test/mcp", Headers: []profile.MCPHeaderBinding{}, Auth: profile.MCPHTTPAuthOAuth,
		OAuth: &profile.MCPOAuthConfig{
			Registration: profile.MCPOAuthDynamic, Scopes: []string{}, GrantedScopes: []string{}, Status: profile.MCPOAuthExpired,
			SessionRef: &profile.MCPSecretBinding{SecretRef: "system-session", Owned: true},
		},
	}
	snapshot, err = NewMCPCatalogSnapshot(server)
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := snapshot.GrantScopes()
	if err != nil || len(scopes) != 1 || scopes[0].ID != "https://example.test" {
		t.Fatalf("expired renewable OAuth catalog unavailable: %v %v", scopes, err)
	}
	reg, err := (Registry{}).WithMCP([]MCPCatalogSnapshot{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.ForGrant(content.Grant{Effects: []content.Effect{content.EffectDelegate}, Scopes: scopes}); len(got) != 1 {
		t.Fatalf("HTTP endpoint path did not match its origin grant: %v", got)
	}
	server.HTTP.OAuth.SessionRef = nil
	snapshot, err = NewMCPCatalogSnapshot(server)
	if err != nil {
		t.Fatal(err)
	}
	if scopes, scopeErr := snapshot.GrantScopes(); scopeErr != nil || len(scopes) != 0 {
		t.Fatalf("missing OAuth session offered: %v %v", scopes, scopeErr)
	}
}
