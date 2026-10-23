package capability_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

func mcpSharedInput(row string) capability.MCPServerConfigInput {
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{{Name: "TOKEN", Value: capability.MCPValueBindingInput{Kind: profile.MCPBindingSecret, Secret: &row}}}
	return input
}

func snapshotMCP(t *testing.T, op capability.ConfigOperation, target capability.MCPMutationTarget) capability.MCPMutationSnapshot {
	t.Helper()
	var snapshot capability.MCPMutationSnapshot
	if err := runConfig(t, op, func(_ context.Context, svc capability.ConfigService) error {
		var err error
		snapshot, err = svc.SnapshotMCPMutation(target)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func verifyMCP(op capability.ConfigOperation, snapshot capability.MCPMutationSnapshot) error {
	return op.Run(context.Background(), func(_ context.Context, svc capability.ConfigService) error { return svc.VerifyMCPMutation(snapshot) })
}

func TestMCPMutationRecomputesEntireSharedHolderSet(t *testing.T) {
	for _, change := range []string{"unchanged", "new-holder", "removed-holder", "changed-holder", "different-row-target", "unaffected-server"} {
		t.Run(change, func(t *testing.T) {
			op, repo, secrets, _ := newMCPConfigEnv(t)
			server := createMCP(t, op, mcpSharedInput(rowA))
			snapshot := snapshotMCP(t, op, capability.MCPMutationTarget{Kind: capability.MCPMutationSecretRow, SecretRow: rowA})
			if !reflect.DeepEqual(snapshot.Servers, []capability.MCPServerIdentity{{ID: server.ID, Revision: server.Revision}}) || !reflect.DeepEqual(snapshot.SecretIDs, []credential.SecretID{credential.SecretID(refA)}) {
				t.Fatal("wrong affected identities")
			}
			switch change {
			case "new-holder":
				createMCP(t, op, mcpSharedInput(rowA))
			case "removed-holder":
				if _, err := repo.DeleteMCPServer(server.ID, server.Revision); err != nil {
					t.Fatal(err)
				}
			case "changed-holder":
				server.Name = "changed"
				if _, err := repo.UpdateMCPServer(server); err != nil {
					t.Fatal(err)
				}
			case "different-row-target":
				secrets.rows[rowA] = refB
			case "unaffected-server":
				createMCP(t, op, mcpSharedInput(rowB))
			}
			err := verifyMCP(op, snapshot)
			wantConflict := change != "unchanged" && change != "unaffected-server"
			if errors.Is(err, profile.ErrMCPServerConflict) != wantConflict || (!wantConflict && err != nil) {
				t.Fatalf("verify %s = %v", change, err)
			}
		})
	}
}

func TestMCPMutationEndpointUsesCurrentIdentityAndEmptyMatchesNone(t *testing.T) {
	op, repo, _, _ := newMCPConfigEnv(t)
	server := createMCP(t, op, mcpSharedInput(rowA))
	ep := testEndpoint()
	if err := repo.CreateEndpoint(ep); err != nil {
		t.Fatal(err)
	}
	target := capability.MCPMutationTarget{Kind: capability.MCPMutationEndpointCredential, EndpointID: ep.ID}
	empty := snapshotMCP(t, op, target)
	if len(empty.Servers) != 0 || len(empty.SecretIDs) != 0 {
		t.Fatal("empty credential selected every server")
	}
	ep.CredentialRef = refA
	if err := repo.UpdateEndpoint(ep); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(verifyMCP(op, empty), profile.ErrMCPServerConflict) {
		t.Fatal("new endpoint credential escaped snapshot")
	}
	snapshot := snapshotMCP(t, op, target)
	if len(snapshot.Servers) != 1 || snapshot.Servers[0].ID != server.ID || snapshot.SecretIDs[0] != credential.SecretID(refA) {
		t.Fatal("endpoint did not capture its current holder")
	}
	ep.CredentialRef = refB
	if err := repo.UpdateEndpoint(ep); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(verifyMCP(op, snapshot), profile.ErrMCPServerConflict) {
		t.Fatal("changed credential target escaped snapshot")
	}
}

func TestMCPMutationRejectsTamperedAndForeignSnapshots(t *testing.T) {
	op, _, _, _ := newMCPConfigEnv(t)
	server := createMCP(t, op, mcpSharedInput(rowA))
	target := capability.MCPMutationTarget{Kind: capability.MCPMutationServer, ServerID: server.ID}
	for _, field := range []string{"servers", "secrets", "revision", "zero"} {
		t.Run(field, func(t *testing.T) {
			snapshot := snapshotMCP(t, op, target)
			switch field {
			case "servers":
				snapshot.Servers = nil
			case "secrets":
				snapshot.SecretIDs[0] = credential.SecretID(refB)
			case "revision":
				snapshot.Servers[0].Revision++
			case "zero":
				snapshot = capability.MCPMutationSnapshot{}
			}
			if !errors.Is(verifyMCP(op, snapshot), profile.ErrMCPServerConflict) {
				t.Fatal("tampered snapshot accepted")
			}
		})
	}
}

func TestMCPMutationSecretCommitServiceVerifiesBeforeMutation(t *testing.T) {
	op, repo, _, _ := newMCPConfigEnv(t)
	createMCP(t, op, mcpSharedInput(rowA))
	snapshot := snapshotMCP(t, op, capability.MCPMutationTarget{Kind: capability.MCPMutationSecretRow, SecretRow: rowA})
	secrets := newFakeVault()
	secrets.rows[rowA] = credential.SecretID(refA)
	cfg, vlt, _, _, _, _ := testGates()
	secretOp := capability.NewSecretOperation(cfg, vlt, testLane(), repo, repo, secrets, secrets, capability.MCPConfigDependencies{Repository: repo})
	var escaped capability.SecretService
	if err := secretOp.Run(context.Background(), func(_ context.Context, svc capability.SecretService) error {
		escaped = svc
		return svc.VerifyMCPMutation(snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	if escaped.VerifyMCPMutation(snapshot) == nil {
		t.Fatal("escaped secret verification succeeded")
	}
	createMCP(t, op, mcpSharedInput(rowA))
	committed := false
	err := secretOp.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
		if err := svc.VerifyMCPMutation(snapshot); err != nil {
			return err
		}
		committed = true
		return svc.ReplaceSecret(ctx, rowA, credential.NewSecret("replacement"))
	})
	if !errors.Is(err, profile.ErrMCPServerConflict) || committed {
		t.Fatal("new holder escaped secret committing service")
	}
}

func TestMCPMutationResetOnlyIncludesReferencedServersAndRevalidates(t *testing.T) {
	op, repo, _, _ := newMCPConfigEnv(t)
	createMCP(t, op, mcpStdioInput())
	holder := createMCP(t, op, mcpSharedInput(rowA))
	snapshot := snapshotMCP(t, op, capability.MCPMutationTarget{Kind: capability.MCPMutationReset})
	if len(snapshot.Servers) != 1 || snapshot.Servers[0].ID != holder.ID {
		t.Fatal("reset included unreferenced server")
	}
	cfg, vlt, _, _, _, _ := testGates()
	reset := &fakeReset{}
	resetOp := capability.NewVaultResetOperation(cfg, vlt, testLane(), reset, capability.MCPConfigDependencies{Repository: repo})
	var escaped capability.VaultResetService
	if err := resetOp.Run(context.Background(), func(_ context.Context, svc capability.VaultResetService) error {
		escaped = svc
		return svc.VerifyMCPMutation(snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	if escaped.VerifyMCPMutation(snapshot) == nil {
		t.Fatal("escaped reset verification succeeded")
	}
	createMCP(t, op, mcpSharedInput(rowB))
	err := resetOp.Run(context.Background(), func(ctx context.Context, svc capability.VaultResetService) error {
		if err := svc.VerifyMCPMutation(snapshot); err != nil {
			return err
		}
		_, err := svc.Execute(ctx)
		return err
	})
	if !errors.Is(err, profile.ErrMCPServerConflict) || reset.execute != 0 {
		t.Fatal("new holder escaped reset committing service")
	}
}

func TestMCPConfigForgedSystemOnlyRowsRefusedBeforeAndAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	documents := storage.NewDocumentStore(dir)
	open := func() *vault.Vault {
		registry, err := vault.NewRegistry(file.New(documents, "vault-blob.json"))
		if err != nil {
			t.Fatal(err)
		}
		v, err := vault.New(documents, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(v.Close)
		return v
	}
	v := open()
	if _, err := v.Setup(ctx, vault.SetupRequest{Passphrase: "isolated-mcp-test"}); err != nil {
		t.Fatal(err)
	}
	id, err := v.CreateSystemSecret(ctx, credential.NewSecret("system-only-token"))
	if err != nil {
		t.Fatal(err)
	}
	store := profile.NewJSONStore(filepath.Join(dir, "profiles.json"))
	// The legacy inventory fallback must not make a forged row selectable.
	if err := store.CreateProfile(profile.SSHProfile{Base: profile.Base{ID: "profile:legacy", Name: "legacy"}, Options: profile.StoredSSHProfileOptions{Host: "example.test", PasswordSecret: string(id)}}); err != nil {
		t.Fatal(err)
	}
	for pass := range 2 {
		cfg, vlt, _, _, _, _ := testGates()
		op := capability.NewConfigOperation(cfg, vlt, testLane(), store, store, store, store, newProfileService(t), nil, v, v, capability.MCPConfigDependencies{Repository: store, Secrets: v})
		input := mcpSharedInput(vault.RowFor(id))
		err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
			_, err := svc.CreateMCPServer(ctx, input)
			return err
		})
		if err == nil {
			t.Fatal("system-only secret selectable in MCP configuration")
		}
		err = runConfig(t, op, func(_ context.Context, svc capability.ConfigService) error {
			_, snapshotErr := svc.SnapshotMCPMutation(capability.MCPMutationTarget{Kind: capability.MCPMutationSecretRow, SecretRow: vault.RowFor(id)})
			return snapshotErr
		})
		if err == nil {
			t.Fatal("system-only secret selectable for ordinary mutation")
		}
		v.Close()
		if pass == 0 {
			v = open()
			if err := v.Unseal(ctx, vault.UnsealRequest{Passphrase: "isolated-mcp-test"}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMCPMutationAbsentEndpointRetainsIdempotentDeletion(t *testing.T) {
	op, repo, _, _ := newMCPConfigEnv(t)
	createMCP(t, op, mcpSharedInput(rowA))
	endpoint := testEndpoint()
	target := capability.MCPMutationTarget{Kind: capability.MCPMutationEndpointCredential, EndpointID: endpoint.ID}
	snapshot := snapshotMCP(t, op, target)
	if len(snapshot.Servers) != 0 || len(snapshot.SecretIDs) != 0 {
		t.Fatal("absent endpoint captured unrelated holders")
	}
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		if err := svc.VerifyMCPMutation(snapshot); err != nil {
			return err
		}
		return svc.DeleteEndpoint(ctx, endpoint.ID)
	}); err != nil {
		t.Fatalf("absent endpoint deletion: %v", err)
	}
	// A new endpoint conflicts even without credentials: target identity,
	// rather than only the set of affected MCP servers, must be compared.
	if err := repo.CreateEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	if err := verifyMCP(op, snapshot); !errors.Is(err, profile.ErrMCPServerConflict) {
		t.Fatalf("endpoint created during drain = %v, want conflict", err)
	}
}
