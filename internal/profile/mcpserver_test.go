package profile

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func literalBinding(value string) MCPValueBinding {
	return MCPValueBinding{Kind: MCPBindingLiteral, Literal: &value}
}

func secretBinding(ref string, owned bool) MCPValueBinding {
	return MCPValueBinding{Kind: MCPBindingSecret, SecretRef: ref, Owned: owned}
}

func validTestMCPServer() MCPServer {
	return MCPServer{
		Name:      "filesystem",
		Enabled:   true,
		Transport: MCPTransportStdio,
		Stdio: &MCPStdioConfig{
			Command: "/usr/bin/mcp-filesystem",
			Argv:    []string{"--safe"},
			Env: []MCPEnvBinding{
				{Name: "MODE", Value: literalBinding("read-only")},
				{Name: "TOKEN", Value: secretBinding("secrow:shared", false)},
			},
		},
		Limits: DefaultMCPLimits(),
	}
}

func TestCreateMCPServerMintsIdentityAndMissingCatalog(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}
	if !strings.HasPrefix(created.ID, "mcp:") || created.Revision != 1 {
		t.Fatalf("identity = %q rev %d, want backend-minted mcp id at revision 1", created.ID, created.Revision)
	}
	if created.Catalog.State != MCPCatalogMissing || created.Catalog.Tools == nil {
		t.Fatalf("catalog = %+v, want missing with a non-nil tool slice", created.Catalog)
	}
	listed, err := s.ListMCPServers()
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed = %+v, want created server", listed)
	}
}

func TestRefreshMCPServerCatalogDigestIsOrderIndependent(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.RefreshMCPServerCatalog(created.ID, created.Revision, MCPCatalog{Tools: []MCPTool{
		{Name: "b", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "a", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RefreshMCPServerCatalog(first.ID, first.Revision, MCPCatalog{Tools: []MCPTool{
		{Name: "a", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "b", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Catalog.Digest != second.Catalog.Digest {
		t.Fatalf("catalog digest changed with tool order: %q != %q", first.Catalog.Digest, second.Catalog.Digest)
	}
}

func TestDeleteMCPServerDoesNotDeleteOwnedReferenceStillUsed(t *testing.T) {
	s := newTestStore(t)
	firstSpec := validTestMCPServer()
	firstSpec.Name = "first"
	firstSpec.Stdio.Env[1].Value = secretBinding("secrow:owned-shared", true)
	first, err := s.CreateMCPServer(firstSpec)
	if err != nil {
		t.Fatal(err)
	}
	secondSpec := validTestMCPServer()
	secondSpec.Name = "second"
	secondSpec.Stdio.Env[1].Value = secretBinding("secrow:owned-shared", true)
	second, err := s.CreateMCPServer(secondSpec)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteMCPServer(first.ID, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.OwnedSecretRefs) != 0 {
		t.Fatalf("first delete refs = %v, want shared ref retained", deleted.OwnedSecretRefs)
	}
	deleted, err = s.DeleteMCPServer(second.ID, second.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.OwnedSecretRefs) != 1 || deleted.OwnedSecretRefs[0] != "secrow:owned-shared" {
		t.Fatalf("second delete refs = %v, want final owned ref", deleted.OwnedSecretRefs)
	}
}

func TestMCPServerValidationRejectsTransportAndConfigContradictions(t *testing.T) {
	cases := map[string]func(*MCPServer){
		"unknown transport": func(v *MCPServer) { v.Transport = "sse" },
		"stdio missing":     func(v *MCPServer) { v.Stdio = nil },
		"both configs": func(v *MCPServer) {
			v.HTTP = &MCPHTTPConfig{Endpoint: "https://example.com/mcp", Auth: MCPHTTPAuthNone}
		},
		"empty command": func(v *MCPServer) { v.Stdio.Command = "" },
		"relative cwd":  func(v *MCPServer) { v.Stdio.Cwd = "relative" },
		"duplicate env": func(v *MCPServer) {
			v.Stdio.Env = append(v.Stdio.Env, MCPEnvBinding{Name: "MODE", Value: literalBinding("other")})
		},
		"invalid env name": func(v *MCPServer) { v.Stdio.Env[0].Name = "NOT-VALID=" },
		"both binding sources": func(v *MCPServer) {
			v.Stdio.Env[0].Value.SecretRef = "secrow:x"
		},
		"too many argv": func(v *MCPServer) { v.Stdio.Argv = make([]string, MaxMCPBindingRows+1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			v := validTestMCPServer()
			mutate(&v)
			if _, err := s.CreateMCPServer(v); err == nil {
				t.Fatal("invalid server was stored")
			}
			got, err := s.ListMCPServers()
			if err != nil {
				t.Fatalf("ListMCPServers: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("rejected create changed store: %+v", got)
			}
		})
	}
}

func TestMCPHTTPValidationAndCanonicalDestination(t *testing.T) {
	v := validTestMCPServer()
	v.Transport = MCPTransportStreamableHTTP
	v.Stdio = nil
	v.HTTP = &MCPHTTPConfig{
		Endpoint: "HTTPS://Example.COM:443/mcp?tenant=a",
		Auth:     MCPHTTPAuthBearer,
		Bearer:   &MCPSecretBinding{SecretRef: "secrow:bearer", Owned: true},
		Headers: []MCPHeaderBinding{
			{Name: "X-Tenant", Value: literalBinding("a")},
		},
	}
	if err := ValidateMCPServer(v); err != nil {
		t.Fatalf("ValidateMCPServer: %v", err)
	}
	got, err := v.CanonicalDestination()
	if err != nil {
		t.Fatalf("CanonicalDestination: %v", err)
	}
	if got != "https://example.com/mcp?tenant=a" {
		t.Fatalf("destination = %q", got)
	}

	bad := map[string]func(*MCPServer){
		"public plaintext HTTP": func(v *MCPServer) { v.HTTP.Endpoint = "http://example.com/mcp" },
		"userinfo":              func(v *MCPServer) { v.HTTP.Endpoint = "https://u:p@example.com/mcp" },
		"fragment":              func(v *MCPServer) { v.HTTP.Endpoint = "https://example.com/mcp#secret" },
		"bearer absent":         func(v *MCPServer) { v.HTTP.Bearer = nil },
		"forbidden header":      func(v *MCPServer) { v.HTTP.Headers[0].Name = "Authorization" },
		"duplicate header": func(v *MCPServer) {
			v.HTTP.Headers = append(v.HTTP.Headers, MCPHeaderBinding{Name: "x-tenant", Value: literalBinding("b")})
		},
		"header control": func(v *MCPServer) { v.HTTP.Headers[0].Value = literalBinding("a\nb") },
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			b := v
			h := *v.HTTP
			h.Headers = append([]MCPHeaderBinding(nil), v.HTTP.Headers...)
			b.HTTP = &h
			mutate(&b)
			if err := ValidateMCPServer(b); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMCPJSONRejectsUnknownFieldsAtEveryStoredLayer(t *testing.T) {
	base, err := json.Marshal(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(base, &raw); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"server": func(m map[string]any) { m["surprise"] = true },
		"stdio": func(m map[string]any) {
			stdio, ok := m["stdio"].(map[string]any)
			if !ok {
				return
			}
			stdio["surprise"] = true
		},
		"binding": func(m map[string]any) {
			stdio, ok := m["stdio"].(map[string]any)
			if !ok {
				return
			}
			env, ok := stdio["env"].([]any)
			if !ok || len(env) == 0 {
				return
			}
			binding, ok := env[0].(map[string]any)
			if !ok {
				return
			}
			value, ok := binding["value"].(map[string]any)
			if !ok {
				return
			}
			value["surprise"] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			var copyMap map[string]any
			copyJSON, _ := json.Marshal(raw)
			_ = json.Unmarshal(copyJSON, &copyMap)
			mutate(copyMap)
			encoded, _ := json.Marshal(copyMap)
			var got MCPServer
			if err := json.Unmarshal(encoded, &got); err == nil {
				t.Fatal("unknown field was accepted")
			}
		})
	}
}

func TestMCPStoreLoadsOldDocumentWithEmptyNonNilSlice(t *testing.T) {
	s := newTestStore(t)
	if err := s.docStore.Write(s.fileName, map[string]any{"profiles": []any{}}); err != nil {
		t.Fatalf("seed old document: %v", err)
	}
	got, err := s.ListMCPServers()
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("servers = %#v, want non-nil empty slice", got)
	}
}

func TestMCPStoreRejectsDuplicateStoredIDs(t *testing.T) {
	s := newTestStore(t)
	v := validTestMCPServer()
	v.ID, v.Revision = "mcp:duplicate", 1
	if err := s.docStore.Write(s.fileName, map[string]any{"mcpServers": []MCPServer{v, v}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.ListMCPServers(); err == nil {
		t.Fatal("duplicate ids on disk must be rejected")
	}
}

func TestMCPUpdateIsCASAndConfigChangeStalesCatalog(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	fresh := MCPCatalog{Tools: []MCPTool{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	current, err := s.RefreshMCPServerCatalog(created.ID, created.Revision, fresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	stale := current
	stale.Name = "lost update"
	stale.Revision--
	if _, conflictErr := s.UpdateMCPServer(stale); !errors.Is(conflictErr, ErrMCPServerConflict) {
		t.Fatalf("stale update error = %v, want conflict", conflictErr)
	}
	current.Stdio.Command = "/usr/bin/changed"
	updated, err := s.UpdateMCPServer(current)
	if err != nil {
		t.Fatalf("UpdateMCPServer: %v", err)
	}
	if updated.Revision != current.Revision+1 || updated.Catalog.State != MCPCatalogStale {
		t.Fatalf("updated = %+v, want incremented stale record", updated)
	}
	if len(updated.Catalog.Tools) != 1 {
		t.Fatal("stale catalog metadata should remain visible until refresh")
	}
	if _, conflictErr := s.DeleteMCPServer(updated.ID, current.Revision); !errors.Is(conflictErr, ErrMCPServerConflict) {
		t.Fatalf("stale delete error = %v, want conflict", conflictErr)
	}
}

func TestRefreshMCPServerCatalogMergesEnablement(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.RefreshMCPServerCatalog(created.ID, created.Revision, MCPCatalog{Tools: []MCPTool{
		{Name: "same", Description: "untrusted", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "changed", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "removed", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	first, err = s.SetMCPToolsEnabled(first.ID, first.Revision, []string{"same", "changed", "removed"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RefreshMCPServerCatalog(first.ID, first.Revision, MCPCatalog{Tools: []MCPTool{
		{Name: "same", Description: "description may change without changing authority", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "changed", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)},
		{Name: "new", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}})
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	byName := map[string]MCPTool{}
	for _, tool := range second.Catalog.Tools {
		byName[tool.Name] = tool
	}
	if !byName["same"].Enabled || byName["same"].Status != MCPToolUnchanged {
		t.Fatalf("same = %+v, want enabled unchanged", byName["same"])
	}
	if byName["changed"].Enabled || byName["changed"].Status != MCPToolChanged {
		t.Fatalf("changed = %+v, want disabled changed", byName["changed"])
	}
	if byName["new"].Enabled || byName["new"].Status != MCPToolNew {
		t.Fatalf("new = %+v, want disabled new", byName["new"])
	}
	if _, exists := byName["removed"]; exists {
		t.Fatal("removed tool survived refresh")
	}
}

func TestInvalidCatalogRefreshCommitsNothing(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RefreshMCPServerCatalog(created.ID, created.Revision, MCPCatalog{Tools: []MCPTool{
		{Name: "duplicate", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "duplicate", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}})
	if err == nil {
		t.Fatal("duplicate refresh must fail")
	}
	got, err := s.GetMCPServer(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != created.Revision || got.Catalog.State != MCPCatalogMissing {
		t.Fatalf("failed refresh committed state: %+v", got)
	}
}

func TestMCPDTOContainsNoSecretReferences(t *testing.T) {
	v := validTestMCPServer()
	v.ID, v.Revision = "mcp:test", 1
	v.Stdio.Env = append(v.Stdio.Env, MCPEnvBinding{Name: "OWNED", Value: secretBinding("secrow:owned", true)})
	raw, err := json.Marshal(v.SanitizedDTO())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"secrow:shared", "secrow:owned", "secretRef"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized DTO leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"secretSet":true`) {
		t.Fatalf("DTO lost non-sensitive binding presence: %s", text)
	}
}

func TestMCPSecretReferencesCountAndClearWithoutDeletingServer(t *testing.T) {
	s := newTestStore(t)
	server := validTestMCPServer()
	created, err := s.CreateMCPServer(server)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatal(err)
	}
	if impact.SecretCount != 1 || impact.MCPServerCount != 1 {
		t.Fatalf("impact = %+v, want one secret and one MCP server", impact)
	}
	if clearErr := s.ClearSecretRefs("secrow:shared"); clearErr != nil {
		t.Fatal(clearErr)
	}
	remaining, err := s.GetMCPServer(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if remaining.Stdio == nil || len(remaining.Stdio.Env) != 1 || remaining.Stdio.Env[0].Name != "MODE" {
		t.Fatalf("server bindings after clear = %+v, want only literal env", remaining.Stdio)
	}
	if remaining.Enabled || remaining.Catalog.State != MCPCatalogStale {
		t.Fatalf("server after secret clear = enabled=%v state=%q, want disabled/stale", remaining.Enabled, remaining.Catalog.State)
	}
	impact, err = s.CountSecretReferences()
	if err != nil {
		t.Fatal(err)
	}
	if impact.SecretCount != 0 || impact.MCPServerCount != 0 {
		t.Fatalf("impact after clear = %+v, want zero", impact)
	}
}

func TestClearMCPSecretRefClearsBearerAuthenticationMode(t *testing.T) {
	server := validTestMCPServer()
	server.Transport = MCPTransportStreamableHTTP
	server.Stdio = nil
	server.HTTP = &MCPHTTPConfig{
		Endpoint: "https://example.com/mcp",
		Auth:     MCPHTTPAuthBearer,
		Bearer:   &MCPSecretBinding{SecretRef: "secrow:bearer", Owned: true},
		Headers:  []MCPHeaderBinding{},
	}
	if !clearMCPSecretRef(&server, "secrow:bearer") {
		t.Fatal("clearMCPSecretRef reported no change")
	}
	if server.HTTP.Auth != MCPHTTPAuthNone || server.HTTP.Bearer != nil {
		t.Fatalf("HTTP auth after clearing bearer = %+v, want none without bearer", server.HTTP)
	}
	if err := ValidateMCPServer(server); err != nil {
		t.Fatalf("cleared server is invalid: %v", err)
	}
}
