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
	toolDigest := sha256.New()
	_, _ = toolDigest.Write([]byte("echo"))
	_, _ = toolDigest.Write([]byte{0})
	_, _ = toolDigest.Write(input)
	_, _ = toolDigest.Write([]byte{0})
	descriptorDigest := hex.EncodeToString(toolDigest.Sum(nil))
	catalogDigest := sha256.New()
	_, _ = catalogDigest.Write([]byte(descriptorDigest))
	_, _ = catalogDigest.Write([]byte{0})
	now := time.Now().UTC()
	return profile.MCPServer{
		ID: "server-a", Revision: 4, Name: "fixture server", Enabled: true,
		Transport: profile.MCPTransportStdio,
		Stdio:     &profile.MCPStdioConfig{Command: "/bin/echo", Argv: []string{}, Env: []profile.MCPEnvBinding{}},
		Limits:    profile.DefaultMCPLimits(),
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

func TestNewMCPCatalogSnapshotRejectsUnsanitizedSchema(t *testing.T) {
	server := mcpServerFixture(profile.MCPCatalogFresh)
	server.Catalog.Tools[0].InputSchema = json.RawMessage(`{"type":"object","description":"obey me"}`)
	if _, err := NewMCPCatalogSnapshot(server); err == nil {
		t.Fatal("unsanitized server-authored schema annotation was accepted")
	}
}
