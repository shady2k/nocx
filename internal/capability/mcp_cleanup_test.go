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

func newMCPReconcileOperation(t *testing.T, repo *mcpTestRepository, secrets *mcpTestSecrets) capability.ConfigOperation {
	t.Helper()
	cfg, vlt, _, _, _, _ := testGates()
	return capability.NewConfigOperation(cfg, vlt, testLane(), repo, repo, repo, repo, newProfileService(t), nil, secrets, secrets, capability.MCPConfigDependencies{Repository: repo, Secrets: secrets})
}

func reconcileMCP(op capability.ConfigOperation) error {
	return op.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
		return svc.ReconcileMCPSecrets(ctx)
	})
}

func TestMCPCleanupPreparationFailurePreservesMetadata(t *testing.T) {
	for _, action := range []string{"update", "delete", "oauth-forget"} {
		t.Run(action, func(t *testing.T) {
			op, repo, secrets, _ := newMCPConfigEnv(t)
			input := mcpStdioInput()
			input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "material")}
			if action == "oauth-forget" {
				input.Stdio = nil
				input.Transport = profile.MCPTransportStreamableHTTP
				input.HTTP = &capability.MCPHTTPInput{Endpoint: "https://example.test/mcp", Auth: profile.MCPHTTPAuthOAuth, Headers: []capability.MCPHeaderBindingInput{}, OAuth: &capability.MCPOAuthInput{Registration: profile.MCPOAuthDynamic, Scopes: []string{}}}
			}
			created := createMCP(t, op, input)
			if action == "oauth-forget" {
				if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
					var err error
					created, err = svc.CommitMCPOAuth(ctx, created.ID, created.Revision, profile.MCPOAuthConfig{Status: profile.MCPOAuthConnected, SessionRef: &profile.MCPSecretBinding{SecretRef: refA, Owned: true}, GrantedScopes: []string{}})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			prepareErr := errors.New("candidate save failed")
			secrets.prepareErr = prepareErr
			err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
				switch action {
				case "update":
					_, err := svc.UpdateMCPServer(ctx, created.ID, created.Revision, mcpStdioInput())
					return err
				case "delete":
					return svc.DeleteMCPServer(ctx, created.ID, created.Revision)
				default:
					_, err := svc.ForgetMCPOAuth(ctx, created.ID, created.Revision)
					return err
				}
			})
			stored, lookupErr := repo.GetMCPServer(created.ID)
			if !errors.Is(err, prepareErr) || lookupErr != nil || !reflect.DeepEqual(stored, created) || len(secrets.deleteAttempts) != 0 {
				t.Fatalf("prepare failure changed metadata/material: operation=%v lookup=%v", err, lookupErr)
			}
		})
	}
}

func TestMCPCleanupPromotionFailureSurvivesNewOperation(t *testing.T) {
	op, repo, secrets, path := newMCPConfigEnv(t)
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "material")}
	created := createMCP(t, op, input)
	id := secrets.minted[0]
	promotionErr := errors.New("promotion save failed")
	secrets.promotionErr = promotionErr
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.DeleteMCPServer(ctx, created.ID, created.Revision)
	})
	if !errors.Is(err, promotionErr) {
		t.Fatalf("delete = %v", err)
	}
	if _, err := repo.GetMCPServer(created.ID); !errors.Is(err, profile.ErrMCPServerNotFound) {
		t.Fatal("committed deletion was restored")
	}
	if _, pending := secrets.candidates[id]; !pending || len(secrets.deleted) != 0 {
		t.Fatal("promotion failure lost its candidate")
	}
	// A new service has no callback-local retired-ref list. Only the durable
	// candidate seam and a reloaded aggregate can drive the cleanup.
	reloaded := &mcpTestRepository{JSONStore: profile.NewJSONStore(path)}
	recovered := &mcpTestSecrets{fakeEndpointSecrets: secrets.fakeEndpointSecrets, repo: reloaded, targets: secrets.targets, candidates: secrets.candidates}
	if err := reconcileMCP(newMCPReconcileOperation(t, reloaded, recovered)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recovered.deleted, []credential.SecretID{id}) || len(recovered.candidates) != 0 {
		t.Fatal("new operation did not retire the persisted candidate")
	}
}

func TestMCPCleanupFailedProfileWriteKeepsReferencedCandidate(t *testing.T) {
	op, repo, secrets, _ := newMCPConfigEnv(t)
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "material")}
	created := createMCP(t, op, input)
	id := secrets.minted[0]
	saveErr := errors.New("profile save failed")
	repo.writeErr = saveErr
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.UpdateMCPServer(ctx, created.ID, created.Revision, mcpStdioInput())
		return err
	})
	if !errors.Is(err, saveErr) {
		t.Fatalf("update = %v", err)
	}
	if _, pending := secrets.candidates[id]; !pending {
		t.Fatal("failed metadata write discarded prepared identity")
	}
	repo.writeErr = nil
	if reconcileErr := reconcileMCP(newMCPReconcileOperation(t, repo, secrets)); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	stored, err := repo.GetMCPServer(created.ID)
	if err != nil || !reflect.DeepEqual(stored, created) || len(secrets.deleteAttempts) != 0 {
		t.Fatal("reconciliation deleted a still-referenced candidate")
	}
}

func TestMCPCleanupUncertainCommittedProfileWriteRetainsRecoveryIdentity(t *testing.T) {
	op, repo, secrets, _ := newMCPConfigEnv(t)
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "material")}
	created := createMCP(t, op, input)
	id := secrets.minted[0]
	saveErr := errors.New("save outcome uncertain")
	promotionErr := errors.New("promotion failed")
	repo.writeErr, repo.commitThenFail = saveErr, true
	secrets.promotionErr = promotionErr
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.UpdateMCPServer(ctx, created.ID, created.Revision, mcpStdioInput())
		return err
	})
	if !errors.Is(err, saveErr) || !errors.Is(err, promotionErr) {
		t.Fatalf("uncertain save = %v", err)
	}
	stored, err := repo.GetMCPServer(created.ID)
	if err != nil || len(profile.MCPSecretReferences(stored, false)) != 0 {
		t.Fatal("fixture did not commit reference removal")
	}
	if _, pending := secrets.candidates[id]; !pending {
		t.Fatal("uncertain committed write lost retirement identity")
	}
	repo.writeErr, repo.commitThenFail, secrets.promotionErr = nil, false, nil
	if err := reconcileMCP(newMCPReconcileOperation(t, repo, secrets)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secrets.deleted, []credential.SecretID{id}) {
		t.Fatal("recovery did not retire the orphan")
	}
}

func TestMCPCleanupSharedCandidateRetainedUntilLastHolderRemoved(t *testing.T) {
	op, repo, secrets, _ := newMCPConfigEnv(t)
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "material")}
	created := createMCP(t, op, input)
	id := secrets.minted[0]
	ref := string(id)
	group := profile.ProfileGroup{ID: "group:cleanup-holder", Name: "holder", Defaults: &profile.ProfileDefaults{SparseSSHOptions: profile.SparseSSHOptions{PasswordSecret: &ref}}}
	if err := repo.CreateGroup(group); err != nil {
		t.Fatal(err)
	}
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.DeleteMCPServer(ctx, created.ID, created.Revision)
	}); err != nil {
		t.Fatal(err)
	}
	if _, pending := secrets.candidates[id]; !pending || len(secrets.deleteAttempts) != 0 {
		t.Fatal("shared owned material lost candidate or was promoted")
	}
	if err := repo.DeleteGroup(group.ID); err != nil {
		t.Fatal(err)
	}
	if err := reconcileMCP(newMCPReconcileOperation(t, repo, secrets)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secrets.deleted, []credential.SecretID{id}) {
		t.Fatal("last-holder removal did not permit candidate retirement")
	}
}

func TestMCPCleanupReconciliationGuardsAndPropagatesErrors(t *testing.T) {
	op, repo, secrets, _ := newMCPConfigEnv(t)
	var escaped capability.ConfigService
	if err := runConfig(t, op, func(_ context.Context, svc capability.ConfigService) error { escaped = svc; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := escaped.ReconcileMCPSecrets(context.Background()); err == nil {
		t.Fatal("unguarded reconciliation succeeded")
	}
	pendingErr := errors.New("candidate read failed")
	secrets.pendingErr = pendingErr
	if err := reconcileMCP(op); !errors.Is(err, pendingErr) {
		t.Fatalf("pending read = %v", err)
	}
	secrets.pendingErr = vault.ErrVaultUninitialized
	if err := reconcileMCP(op); err != nil {
		t.Fatalf("uninitialized vault = %v", err)
	}
	secrets.pendingErr = nil
	if err := secrets.PrepareDelete(context.Background(), credential.SecretID(refA)); err != nil {
		t.Fatal(err)
	}
	queryErr := errors.New("aggregate read failed")
	repo.queryErr = queryErr
	if err := reconcileMCP(op); !errors.Is(err, queryErr) || len(secrets.deleteAttempts) != 0 {
		t.Fatalf("reference query = %v", err)
	}
}

type mcpPromotionFaultVault struct {
	*vault.Vault
	promotionErr error
}

func (v *mcpPromotionFaultVault) ScheduleDelete(ctx context.Context, id credential.SecretID) error {
	if v.promotionErr != nil {
		return v.promotionErr
	}
	return v.Vault.ScheduleDelete(ctx, id)
}

func TestMCPCleanupRealVaultCandidateSurvivesSealedRestart(t *testing.T) {
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
	if _, err := v.Setup(ctx, vault.SetupRequest{Passphrase: "isolated-mcp-cleanup"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "profiles.json")
	store := profile.NewJSONStore(path)
	faults := &mcpPromotionFaultVault{Vault: v}
	operation := func(repo *profile.JSONStore, secrets *mcpPromotionFaultVault) capability.ConfigOperation {
		cfg, vlt, _, _, _, _ := testGates()
		return capability.NewConfigOperation(cfg, vlt, testLane(), repo, repo, repo, repo, newProfileService(t), nil, secrets, secrets, capability.MCPConfigDependencies{Repository: repo, Secrets: secrets})
	}
	op := operation(store, faults)
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "isolated-owned-material")}
	created := createMCP(t, op, input)
	id := credential.SecretID(created.Stdio.Env[0].Value.SecretRef)
	v.Seal()
	promotionErr := errors.New("injected promotion save failure")
	faults.promotionErr = promotionErr
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.DeleteMCPServer(ctx, created.ID, created.Revision)
	}); !errors.Is(err, promotionErr) {
		t.Fatalf("sealed delete = %v", err)
	}
	v.Close()
	v = open()
	pending, err := v.PendingDeleteCandidates(ctx)
	if err != nil || !reflect.DeepEqual(pending, []credential.SecretID{id}) {
		t.Fatalf("reopened candidates = %v, %v", pending, err)
	}
	// Unseal must not execute even an orphaned candidate without the guarded
	// aggregate check. The old value proves the candidate remains nonexecuting.
	if unsealErr := v.Unseal(ctx, vault.UnsealRequest{Passphrase: "isolated-mcp-cleanup"}); unsealErr != nil {
		t.Fatal(unsealErr)
	}
	if _, getErr := v.Get(ctx, id); getErr != nil {
		t.Fatalf("unseal executed candidate: %v", getErr)
	}
	v.Seal()
	if reconcileErr := reconcileMCP(operation(profile.NewJSONStore(path), &mcpPromotionFaultVault{Vault: v})); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	pending, err = v.PendingDeleteCandidates(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("promoted candidates = %v, %v", pending, err)
	}
	if err := v.Unseal(ctx, vault.UnsealRequest{Passphrase: "isolated-mcp-cleanup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, id); err == nil {
		t.Fatal("promoted deletion did not retire the material")
	}
}

func TestMCPCleanupFreshRollbackRetainsFailedPromotion(t *testing.T) {
	op, repo, secrets, _ := newMCPConfigEnv(t)
	input := mcpStdioInput()
	input.Stdio.Env = []capability.MCPEnvBindingInput{mcpFresh("TOKEN", "orphan-material")}
	saveErr := errors.New("create metadata failed")
	promotionErr := errors.New("rollback promotion failed")
	repo.writeErr, secrets.promotionErr = saveErr, promotionErr
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateMCPServer(ctx, input)
		return err
	})
	if !errors.Is(err, saveErr) || !errors.Is(err, promotionErr) {
		t.Fatalf("create rollback = %v", err)
	}
	id := secrets.minted[0]
	if _, pending := secrets.candidates[id]; !pending {
		t.Fatal("rollback lost the orphan identity")
	}
	repo.writeErr, secrets.promotionErr = nil, nil
	if err := reconcileMCP(newMCPReconcileOperation(t, repo, secrets)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secrets.deleted, []credential.SecretID{id}) {
		t.Fatal("reconciliation did not retire the orphan from failed create")
	}
}
