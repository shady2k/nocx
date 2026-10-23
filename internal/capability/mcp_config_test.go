package capability_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

type mcpTestRepository struct {
	*profile.JSONStore
	writeErr       error
	queryErr       error
	commitThenFail bool
}

func (r *mcpTestRepository) CreateMCPServer(server profile.MCPServer) (profile.MCPServer, error) {
	if r.writeErr != nil && !r.commitThenFail {
		return profile.MCPServer{}, r.writeErr
	}
	created, err := r.JSONStore.CreateMCPServer(server)
	if err == nil && r.commitThenFail {
		return profile.MCPServer{}, r.writeErr
	}
	return created, err
}

func (r *mcpTestRepository) UpdateMCPServer(server profile.MCPServer) (profile.MCPServer, error) {
	if r.writeErr != nil && !r.commitThenFail {
		return profile.MCPServer{}, r.writeErr
	}
	updated, err := r.JSONStore.UpdateMCPServer(server)
	if err == nil && r.commitThenFail {
		return profile.MCPServer{}, r.writeErr
	}
	return updated, err
}

func (r *mcpTestRepository) DeleteMCPServer(id string, revision uint64) (profile.MCPDeleteResult, error) {
	if r.writeErr != nil {
		return profile.MCPDeleteResult{}, r.writeErr
	}
	return r.JSONStore.DeleteMCPServer(id, revision)
}

func (r *mcpTestRepository) UnreferencedSecretRefs(refs []string) ([]string, error) {
	if r.queryErr != nil {
		return nil, r.queryErr
	}
	return r.JSONStore.UnreferencedSecretRefs(refs)
}

type mcpTestSecrets struct {
	*fakeEndpointSecrets
	repo            *mcpTestRepository
	targets         map[credential.SecretID]string
	finalized       []credential.SecretID
	deleteAttempts  []credential.SecretID
	prepareAttempts []credential.SecretID
	candidates      map[credential.SecretID]struct{}
	prepareErr      error
	pendingErr      error
	promotionErr    error
	failMintAt      int
	attachErr       error
	finalizeErr     error
}

func (s *mcpTestSecrets) CreateNamed(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error) {
	if s.failMintAt > 0 && len(s.minted)+1 == s.failMintAt {
		return "", errors.New("injected mint failure")
	}
	return s.fakeEndpointSecrets.CreateNamed(ctx, value, meta)
}

func (s *mcpTestSecrets) AttachTarget(_ context.Context, id credential.SecretID, target string) error {
	if s.attachErr != nil {
		return s.attachErr
	}
	s.targets[id] = target
	return nil
}

func (s *mcpTestSecrets) CommitMetadata(_ context.Context, id credential.SecretID) error {
	if s.finalizeErr != nil {
		return s.finalizeErr
	}
	unused, err := s.repo.UnreferencedSecretRefs([]string{string(id)})
	if err != nil {
		return err
	}
	if len(unused) != 0 {
		return errors.New("finalized before metadata commit")
	}
	s.finalized = append(s.finalized, id)
	return nil
}

func (s *mcpTestSecrets) PrepareDelete(_ context.Context, id credential.SecretID) error {
	s.prepareAttempts = append(s.prepareAttempts, id)
	if s.prepareErr != nil {
		return s.prepareErr
	}
	if s.candidates == nil {
		s.candidates = make(map[credential.SecretID]struct{})
	}
	s.candidates[id] = struct{}{}
	return nil
}

func (s *mcpTestSecrets) PendingDeleteCandidates(context.Context) ([]credential.SecretID, error) {
	if s.pendingErr != nil {
		return nil, s.pendingErr
	}
	ids := make([]credential.SecretID, 0, len(s.candidates))
	for id := range s.candidates {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func (s *mcpTestSecrets) ScheduleDelete(ctx context.Context, id credential.SecretID) error {
	s.deleteAttempts = append(s.deleteAttempts, id)
	if s.promotionErr != nil {
		return s.promotionErr
	}
	if _, ok := s.candidates[id]; !ok {
		return errors.New("deletion was not prepared")
	}
	if err := s.fakeEndpointSecrets.Delete(ctx, id); err != nil {
		return err
	}
	delete(s.candidates, id)
	return nil
}

func newMCPConfigEnv(t *testing.T) (capability.ConfigOperation, *mcpTestRepository, *mcpTestSecrets, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	repo := &mcpTestRepository{JSONStore: profile.NewJSONStore(path)}
	secrets := &mcpTestSecrets{fakeEndpointSecrets: &fakeEndpointSecrets{rows: map[string]string{rowA: refA, rowB: refB}}, repo: repo, targets: map[credential.SecretID]string{}}
	cfg, vlt, _, _, _, _ := testGates()
	op := capability.NewConfigOperation(cfg, vlt, testLane(), repo, repo, repo, repo, newProfileService(t), nil, secrets, secrets, capability.MCPConfigDependencies{Repository: repo, Secrets: secrets})
	return op, repo, secrets, path
}

func mcpStdioInput() capability.MCPServerConfigInput {
	return capability.MCPServerConfigInput{
		Name: "fixture", Enabled: true, Transport: profile.MCPTransportStdio,
		Stdio:  &capability.MCPStdioInput{Command: "/bin/fixture", Argv: []string{"--stdio"}, Cwd: "/tmp", Env: []capability.MCPEnvBindingInput{}},
		Limits: profile.MCPLimits{StartupTimeoutMS: 1000, CallTimeoutMS: 2000, IdleTimeoutMS: 0, MaxResultBytes: 4096},
	}
}

func mcpFresh(name, value string) capability.MCPEnvBindingInput {
	return capability.MCPEnvBindingInput{Name: name, Value: capability.MCPValueBindingInput{Kind: profile.MCPBindingSecret, SecretValue: &value}}
}

func createMCP(t *testing.T, op capability.ConfigOperation, input capability.MCPServerConfigInput) profile.MCPServer {
	t.Helper()
	var server profile.MCPServer
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		var err error
		server, err = svc.CreateMCPServer(ctx, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return server
}

func TestMCPConfigOneWayMaterialAndJournalOrdering(t *testing.T) {
	op, _, secrets, path := newMCPConfigEnv(t)
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "mcp-only-material"), {Name: "MODE", Value: capability.MCPValueBindingInput{Kind: profile.MCPBindingLiteral, Literal: profile.Ptr("safe")}}}
	created := createMCP(t, op, input)
	if len(secrets.minted) != 1 || !reflect.DeepEqual(secrets.finalized, secrets.minted) {
		t.Fatalf("mint/finalize = %v / %v", secrets.minted, secrets.finalized)
	}
	if secrets.targets[secrets.minted[0]] != created.ID {
		t.Fatal("journal target is not the committed identity")
	}
	if !created.Stdio.Env[0].Value.Owned || created.Stdio.Env[0].Value.SecretRef != string(secrets.minted[0]) {
		t.Fatal("fresh binding is not owned")
	}
	// #nosec G304 — newMCPConfigEnv constructed this profile document path under
	// t.TempDir(); reading the persisted fixture is the plaintext-leak assertion.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("mcp-only-material")) {
		t.Fatal("plaintext persisted")
	}
	input.Stdio.Env[0].Value = capability.MCPValueBindingInput{Kind: profile.MCPBindingSecret, Keep: true}
	input.Name = "renamed"
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		updated, err := svc.UpdateMCPServer(ctx, created.ID, created.Revision, input)
		if err != nil {
			return err
		}
		if updated.Name != "renamed" || updated.Stdio.Cwd != "/tmp" || *updated.Stdio.Env[1].Value.Literal != "safe" || updated.Stdio.Env[0].Value != created.Stdio.Env[0].Value {
			t.Fatal("configuration or kept binding changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(secrets.minted) != 1 || len(secrets.deleted) != 0 {
		t.Fatal("keep minted or destroyed material")
	}
	if len(secrets.prepareAttempts) != 0 {
		t.Fatal("rename prepared unchanged owned material for retirement")
	}
}

func TestMCPConfigRefusesAmbiguousSourcesAndKeepOnCreate(t *testing.T) {
	cases := []capability.MCPValueBindingInput{
		{Kind: profile.MCPBindingSecret},
		{Kind: profile.MCPBindingSecret, Keep: true},
		{Kind: profile.MCPBindingSecret, Keep: true, SecretValue: profile.Ptr("material")},
		{Kind: profile.MCPBindingSecret, Secret: profile.Ptr(rowA), SecretValue: profile.Ptr("material")},
		{Kind: profile.MCPBindingSecret, Secret: profile.Ptr(refA)},
		{Kind: profile.MCPBindingLiteral, Literal: profile.Ptr("literal"), SecretValue: profile.Ptr("material")},
		{Kind: profile.MCPBindingSecret, Literal: profile.Ptr("literal"), Secret: profile.Ptr(rowA)},
	}
	for i, binding := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			op, repo, secrets, _ := newMCPConfigEnv(t)
			input := mcpStdioInput()
			input.Stdio.Env = []capability.MCPEnvBindingInput{{Name: "TOKEN", Value: binding}}
			err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
				_, err := svc.CreateMCPServer(ctx, input)
				return err
			})
			if err == nil || len(secrets.minted) != 0 {
				t.Fatal("invalid binding was accepted or minted")
			}
			servers, err := repo.ListMCPServers()
			if err != nil || len(servers) != 0 {
				t.Fatalf("invalid binding persisted: %v", err)
			}
		})
	}
}

func TestMCPConfigCASBeforeMint(t *testing.T) {
	op, _, secrets, _ := newMCPConfigEnv(t)
	created := createMCP(t, op, mcpStdioInput())
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "material")}
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.UpdateMCPServer(ctx, created.ID, created.Revision+1, input)
		return err
	})
	if !errors.Is(err, profile.ErrMCPServerConflict) || len(secrets.minted) != 0 {
		t.Fatalf("stale CAS = %v; minted %v", err, secrets.minted)
	}
}

func TestMCPConfigPartialFailuresOnlyRetireKnownOrphans(t *testing.T) {
	for _, failure := range []string{"second-mint", "attach", "metadata", "uncertain-metadata", "query", "finalize"} {
		t.Run(failure, func(t *testing.T) {
			op, repo, secrets, _ := newMCPConfigEnv(t)
			input := mcpStdioInput()
			input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("ONE", "first"), mcpFresh("TWO", "second")}
			switch failure {
			case "second-mint":
				secrets.failMintAt = 2
			case "attach":
				secrets.attachErr = errors.New("attach failed")
			case "metadata":
				repo.writeErr = errors.New("save failed")
			case "uncertain-metadata":
				repo.writeErr = errors.New("save uncertain")
				repo.commitThenFail = true
			case "query":
				repo.writeErr = errors.New("save failed")
				repo.queryErr = errors.New("cannot establish orphanhood")
			case "finalize":
				secrets.finalizeErr = errors.New("finalize failed")
			}
			err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
				_, err := svc.CreateMCPServer(ctx, input)
				return err
			})
			if (err == nil) != (failure == "finalize") {
				t.Fatalf("result = %v", err)
			}
			retained := failure == "uncertain-metadata" || failure == "query" || failure == "finalize"
			if retained && len(secrets.deleteAttempts) != 0 {
				t.Fatal("deleted material whose outcome was not an orphan")
			}
			if !retained && !reflect.DeepEqual(secrets.deleted, secrets.minted) {
				t.Fatalf("orphan cleanup = %v, minted %v", secrets.deleted, secrets.minted)
			}
		})
	}
}

func TestMCPConfigOwnedCleanupHonorsAggregateReferences(t *testing.T) {
	for _, holder := range []string{"group", "endpoint-header", "mcp", "none"} {
		t.Run(holder, func(t *testing.T) {
			op, repo, secrets, _ := newMCPConfigEnv(t)
			input := mcpStdioInput()
			input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "material")}
			created := createMCP(t, op, input)
			ref := created.Stdio.Env[0].Value.SecretRef
			switch holder {
			case "group":
				if err := repo.CreateGroup(profile.ProfileGroup{ID: "group:holder", Name: "holder", Defaults: &profile.ProfileDefaults{SparseSSHOptions: profile.SparseSSHOptions{PasswordSecret: &ref}}}); err != nil {
					t.Fatal(err)
				}
			case "endpoint-header":
				ep := testEndpoint()
				ep.Headers = []profile.EndpointHeader{{Name: "X-Token", ValueRef: ref}}
				if err := repo.CreateEndpoint(ep); err != nil {
					t.Fatal(err)
				}
			case "mcp":
				secrets.rows[rowA] = ref
				other := mcpStdioInput()
				other.Stdio.Env = []capability.MCPEnvBindingInput{{Name: "TOKEN", Value: capability.MCPValueBindingInput{Kind: profile.MCPBindingSecret, Secret: profile.Ptr(rowA)}}}
				shared := createMCP(t, op, other)
				if shared.Stdio.Env[0].Value.Owned {
					t.Fatal("selected row became owned")
				}
			}
			if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
				return svc.DeleteMCPServer(ctx, created.ID, created.Revision)
			}); err != nil {
				t.Fatal(err)
			}
			if (len(secrets.deleted) == 1) != (holder == "none") {
				t.Fatalf("cleanup with %s holder: %v", holder, secrets.deleted)
			}
		})
	}
}

func TestMCPConfigDeleteFailureOrdering(t *testing.T) {
	for _, failure := range []string{"metadata", "deletion-intent"} {
		t.Run(failure, func(t *testing.T) {
			op, repo, secrets, _ := newMCPConfigEnv(t)
			input := mcpStdioInput()
			input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "material")}
			created := createMCP(t, op, input)
			if failure == "metadata" {
				repo.writeErr = errors.New("save failed")
			} else {
				secrets.deleteErr = errors.New("cannot persist deletion intent")
			}
			err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
				return svc.DeleteMCPServer(ctx, created.ID, created.Revision)
			})
			_, lookup := repo.GetMCPServer(created.ID)
			if failure == "metadata" {
				if err == nil || lookup != nil || len(secrets.deleteAttempts) != 0 {
					t.Fatal("metadata failure destroyed material or configuration")
				}
			} else if err == nil || !errors.Is(lookup, profile.ErrMCPServerNotFound) || len(secrets.deleteAttempts) != 1 {
				t.Fatal("deletion-intent failure restored metadata or falsely reported success")
			}
		})
	}
}

func TestMCPOAuthCommitForgetAndCASOwnership(t *testing.T) {
	op, repo, secrets, _ := newMCPConfigEnv(t)
	input := mcpStdioInput()
	input.Transport = profile.MCPTransportStreamableHTTP
	input.Stdio = nil
	input.HTTP = &capability.MCPHTTPInput{Endpoint: "https://example.test/mcp", Auth: profile.MCPHTTPAuthOAuth, Headers: []capability.MCPHeaderBindingInput{}, OAuth: &capability.MCPOAuthInput{Registration: profile.MCPOAuthDynamic, Scopes: []string{"tools"}}}
	created := createMCP(t, op, input)
	connected := profile.MCPOAuthConfig{SessionRef: &profile.MCPSecretBinding{SecretRef: refA, Owned: true}, Status: profile.MCPOAuthConnected, Issuer: "https://example.test", GrantedScopes: []string{"tools"}}
	var committed profile.MCPServer
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		var err error
		committed, err = svc.CommitMCPOAuth(ctx, created.ID, created.Revision, connected)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if committed.HTTP.OAuth.Registration != profile.MCPOAuthDynamic || !reflect.DeepEqual(committed.HTTP.OAuth.Scopes, []string{"tools"}) {
		t.Fatal("backend result replaced registration configuration")
	}
	connected.SessionRef = &profile.MCPSecretBinding{SecretRef: refB, Owned: true}
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CommitMCPOAuth(ctx, created.ID, created.Revision, connected)
		return err
	})
	if !errors.Is(err, profile.ErrMCPServerConflict) || !reflect.DeepEqual(secrets.deleted, []credential.SecretID{credential.SecretID(refB)}) {
		t.Fatal("CAS loser did not delete exactly its own orphan")
	}
	if forgetErr := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, commitErr := svc.ForgetMCPOAuth(ctx, committed.ID, committed.Revision)
		return commitErr
	}); forgetErr != nil {
		t.Fatal(forgetErr)
	}
	stored, err := repo.GetMCPServer(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.HTTP.OAuth.SessionRef != nil || stored.HTTP.OAuth.Status != profile.MCPOAuthMissing || !reflect.DeepEqual(secrets.deleted, []credential.SecretID{credential.SecretID(refB), credential.SecretID(refA)}) {
		t.Fatal("forget did not retire exactly the persisted session")
	}
}

func TestMCPOAuthFailedSavePreservesMaterialWhenMetadataCommitted(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) {
			op, repo, secrets, _ := newMCPConfigEnv(t)
			input := mcpStdioInput()
			input.Transport, input.Stdio = profile.MCPTransportStreamableHTTP, nil
			input.HTTP = &capability.MCPHTTPInput{Endpoint: "https://example.test/mcp", Auth: profile.MCPHTTPAuthOAuth, Headers: []capability.MCPHeaderBindingInput{}, OAuth: &capability.MCPOAuthInput{Registration: profile.MCPOAuthDynamic, Scopes: []string{}}}
			server := createMCP(t, op, input)
			repo.writeErr, repo.commitThenFail = errors.New("uncertain save failure"), committed
			authorization := profile.MCPOAuthConfig{SessionRef: &profile.MCPSecretBinding{SecretRef: refA, Owned: true}, Status: profile.MCPOAuthConnected, Issuer: "https://example.test", GrantedScopes: []string{}}
			err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
				_, err := svc.CommitMCPOAuth(ctx, server.ID, server.Revision, authorization)
				return err
			})
			if !errors.Is(err, repo.writeErr) {
				t.Fatalf("save result: %v", err)
			}
			stored, err := repo.GetMCPServer(server.ID)
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				if stored.HTTP.OAuth.SessionRef == nil || stored.HTTP.OAuth.SessionRef.SecretRef != refA || len(secrets.deleted) != 0 || len(secrets.finalized) != 0 {
					t.Fatal("uncertain committed metadata lost its material or recovery journal")
				}
			} else if stored.HTTP.OAuth.SessionRef != nil || !reflect.DeepEqual(secrets.deleted, []credential.SecretID{credential.SecretID(refA)}) {
				t.Fatal("failed uncommitted authorization retained an orphan or changed metadata")
			}
		})
	}
}

func TestMCPConfigGuardsEveryEscapedOperation(t *testing.T) {
	op, _, _, _ := newMCPConfigEnv(t)
	var escaped capability.ConfigService
	if err := runConfig(t, op, func(_ context.Context, svc capability.ConfigService) error { escaped = svc; return nil }); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	checks := []func() error{
		func() error { _, err := escaped.ListMCPServers(); return err },
		func() error { _, err := escaped.GetMCPServer("id"); return err },
		func() error { _, err := escaped.CreateMCPServer(ctx, mcpStdioInput()); return err },
		func() error { _, err := escaped.UpdateMCPServer(ctx, "id", 1, mcpStdioInput()); return err },
		func() error { return escaped.DeleteMCPServer(ctx, "id", 1) },
		func() error { _, err := escaped.SetMCPToolsEnabled("id", 1, nil); return err },
		func() error { _, err := escaped.RefreshMCPServerCatalog("id", 1, profile.MCPCatalog{}); return err },
		func() error { _, err := escaped.CommitMCPOAuth(ctx, "id", 1, profile.MCPOAuthConfig{}); return err },
		func() error { _, err := escaped.ForgetMCPOAuth(ctx, "id", 1); return err },
		func() error {
			_, err := escaped.SnapshotMCPMutation(capability.MCPMutationTarget{Kind: capability.MCPMutationReset})
			return err
		},
		func() error { return escaped.VerifyMCPMutation(capability.MCPMutationSnapshot{}) },
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("escaped operation %d succeeded", i)
		}
	}
}

func TestMCPConfigUpdateCleanupAndMetadataFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			op, repo, secrets, _ := newMCPConfigEnv(t)
			input := mcpStdioInput()
			input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "old-material")}
			created := createMCP(t, op, input)
			input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "new-material")}
			if failure {
				repo.writeErr = errors.New("save failed")
			}
			err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
				_, err := svc.UpdateMCPServer(ctx, created.ID, created.Revision, input)
				return err
			})
			if (err != nil) != failure {
				t.Fatalf("update = %v", err)
			}
			stored, err := repo.GetMCPServer(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			kept, retired := secrets.minted[1], secrets.minted[0]
			if failure {
				kept, retired = retired, kept
			}
			if stored.Stdio.Env[0].Value.SecretRef != string(kept) || !reflect.DeepEqual(secrets.deleted, []credential.SecretID{retired}) {
				t.Fatal("update retired the wrong material")
			}
		})
	}
}

func TestMCPConfigMissingDependencyRefusesRequestedMutation(t *testing.T) {
	cfg, vlt, _, _, _, _ := testGates()
	op := capability.NewConfigOperation(cfg, vlt, testLane(), &fakeProfileRepo{}, &fakeGroupRepo{}, nil, nil, newProfileService(t), nil, nil, nil, capability.MCPConfigDependencies{})
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateMCPServer(ctx, mcpStdioInput())
		return err
	})
	if !errors.Is(err, capability.ErrMCPUnavailable) {
		t.Fatalf("missing MCP dependency = %v", err)
	}
}

type mcpInventoryResolver struct{ seen []vault.CredentialInventory }

func (r *mcpInventoryResolver) ResolveRow(_ string, inputs []vault.CredentialInventory) (credential.SecretID, bool) {
	r.seen = inputs
	for _, input := range inputs {
		if input.SecretID == refA {
			return credential.SecretID(refA), true
		}
	}
	return "", false
}

func TestMCPConfigUsesCurrentInventoryForRowSelection(t *testing.T) {
	_, repo, secrets, _ := newMCPConfigEnv(t)
	if err := repo.CreateProfile(profile.SSHProfile{Base: profile.Base{ID: "profile:legacy", Name: "legacy"}, Options: profile.StoredSSHProfileOptions{Host: "example.test", PasswordSecret: refA}}); err != nil {
		t.Fatal(err)
	}
	rows := &mcpInventoryResolver{}
	cfg, vlt, _, _, _, _ := testGates()
	op := capability.NewConfigOperation(cfg, vlt, testLane(), repo, repo, repo, repo, newProfileService(t), nil, rows, secrets, capability.MCPConfigDependencies{Repository: repo, Secrets: secrets})
	created := createMCP(t, op, mcpSharedInput(rowA))
	if len(rows.seen) == 0 || created.Stdio.Env[0].Value.SecretRef != refA || created.Stdio.Env[0].Value.Owned {
		t.Fatal("row selection omitted current inventory or acquired ownership")
	}
}

func TestMCPConfigCatalogActionsRemainIndependentOfConfig(t *testing.T) {
	op, _, _, _ := newMCPConfigEnv(t)
	created := createMCP(t, op, mcpStdioInput())
	if err := runConfig(t, op, func(_ context.Context, svc capability.ConfigService) error {
		catalog := profile.MCPCatalog{Tools: []profile.MCPTool{{Name: "echo", InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`)}}}
		refreshed, err := svc.RefreshMCPServerCatalog(created.ID, created.Revision, catalog)
		if err != nil {
			return err
		}
		if refreshed.Catalog.State != profile.MCPCatalogFresh || len(refreshed.Catalog.Tools) != 1 || refreshed.Catalog.Tools[0].Enabled {
			t.Fatal("refresh did not install a disabled fresh catalog")
		}
		enabled, err := svc.SetMCPToolsEnabled(refreshed.ID, refreshed.Revision, []string{"echo"})
		if err != nil {
			return err
		}
		if !enabled.Catalog.Tools[0].Enabled || enabled.Stdio.Command != created.Stdio.Command || enabled.Stdio.Cwd != created.Stdio.Cwd {
			t.Fatal("tool enablement changed configuration")
		}
		again, err := svc.RefreshMCPServerCatalog(enabled.ID, enabled.Revision, catalog)
		if err != nil {
			return err
		}
		if !again.Catalog.Tools[0].Enabled {
			t.Fatal("unchanged catalog lost enablement")
		}
		_, err = svc.SetMCPToolsEnabled(again.ID, created.Revision, nil)
		if !errors.Is(err, profile.ErrMCPServerConflict) {
			t.Fatalf("stale catalog action = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMCPInputValidationIsPureAndDistinguishesCreateFromUpdate(t *testing.T) {
	input := mcpSharedInput(rowA)
	if err := capability.ValidateMCPServerConfigInput(input, false); err != nil {
		t.Fatal(err)
	}
	input.Stdio.Env[0].Value = capability.MCPValueBindingInput{Kind: profile.MCPBindingSecret, Keep: true}
	if err := capability.ValidateMCPServerConfigInput(input, false); err == nil {
		t.Fatal("create accepted keep")
	}
	if err := capability.ValidateMCPServerConfigInput(input, true); err != nil {
		t.Fatal(err)
	}
	input.Stdio.Env[0].Value.SecretValue = profile.Ptr("material")
	if err := capability.ValidateMCPServerConfigInput(input, true); err == nil {
		t.Fatal("update accepted two sources")
	}
	input = mcpStdioInput()
	input.Stdio.Cwd = "relative"
	if err := capability.ValidateMCPServerConfigInput(input, false); err == nil {
		t.Fatal("input bypassed profile configuration validation")
	}
	input = mcpStdioInput()
	input.Transport = profile.MCPTransportStreamableHTTP
	input.Stdio = nil
	input.HTTP = &capability.MCPHTTPInput{
		Endpoint: "https://example.test/mcp", Auth: profile.MCPHTTPAuthOAuth,
		Headers: []capability.MCPHeaderBindingInput{},
		OAuth:   &capability.MCPOAuthInput{Registration: profile.MCPOAuthPreregistered, ClientID: "client", Scopes: []string{"tools"}, ClientSecret: &capability.MCPSecretBindingInput{SecretValue: profile.Ptr("client-material")}},
	}
	if err := capability.ValidateMCPServerConfigInput(input, false); err != nil {
		t.Fatal(err)
	}
	input.HTTP.Headers = []capability.MCPHeaderBindingInput{{Name: "Authorization", Value: capability.MCPValueBindingInput{Kind: profile.MCPBindingLiteral, Literal: profile.Ptr("value")}}}
	if err := capability.ValidateMCPServerConfigInput(input, false); err == nil {
		t.Fatal("reserved HTTP header accepted")
	}
}

func TestMCPInputValidationCompletesBeforeAnyMint(t *testing.T) {
	for _, failure := range []string{"late-source", "config"} {
		t.Run(failure, func(t *testing.T) {
			op, _, secrets, _ := newMCPConfigEnv(t)
			input := mcpStdioInput()
			input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("FIRST", "material")}
			if failure == "late-source" {
				input.Stdio.Env = append(input.Stdio.Env, capability.MCPEnvBindingInput{Name: "SECOND", Value: capability.MCPValueBindingInput{Kind: profile.MCPBindingSecret, Secret: profile.Ptr(rowA), Keep: true}})
			} else {
				input.Stdio.Cwd = "relative"
			}
			err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
				_, err := svc.CreateMCPServer(ctx, input)
				return err
			})
			if err == nil || len(secrets.minted) != 0 {
				t.Fatal("service minted before validating the entire input")
			}
		})
	}
}
