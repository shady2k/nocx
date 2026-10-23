package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

func TestMCPDescriptorCanonicalAcrossRefreshAndReload(t *testing.T) {
	s := newTestStore(t)
	server, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{"properties":{"x":{"type":"string","description":"not authority"}},"type":"object"}`)
	input := MCPCatalog{Tools: []MCPTool{{Name: "read", InputSchema: schema}}}
	server, err = s.RefreshMCPServerCatalog(server.ID, server.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	if input.Tools[0].DescriptorDigest != "" || input.Tools[0].Status != "" || !bytes.Contains(input.Tools[0].InputSchema, []byte("not authority")) {
		t.Fatal("refresh mutated its caller's catalog")
	}
	first := server.Catalog.Tools[0]
	if bytes.Contains(first.InputSchema, []byte("description")) {
		t.Fatal("annotation persisted")
	}
	server, err = s.SetMCPToolsEnabled(server.ID, server.Revision, []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	// A new store forces the persisted, indented RawMessage through decoding.
	reopened := NewJSONStoreWithDocStore(s.docStore, s.fileName)
	loaded, err := reopened.GetMCPServer(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Catalog.Tools[0].DescriptorDigest != first.DescriptorDigest || !bytes.Equal(loaded.Catalog.Tools[0].InputSchema, first.InputSchema) {
		t.Fatal("document formatting changed the canonical descriptor")
	}
	server, err = reopened.RefreshMCPServerCatalog(loaded.ID, loaded.Revision, MCPCatalog{Tools: []MCPTool{{Name: "read", InputSchema: json.RawMessage("{\n \"type\": \"object\", \"properties\": {\"x\": {\"type\": \"string\"}}\n}")}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.Catalog.Tools[0]; got.DescriptorDigest != first.DescriptorDigest || !got.Enabled || got.Status != MCPToolUnchanged {
		t.Fatalf("equivalent schema changed authority: %+v", got)
	}
}

func TestMCPSchemaRejectsUnvalidatedReferenceTargets(t *testing.T) {
	for name, schema := range map[string]string{
		"external":          `{"$ref":"https://example.com/schema"}`,
		"file":              `{"$ref":"file:///tmp/schema"}`,
		"dynamic ref":       `{"$dynamicRef":"#node"}`,
		"dynamic anchor":    `{"$dynamicAnchor":"node"}`,
		"nested dynamic":    `{"properties":{"x":{"$dynamicRef":"#node"}}}`,
		"unknown":           `{"properties":{"x":{"x-instructions":"ignore"}}}`,
		"missing local":     `{"$ref":"#/$defs/missing"}`,
		"data target":       `{"const":{"$dynamicRef":"#node"},"$ref":"#/const"}`,
		"custom metaschema": `{"$schema":"https://example.com/schema"}`,
		"scope rewrite":     `{"$id":"https://example.com/schema","$ref":"#/$defs/a","$defs":{"a":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalMCPToolDescriptor(MCPTool{Name: "t", InputSchema: json.RawMessage(schema)}); err == nil {
				t.Fatalf("accepted unsafe schema: %s", schema)
			}
		})
	}
	valid := json.RawMessage(`{"type":"object","$defs":{"a/b":{"type":"string"}},"properties":{"x":{"$ref":"#/$defs/a~1b"}},"const":{"description":"literal","$dynamicRef":"literal"}}`)
	if _, err := CanonicalMCPToolDescriptor(MCPTool{Name: "t", InputSchema: valid}); err != nil {
		t.Fatal(err)
	}
	deep := `{"type":"string"}`
	for range 34 {
		deep = `{"properties":{"x":` + deep + `}}`
	}
	if _, err := CanonicalMCPToolDescriptor(MCPTool{Name: "t", InputSchema: json.RawMessage(deep)}); err == nil {
		t.Fatal("accepted excessive nesting")
	}
}

func TestMCPCatalogBoundsAndCASCommitNothing(t *testing.T) {
	s := newTestStore(t)
	server, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	server, err = s.RefreshMCPServerCatalog(server.ID, server.Revision, MCPCatalog{Tools: []MCPTool{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetMCPServer(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	tooMany := make([]MCPTool, MaxMCPToolsPerServer+1)
	for i := range tooMany {
		tooMany[i] = MCPTool{Name: fmt.Sprintf("page-tool-%d", i), InputSchema: json.RawMessage(`{"type":"object"}`)}
	}
	tooLarge := make([]MCPTool, MaxMCPToolsPerServer)
	largeSchema := json.RawMessage(`{"const":"` + strings.Repeat("x", 9<<10) + `"}`)
	for i := range tooLarge {
		tooLarge[i] = MCPTool{Name: fmt.Sprintf("t-%d", i), InputSchema: largeSchema}
	}
	for _, tools := range [][]MCPTool{tooMany, tooLarge, {{Name: "bad", InputSchema: json.RawMessage(`{"type":"not-a-type"}`)}}} {
		if _, refreshErr := s.RefreshMCPServerCatalog(server.ID, server.Revision, MCPCatalog{Tools: tools}); refreshErr == nil {
			t.Fatal("oversized/invalid catalog committed")
		}
	}
	stale := server
	stale.Revision--
	if _, updateErr := s.UpdateMCPServer(stale); !errors.Is(updateErr, ErrMCPServerConflict) {
		t.Fatalf("update: %v", updateErr)
	}
	if _, refreshErr := s.RefreshMCPServerCatalog(server.ID, stale.Revision, MCPCatalog{}); !errors.Is(refreshErr, ErrMCPServerConflict) {
		t.Fatalf("refresh: %v", refreshErr)
	}
	if _, enableErr := s.SetMCPToolsEnabled(server.ID, stale.Revision, nil); !errors.Is(enableErr, ErrMCPServerConflict) {
		t.Fatalf("enable: %v", enableErr)
	}
	if _, deleteErr := s.DeleteMCPServer(server.ID, stale.Revision); !errors.Is(deleteErr, ErrMCPServerConflict) {
		t.Fatalf("delete: %v", deleteErr)
	}
	after, err := s.GetMCPServer(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected operation changed the record")
	}
}

func TestMCPSnapshotsDoNotAliasCallerOrDTO(t *testing.T) {
	s := newTestStore(t)
	input := validTestMCPServer()
	created, err := s.CreateMCPServer(input)
	if err != nil {
		t.Fatal(err)
	}
	*created.Stdio.Env[0].Value.Literal = "changed"
	created.Stdio.Argv[0] = "changed"
	if *input.Stdio.Env[0].Value.Literal != "read-only" || input.Stdio.Argv[0] != "--safe" {
		t.Fatal("create aliases input")
	}
	loaded, err := s.GetMCPServer(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = s.RefreshMCPServerCatalog(loaded.ID, loaded.Revision, MCPCatalog{Tools: []MCPTool{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	dto := loaded.SanitizedDTO()
	*dto.Stdio.Env[0].Value.Literal = "changed"
	dto.Catalog.Tools[0].InputSchema[0] = 'X'
	if *loaded.Stdio.Env[0].Value.Literal != "read-only" || loaded.Catalog.Tools[0].InputSchema[0] != '{' {
		t.Fatal("DTO aliases snapshot")
	}
	updated, err := s.UpdateMCPServer(loaded)
	if err != nil {
		t.Fatal(err)
	}
	updated.Stdio.Argv[0] = "changed"
	if loaded.Stdio.Argv[0] != "--safe" {
		t.Fatal("update aliases input")
	}
}

func TestMCPOwnedCleanupChecksEverySurvivingHolder(t *testing.T) {
	for _, holder := range []string{"profile-password", "profile-key", "profile-passphrase", "group-password", "group-key", "group-passphrase", "endpoint-credential", "endpoint-header", "mcp-env", "mcp-header", "mcp-bearer", "mcp-client", "mcp-session"} {
		for _, operation := range []string{"update", "delete"} {
			t.Run(holder+"/"+operation, func(t *testing.T) {
				s := newTestStore(t)
				const ref = "sec:v1:file:shared"
				input := validTestMCPServer()
				input.Stdio.Env[1].Value = secretBinding(ref, true)
				server, err := s.CreateMCPServer(input)
				if err != nil {
					t.Fatal(err)
				}
				d, err := s.LoadAll()
				if err != nil {
					t.Fatal(err)
				}
				seedMCPReferenceHolder(d, holder, ref)
				if writeErr := s.WriteAll(d); writeErr != nil {
					t.Fatal(writeErr)
				}
				if operation == "delete" {
					result, deleteErr := s.DeleteMCPServer(server.ID, server.Revision)
					if deleteErr != nil {
						t.Fatal(deleteErr)
					}
					if len(result.OwnedSecretRefs) != 0 {
						t.Fatalf("deleted surviving holder's material: %v", result)
					}
				} else {
					server.Stdio.Env = server.Stdio.Env[:1]
					if _, updateErr := s.UpdateMCPServer(server); updateErr != nil {
						t.Fatal(updateErr)
					}
				}
				refs, err := s.UnreferencedSecretRefs([]string{ref, ref, "orphan", "orphan", ""})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(refs, []string{"orphan"}) {
					t.Fatalf("cleanup refs = %v", refs)
				}
			})
		}
	}
}

func seedMCPReferenceHolder(d *storeData, holder, ref string) {
	if strings.HasPrefix(holder, "profile-") {
		p := SSHProfile{Base: Base{ID: "profile", Name: "profile", Type: "ssh"}, Options: StoredSSHProfileOptions{Host: "host"}}
		switch holder {
		case "profile-password":
			p.Options.PasswordSecret = ref
		case "profile-key":
			p.Options.KeySecret = ref
		case "profile-passphrase":
			p.Options.KeyPassphraseSecret = ref
		}
		d.Profiles = append(d.Profiles, p)
	} else if strings.HasPrefix(holder, "group-") {
		g := ProfileGroup{ID: "group", Name: "group", Defaults: &ProfileDefaults{}}
		switch holder {
		case "group-password":
			g.Defaults.PasswordSecret = &ref
		case "group-key":
			g.Defaults.KeySecret = &ref
		case "group-passphrase":
			g.Defaults.KeyPassphraseSecret = &ref
		}
		d.Groups = append(d.Groups, g)
	} else if strings.HasPrefix(holder, "endpoint-") {
		ep := validTestEndpoint()
		ep.CredentialRef = ""
		if holder == "endpoint-credential" {
			ep.CredentialRef = ref
		} else {
			ep.Headers = []EndpointHeader{{Name: "X-Token", ValueRef: ref}}
		}
		d.Endpoints = append(d.Endpoints, ep)
	} else {
		s := validTestMCPServer()
		s.ID, s.Revision = "mcp:holder", 7
		s.Catalog = MCPCatalog{State: MCPCatalogMissing, Tools: []MCPTool{}}
		if holder == "mcp-env" {
			s.Stdio.Env = []MCPEnvBinding{{Name: "TOKEN", Value: secretBinding(ref, false)}}
		} else {
			s.Transport, s.Stdio = MCPTransportStreamableHTTP, nil
			s.HTTP = &MCPHTTPConfig{Endpoint: "https://example.com/mcp", Auth: MCPHTTPAuthNone, Headers: []MCPHeaderBinding{}}
			switch holder {
			case "mcp-header":
				s.HTTP.Headers = []MCPHeaderBinding{{Name: "X-Token", Value: secretBinding(ref, false)}}
			case "mcp-bearer":
				s.HTTP.Auth = MCPHTTPAuthBearer
				s.HTTP.Bearer = &MCPSecretBinding{SecretRef: ref}
			case "mcp-client", "mcp-session":
				s.HTTP.Auth = MCPHTTPAuthOAuth
				s.HTTP.OAuth = &MCPOAuthConfig{Registration: MCPOAuthPreregistered, ClientID: "client", Scopes: []string{}, GrantedScopes: []string{}, Status: MCPOAuthMissing}
				if holder == "mcp-client" {
					s.HTTP.OAuth.ClientSecret = &MCPSecretBinding{SecretRef: ref}
				} else {
					s.HTTP.OAuth.SessionRef = &MCPSecretBinding{SecretRef: ref, Owned: true}
					s.HTTP.OAuth.Status = MCPOAuthConnected
				}
			}
		}
		d.MCPServers = append(d.MCPServers, s)
	}
}

func TestMCPResetCountsDistinctAndClearsGroupsAndAllBindings(t *testing.T) {
	s := newTestStore(t)
	d := &storeData{}
	for _, holder := range []string{"profile-password", "group-key", "endpoint-header", "mcp-session"} {
		seedMCPReferenceHolder(d, holder, "shared")
	}
	server := &d.MCPServers[0]
	server.HTTP.Headers = []MCPHeaderBinding{{Name: "X-Secret", Value: secretBinding("header", true)}, {Name: "X-Literal", Value: literalBinding("keep")}}
	server.HTTP.OAuth.ClientSecret = &MCPSecretBinding{SecretRef: "client", Owned: true}
	unaffected := validTestMCPServer()
	unaffected.ID, unaffected.Revision = "mcp:unaffected", 4
	unaffected.Stdio.Env = unaffected.Stdio.Env[:1]
	normalizeMCPServer(&unaffected)
	d.MCPServers = append(d.MCPServers, unaffected)
	if err := s.WriteAll(d); err != nil {
		t.Fatal(err)
	}
	want := SecretReferenceImpact{SecretCount: 3, ProfileCount: 1, EndpointCount: 1, MCPServerCount: 1}
	impact, err := s.CountSecretReferences()
	if err != nil || impact != want {
		t.Fatalf("preview = %+v, %v", impact, err)
	}
	matched, err := s.MCPServersReferencingSecrets([]string{"client", "shared", "client"})
	if err != nil || len(matched) != 1 || matched[0].ID != "mcp:holder" {
		t.Fatalf("matched = %+v, %v", matched, err)
	}
	matched, err = s.MCPServersReferencingSecrets(nil)
	if err != nil || matched == nil || len(matched) != 0 {
		t.Fatalf("empty match = %+v, %v", matched, err)
	}
	impact, err = s.ClearAllSecretReferences()
	if err != nil || impact != want {
		t.Fatalf("clear = %+v, %v", impact, err)
	}
	after, err := s.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if after.Groups[0].Defaults.KeySecret != nil || after.Profiles[0].Options.PasswordSecret != "" || len(after.Endpoints[0].Headers) != 0 {
		t.Fatal("aggregate references survived reset")
	}
	got := after.MCPServers[0]
	if got.Revision != 8 || got.Enabled || got.HTTP.OAuth.SessionRef != nil || got.HTTP.OAuth.ClientSecret != nil || got.HTTP.OAuth.Status != MCPOAuthMissing || len(got.HTTP.Headers) != 1 || *got.HTTP.Headers[0].Value.Literal != "keep" {
		t.Fatalf("reset server = %+v", got)
	}
	if !reflect.DeepEqual(after.MCPServers[1], unaffected) {
		t.Fatal("reset touched server with no secrets")
	}
	impact, err = s.ClearAllSecretReferences()
	if err != nil || impact != (SecretReferenceImpact{}) {
		t.Fatalf("idempotent reset = %+v, %v", impact, err)
	}
}

type mcpFailWrites struct{ storage.DocumentStore }

func (mcpFailWrites) Write(string, any) error { return errors.New("injected metadata failure") }

func TestMCPMetadataFailureReturnsNoCleanupAndKeepsReferences(t *testing.T) {
	s := newTestStore(t)
	input := validTestMCPServer()
	input.Stdio.Env[1].Value.Owned = true
	server, err := s.CreateMCPServer(input)
	if err != nil {
		t.Fatal(err)
	}
	s.docStore = mcpFailWrites{s.docStore}
	result, err := s.DeleteMCPServer(server.ID, server.Revision)
	if err == nil || len(result.OwnedSecretRefs) != 0 {
		t.Fatal("failed metadata write scheduled deletion")
	}
	if _, clearAllErr := s.ClearAllSecretReferences(); clearAllErr == nil {
		t.Fatal("reset accepted failed metadata write")
	}
	if clearErr := s.ClearSecretRefs("secrow:shared"); clearErr == nil {
		t.Fatal("delete accepted failed metadata write")
	}
	loaded, err := s.GetMCPServer(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, server) {
		t.Fatal("failed write changed stored record")
	}
}

func TestMCPClearingExactSecretLeavesOtherBindingsAndServers(t *testing.T) {
	for _, holder := range []string{"mcp-env", "mcp-header", "mcp-bearer", "mcp-client", "mcp-session"} {
		t.Run(holder, func(t *testing.T) {
			s := newTestStore(t)
			d := &storeData{}
			seedMCPReferenceHolder(d, holder, "target")
			if d.MCPServers[0].HTTP != nil {
				d.MCPServers[0].HTTP.Headers = append(d.MCPServers[0].HTTP.Headers,
					MCPHeaderBinding{Name: "X-Keep", Value: secretBinding("keep", false)},
					MCPHeaderBinding{Name: "X-Literal", Value: literalBinding("literal")})
			} else {
				d.MCPServers[0].Stdio.Env = append(d.MCPServers[0].Stdio.Env,
					MCPEnvBinding{Name: "KEEP", Value: secretBinding("keep", false)},
					MCPEnvBinding{Name: "LITERAL", Value: literalBinding("literal")})
			}
			if err := s.WriteAll(d); err != nil {
				t.Fatal(err)
			}
			if err := s.ClearSecretRefs("target"); err != nil {
				t.Fatal(err)
			}
			server, err := s.GetMCPServer("mcp:holder")
			if err != nil {
				t.Fatal(err)
			}
			if server.Revision != 8 || server.Enabled {
				t.Fatalf("cleared server revision/enabled = %d/%v", server.Revision, server.Enabled)
			}
			var refs []string
			visitMCPSecretRefs(&server, func(ref string, _ bool) { refs = append(refs, ref) })
			if !reflect.DeepEqual(refs, []string{"keep"}) {
				t.Fatalf("remaining refs = %v", refs)
			}
			if clearErr := s.ClearSecretRefs("target"); clearErr != nil {
				t.Fatal(clearErr)
			}
			again, err := s.GetMCPServer(server.ID)
			if err != nil || !reflect.DeepEqual(again, server) {
				t.Fatalf("repeat clear changed server: %+v, %v", again, err)
			}
		})
	}
}

func TestMCPWritesPreserveCurrentAggregateFields(t *testing.T) {
	s := newTestStore(t)
	d := &storeData{}
	seedMCPReferenceHolder(d, "profile-key", "ssh-key")
	seedMCPReferenceHolder(d, "group-passphrase", "group-secret")
	seedMCPReferenceHolder(d, "endpoint-header", "header-secret")
	d.Roles = []RoleAssignment{{Role: RoleAnswering, EndpointID: d.Endpoints[0].ID, Model: "model"}}
	d.DefaultModel = DefaultModel{EndpointID: d.Endpoints[0].ID, Model: "default"}
	if err := s.WriteAll(d); err != nil {
		t.Fatal(err)
	}
	before, err := s.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	server, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	server.Name = "renamed"
	server, err = s.UpdateMCPServer(server)
	if err != nil {
		t.Fatal(err)
	}
	if _, deleteErr := s.DeleteMCPServer(server.ID, server.Revision); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	after, err := s.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	after.MCPServers = nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("MCP mutations changed existing aggregate fields")
	}
}

func TestGroupOnlyReferencesParticipateInReset(t *testing.T) {
	s := newTestStore(t)
	d := &storeData{}
	seedMCPReferenceHolder(d, "group-password", "only-group")
	if err := s.WriteAll(d); err != nil {
		t.Fatal(err)
	}
	impact, err := s.ClearAllSecretReferences()
	if err != nil || impact.SecretCount != 1 || impact.ProfileCount != 0 {
		t.Fatalf("group-only reset = %+v, %v", impact, err)
	}
	groups, err := s.LoadGroups()
	if err != nil || len(groups) != 1 || groups[0].Defaults.PasswordSecret != nil {
		t.Fatalf("groups after reset = %+v, %v", groups, err)
	}
}

func TestMCPLargeCanonicalSchemaSurvivesIndentedStorage(t *testing.T) {
	s := newTestStore(t)
	server, err := s.CreateMCPServer(validTestMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	properties := make(map[string]any)
	for i := range 600 {
		properties[fmt.Sprintf("field-%d", i)] = map[string]any{"type": "string"}
	}
	raw, err := json.Marshal(map[string]any{"type": "object", "properties": properties})
	if err != nil || len(raw) > MaxMCPSchemaBytes {
		t.Fatalf("fixture bound: %d, %v", len(raw), err)
	}
	server, err = s.RefreshMCPServerCatalog(server.ID, server.Revision, MCPCatalog{Tools: []MCPTool{{Name: "large", InputSchema: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetMCPServer(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Catalog.Tools[0].DescriptorDigest != server.Catalog.Tools[0].DescriptorDigest {
		t.Fatal("storage formatting changed large schema digest")
	}
}

func TestMCPSecretReferencesFiltersOwnershipAndDeduplicates(t *testing.T) {
	for _, holder := range []string{"mcp-env", "mcp-header", "mcp-bearer", "mcp-client", "mcp-session"} {
		t.Run(holder, func(t *testing.T) {
			d := &storeData{}
			seedMCPReferenceHolder(d, holder, "a-holder")
			server := d.MCPServers[0]
			// Exercise duplicate owned/shared references alongside each holder.
			// Empty references and literal values must not become candidates.
			extra := []MCPValueBinding{
				secretBinding("z-owned", true),
				secretBinding("z-owned", false),
				secretBinding("", true),
				literalBinding("not-a-reference"),
			}
			for i, binding := range extra {
				name := fmt.Sprintf("EXTRA%d", i)
				if server.Stdio != nil {
					server.Stdio.Env = append(server.Stdio.Env, MCPEnvBinding{Name: name, Value: binding})
				} else {
					server.HTTP.Headers = append(server.HTTP.Headers, MCPHeaderBinding{Name: name, Value: binding})
				}
			}
			if got := MCPSecretReferences(server, false); !reflect.DeepEqual(got, []string{"a-holder", "z-owned"}) {
				t.Fatalf("all references = %v", got)
			}
			wantOwned := []string{"z-owned"}
			if holder == "mcp-session" {
				wantOwned = []string{"a-holder", "z-owned"}
			}
			if got := MCPSecretReferences(server, true); !reflect.DeepEqual(got, wantOwned) {
				t.Fatalf("owned references = %v, want %v", got, wantOwned)
			}
		})
	}
	if got := MCPSecretReferences(MCPServer{}, false); got == nil || len(got) != 0 {
		t.Fatalf("empty references = %#v", got)
	}
}
