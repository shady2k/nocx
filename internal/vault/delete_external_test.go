package vault_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

// Faults are injected at the real document boundary, not by replacing the
// encrypted file provider with a fake that cannot prove restart recovery.
type deletionStore struct {
	storage.DocumentStore
	failName          string
	failWrite         int
	writes            int
	blobReads         int
	blobWrites        int
	failedWrite       int
	commitBeforeError bool
	failRead          bool
}

var errDeletionSave = errors.New("injected deletion persistence failure")

func (s *deletionStore) Read(name string, into any) (bool, error) {
	if s.failRead && name == "vault.json" {
		return false, errors.New("injected deletion readback failure")
	}
	if name == "vault-blob.json" {
		s.blobReads++
	}
	return s.DocumentStore.Read(name, into)
}

func (s *deletionStore) Write(name string, value any) error {
	if name == "vault-blob.json" {
		s.blobWrites++
	}
	if name == s.failName {
		s.writes++
		if s.failWrite == 0 || s.writes == s.failWrite {
			s.failedWrite++
			if s.commitBeforeError {
				if err := s.DocumentStore.Write(name, value); err != nil {
					return err
				}
			}
			return errDeletionSave
		}
	}
	return s.DocumentStore.Write(name, value)
}

func openDeletionVault(t *testing.T, docs storage.DocumentStore) *vault.Vault {
	t.Helper()
	reg, err := vault.NewRegistry(file.New(docs, "vault-blob.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(docs, reg, logtest.Slog(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	return v
}

func setupDeletionVault(t *testing.T) (*vault.Vault, *deletionStore) {
	t.Helper()
	docs := &deletionStore{DocumentStore: storage.NewDocumentStore(t.TempDir())}
	v := openDeletionVault(t, docs)
	if _, err := v.Setup(context.Background(), vault.SetupRequest{Passphrase: "deletion-test-pass"}); err != nil {
		t.Fatal(err)
	}
	return v, docs
}

func unsealDeletionVault(t *testing.T, v *vault.Vault) {
	t.Helper()
	if err := v.Unseal(context.Background(), vault.UnsealRequest{Passphrase: "deletion-test-pass"}); err != nil {
		t.Fatal(err)
	}
}

func deletionDocumentOnDisk(t *testing.T, docs storage.DocumentStore) vault.Document {
	t.Helper()
	var doc vault.Document
	if found, err := docs.Read("vault.json", &doc); err != nil || !found {
		t.Fatalf("read durable vault document: found=%v error=%v", found, err)
	}
	return doc
}

func assertDeletionIntent(t *testing.T, docs storage.DocumentStore, id credential.SecretID, hidden bool) {
	t.Helper()
	doc := deletionDocumentOnDisk(t, docs)
	count := 0
	for _, entry := range doc.Journal {
		if entry.NewID == id && entry.Op == "delete" {
			count++
			if entry.Phase != vault.PhasePrepared || entry.SystemOnly != hidden {
				t.Fatalf("incorrect durable deletion intent: %+v", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("durable deletion intents = %d, want exactly one", count)
	}
	for _, rec := range doc.Secrets {
		if rec.ID == id {
			t.Fatal("scheduled deletion retained a catalogue record")
		}
	}
}

func assertDeletionComplete(t *testing.T, v *vault.Vault, docs storage.DocumentStore, id credential.SecretID) {
	t.Helper()
	if _, err := v.Get(context.Background(), id); !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("provider material remains after recovery: %v", err)
	}
	doc := deletionDocumentOnDisk(t, docs)
	for _, entry := range doc.Journal {
		if entry.NewID == id {
			t.Fatalf("completed deletion retained journal entry: %+v", entry)
		}
	}
	for _, rec := range doc.Secrets {
		if rec.ID == id {
			t.Fatal("completed deletion retained catalogue record")
		}
	}
}

func assertDeletionHidden(t *testing.T, v *vault.Vault, id credential.SecretID) {
	t.Helper()
	row := vault.RowFor(id)
	for _, inventory := range [][]vault.CredentialInventory{
		nil,
		{{ID: "forged-owner", SecretID: string(id)}},
		{{ID: "forged-passphrase", PassphraseSecretID: string(id)}},
		{{ID: "forged-key", KeyMaterialSecretID: string(id)}},
	} {
		if _, ok := v.ResolveRow(row, inventory); ok {
			t.Fatal("hidden material became selectable by a forged row")
		}
		entries, err := v.BuildInventory(context.Background(), inventory)
		if v.State() == vault.StateSealed {
			if !errors.Is(err, vault.ErrVaultSealed) {
				t.Fatalf("sealed inventory returned %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.ID == row {
				t.Fatal("hidden material leaked through inventory")
			}
		}
	}
}

func TestScheduleDelete_SealedFileProviderRestart(t *testing.T) {
	for _, state := range []string{"hidden-finalized", "ordinary-finalized", "hidden-pending-create", "hidden-pending-target"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			v, docs := setupDeletionVault(t)
			hidden := state != "ordinary-finalized"
			var id credential.SecretID
			var err error
			if hidden {
				id, err = v.CreateSystemSecret(ctx, credential.NewSecret("hidden-oauth-session"))
			} else {
				id, err = v.Create(ctx, credential.NewSecret("backend-owned-password"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if hidden {
				assertDeletionHidden(t, v, id)
			}
			if state != "hidden-pending-create" {
				if err := v.AttachTarget(ctx, id, "mcp:server"); err != nil {
					t.Fatal(err)
				}
				if state != "hidden-pending-target" {
					if err := v.CommitMetadata(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			v.Seal()
			before := deletionDocumentOnDisk(t, docs)
			if err := v.Delete(ctx, id); !errors.Is(err, vault.ErrVaultSealed) {
				t.Fatalf("ordinary Delete while sealed = %v", err)
			}
			after := deletionDocumentOnDisk(t, docs)
			beforeJSON, _ := json.Marshal(before)
			afterJSON, _ := json.Marshal(after)
			if !bytes.Equal(beforeJSON, afterJSON) {
				t.Fatal("ordinary sealed Delete changed durable metadata")
			}
			reads, writes := docs.blobReads, docs.blobWrites
			for range 2 {
				if err := v.ScheduleDelete(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if v.State() != vault.StateSealed || docs.blobReads != reads || docs.blobWrites != writes {
				t.Fatal("sealed scheduling accessed the encrypted provider or unsealed the vault")
			}
			assertDeletionIntent(t, docs, id, hidden)
			if hidden {
				assertDeletionHidden(t, v, id)
			}
			v.Close()
			v = openDeletionVault(t, docs)
			assertDeletionIntent(t, docs, id, hidden) // locked startup cannot finish
			if hidden {
				assertDeletionHidden(t, v, id)
			}
			unsealDeletionVault(t, v)
			assertDeletionComplete(t, v, docs, id)
			// Reopen once more: absence must be on disk, not just in memory.
			v.Close()
			v = openDeletionVault(t, docs)
			unsealDeletionVault(t, v)
			assertDeletionComplete(t, v, docs, id)
		})
	}
}

func TestScheduleDelete_FileProviderFailureRetainsDurableIntent(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, err := v.CreateSystemSecret(ctx, credential.NewSecret("oauth-session"))
	if err != nil {
		t.Fatal(err)
	}
	var originalBlob json.RawMessage
	if _, err := docs.Read("vault-blob.json", &originalBlob); err != nil {
		t.Fatal(err)
	}
	docs.failName = "vault-blob.json"
	for range 2 {
		if err := v.ScheduleDelete(ctx, id); err != nil {
			t.Fatalf("durably scheduled provider failure returned error: %v", err)
		}
		assertDeletionIntent(t, docs, id, true)
	}
	if docs.failedWrite != 2 {
		t.Fatalf("real file provider deletion attempts = %d, want 2", docs.failedWrite)
	}
	var retainedBlob json.RawMessage
	if _, err := docs.Read("vault-blob.json", &retainedBlob); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalBlob, retainedBlob) {
		t.Fatal("failed provider write changed persisted encrypted material")
	}
	assertDeletionHidden(t, v, id)
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v) // failure is retained, not turned into unlock failure
	assertDeletionIntent(t, docs, id, true)
	if docs.failedWrite != 3 {
		t.Fatalf("unseal did not retry failed file deletion: attempts=%d", docs.failedWrite)
	}
	docs.failName = ""
	v.Seal()
	unsealDeletionVault(t, v) // same-process recovery is also a retry trigger
	assertDeletionComplete(t, v, docs, id)
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	assertDeletionComplete(t, v, docs, id)
}

func TestScheduleDelete_FailedIntentSaveDoesNotPublishMemoryOnlyDeletion(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, createErr := v.CreateSystemSecret(ctx, credential.NewSecret("must-remain"))
	if createErr != nil {
		t.Fatal(createErr)
	}
	v.Seal()
	docs.failName = "vault.json"
	for range 2 {
		if err := v.ScheduleDelete(ctx, id); !errors.Is(err, errDeletionSave) {
			t.Fatalf("failed durable intent save = %v", err)
		}
	}
	if docs.failedWrite != 2 {
		t.Fatal("second scheduling trusted an in-memory-only intent")
	}
	doc := deletionDocumentOnDisk(t, docs)
	for _, entry := range doc.Journal {
		if entry.Op == "delete" {
			t.Fatal("failed intent persisted a delete entry")
		}
	}
	docs.failName = ""
	unsealDeletionVault(t, v)
	if _, err := v.Get(ctx, id); err != nil {
		t.Fatalf("failed intent removed provider material: %v", err)
	}
	records, recordsErr := v.SecretRecords(ctx)
	if recordsErr != nil || len(records) != 1 || records[0].ID != id || !records[0].SystemOnly {
		t.Fatalf("failed save changed live catalogue: %+v, %v", records, recordsErr)
	}
	assertDeletionHidden(t, v, id)
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	if _, err := v.Get(ctx, id); err != nil {
		t.Fatalf("failed intent lost material after restart: %v", err)
	}
	assertDeletionHidden(t, v, id)
}

func TestScheduleDelete_FailedCompletionSaveRetainsIntentForRetry(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, err := v.CreateSystemSecret(ctx, credential.NewSecret("oauth-session"))
	if err != nil {
		t.Fatal(err)
	}
	docs.failName, docs.failWrite = "vault.json", 2 // intent succeeds, completion fails
	if err := v.ScheduleDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if docs.failedWrite != 1 {
		t.Fatal("completion persistence failure was not exercised")
	}
	assertDeletionIntent(t, docs, id, true)
	assertDeletionHidden(t, v, id)
	// Unseal must still see the in-memory intent after the failed clear.
	docs.failName = ""
	v.Seal()
	unsealDeletionVault(t, v)
	assertDeletionComplete(t, v, docs, id)
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	assertDeletionComplete(t, v, docs, id)
}

// Hold the actual provider's successful deletion result until Seal advances
// the generation. The journal must not be cleared by the stale completion.
type pausedDeletionProvider struct {
	*file.Provider
	once    sync.Once
	deleted chan struct{}
	release chan struct{}
}

func (p *pausedDeletionProvider) Delete(ctx context.Context, id credential.SecretID) error {
	err := p.Provider.Delete(ctx, id)
	p.once.Do(func() {
		close(p.deleted)
		<-p.release
	})
	return err
}

func TestScheduleDelete_SealDuringProviderCompletionRetainsIntent(t *testing.T) {
	ctx := context.Background()
	docs := storage.NewDocumentStore(t.TempDir())
	provider := &pausedDeletionProvider{
		Provider: file.New(docs, "vault-blob.json"),
		deleted:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	reg, err := vault.NewRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(docs, reg, logtest.Slog(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	if _, setupErr := v.Setup(ctx, vault.SetupRequest{Passphrase: "deletion-test-pass"}); setupErr != nil {
		t.Fatal(setupErr)
	}
	id, err := v.CreateSystemSecret(ctx, credential.NewSecret("oauth-session"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- v.ScheduleDelete(ctx, id) }()
	<-provider.deleted
	v.Seal()
	close(provider.release)
	if err := <-result; err != nil {
		t.Fatalf("durable schedule failed on generation change: %v", err)
	}
	assertDeletionIntent(t, docs, id, true)
	assertDeletionHidden(t, v, id)
	unsealDeletionVault(t, v)
	assertDeletionComplete(t, v, docs, id)
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	assertDeletionComplete(t, v, docs, id)
}

func TestDelete_FileProviderFailureStillReturnsError(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, err := v.Create(ctx, credential.NewSecret("ordinary-password"))
	if err != nil {
		t.Fatal(err)
	}
	docs.failName = "vault-blob.json"
	if err := v.Delete(ctx, id); !errors.Is(err, errDeletionSave) {
		t.Fatalf("ordinary Delete hid provider failure: %v", err)
	}
	assertDeletionIntent(t, docs, id, false)
	docs.failName = ""
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	assertDeletionComplete(t, v, docs, id)
}

type deletionUnlockRequester func(context.Context, string) error

func (f deletionUnlockRequester) RequestUnlock(ctx context.Context, reason string) error {
	return f(ctx, reason)
}

func TestDelete_AfterUnlockUsesProviderWithRetiredPromptContext(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, err := v.Create(ctx, credential.NewSecret("probe-credential"))
	if err != nil {
		t.Fatal(err)
	}
	v.Seal()
	promptContext := make(chan error, 1)
	deleteResult := make(chan error, 1)
	v.SetUnlockRequester(deletionUnlockRequester(func(ctx context.Context, _ string) error {
		if err := v.Unseal(ctx, vault.UnsealRequest{Passphrase: "deletion-test-pass"}); err != nil {
			return err
		}
		// Unseal retires its own prompt. That cancellation must not newly
		// short-circuit ordinary Delete before the provider sees the call.
		promptContext <- ctx.Err()
		err := v.Delete(ctx, id)
		deleteResult <- err
		return err
	}))
	if err := v.EnsureUnsealed(ctx, "probe credential"); err != nil {
		t.Fatal(err)
	}
	if err := <-promptContext; !errors.Is(err, context.Canceled) {
		t.Fatalf("unlock did not retire the requester context: %v", err)
	}
	if err := <-deleteResult; err != nil {
		t.Fatalf("ordinary Delete failed after its unlock: %v", err)
	}
	assertDeletionComplete(t, v, docs, id)
}

func assertDeleteCandidate(t *testing.T, v *vault.Vault, docs storage.DocumentStore, id credential.SecretID) {
	t.Helper()
	ids, err := v.PendingDeleteCandidates(context.Background())
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatalf("pending candidates = %v, error %v", ids, err)
	}
	ids[0] = "" // enumeration must not expose mutable journal storage
	doc := deletionDocumentOnDisk(t, docs)
	count := 0
	for _, entry := range doc.Journal {
		if entry.NewID == id && entry.Op == "delete-candidate" {
			count++
			if entry.Phase != vault.PhasePrepared || !entry.SystemOnly {
				t.Fatalf("candidate lost durable classification: %+v", entry)
			}
		}
		if entry.NewID == id && entry.Op == "delete" {
			t.Fatal("conditional candidate was promoted without reference admission")
		}
	}
	if count != 1 {
		t.Fatalf("durable candidate count = %d, want 1", count)
	}
}

func TestPrepareDelete_FailureKeepsRecordAndMaterial(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, createErr := v.CreateSystemSecret(ctx, credential.NewSecret("referenced-session"))
	if createErr != nil {
		t.Fatal(createErr)
	}
	if err := v.AttachTarget(ctx, id, "mcp:server"); err != nil {
		t.Fatal(err)
	}
	if err := v.CommitMetadata(ctx, id); err != nil {
		t.Fatal(err)
	}
	v.Seal()
	docs.failName = "vault.json"
	for range 2 {
		if err := v.PrepareDelete(ctx, id); !errors.Is(err, errDeletionSave) {
			t.Fatalf("failed preparation returned %v", err)
		}
		ids, err := v.PendingDeleteCandidates(ctx)
		if err != nil || len(ids) != 0 {
			t.Fatalf("failed preparation published candidates: %v, %v", ids, err)
		}
	}
	docs.failName = ""
	unsealDeletionVault(t, v)
	records, recordsErr := v.SecretRecords(ctx)
	if recordsErr != nil || len(records) != 1 || records[0].ID != id || !records[0].SystemOnly {
		t.Fatalf("failed preparation changed live record: %+v, %v", records, recordsErr)
	}
	if _, err := v.Get(ctx, id); err != nil {
		t.Fatalf("failed preparation removed referenced material: %v", err)
	}
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	ids, candidatesErr := v.PendingDeleteCandidates(ctx)
	if candidatesErr != nil || len(ids) != 0 {
		t.Fatalf("failed preparation persisted candidates: %v, %v", ids, candidatesErr)
	}
	if _, err := v.Get(ctx, id); err != nil {
		t.Fatalf("failed preparation lost material after restart: %v", err)
	}
	assertDeletionHidden(t, v, id)
}

func TestPrepareDelete_RestartKeepsReferencedMaterialUntilPromotion(t *testing.T) {
	for _, finalized := range []bool{false, true} {
		name := "pending-create"
		if finalized {
			name = "finalized"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			v, docs := setupDeletionVault(t)
			id, createErr := v.CreateSystemSecret(ctx, credential.NewSecret("still-referenced"))
			if createErr != nil {
				t.Fatal(createErr)
			}
			if finalized {
				if err := v.AttachTarget(ctx, id, "mcp:server"); err != nil {
					t.Fatal(err)
				}
				if err := v.CommitMetadata(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			// Preparation must be metadata-only both unsealed and sealed.
			reads, writes := docs.blobReads, docs.blobWrites
			if err := v.PrepareDelete(ctx, id); err != nil {
				t.Fatal(err)
			}
			v.Seal()
			if err := v.PrepareDelete(ctx, id); err != nil {
				t.Fatal(err)
			}
			if reads != docs.blobReads || writes != docs.blobWrites {
				t.Fatal("preparation accessed the encrypted provider")
			}
			assertDeleteCandidate(t, v, docs, id)
			assertDeletionHidden(t, v, id)
			v.Close()
			v = openDeletionVault(t, docs)
			assertDeleteCandidate(t, v, docs, id)
			unsealDeletionVault(t, v)
			assertDeleteCandidate(t, v, docs, id)
			if _, err := v.Get(ctx, id); err != nil {
				t.Fatalf("startup/unseal executed a conditional candidate: %v", err)
			}
			records, recordsErr := v.SecretRecords(ctx)
			if recordsErr != nil || len(records) != 1 || records[0].ID != id || !records[0].SystemOnly {
				t.Fatalf("candidate dropped referenced record: %+v, %v", records, recordsErr)
			}
			assertDeletionHidden(t, v, id)

			// The metadata owner has now removed all references, but the
			// unconditional-intent write fails. The candidate must survive.
			v.Seal()
			docs.failName = "vault.json"
			if err := v.ScheduleDelete(ctx, id); !errors.Is(err, errDeletionSave) {
				t.Fatalf("promotion failure returned %v", err)
			}
			assertDeleteCandidate(t, v, docs, id)
			docs.failName = ""
			v.Close()
			v = openDeletionVault(t, docs)
			assertDeleteCandidate(t, v, docs, id)
			unsealDeletionVault(t, v)
			if _, err := v.Get(ctx, id); err != nil {
				t.Fatalf("failed promotion deleted candidate material: %v", err)
			}
			// The owner rechecks references and retries promotion.
			if err := v.ScheduleDelete(ctx, id); err != nil {
				t.Fatal(err)
			}
			assertDeletionComplete(t, v, docs, id)
			ids, candidatesErr := v.PendingDeleteCandidates(ctx)
			if candidatesErr != nil || len(ids) != 0 {
				t.Fatalf("successful deletion retained candidates: %v, %v", ids, candidatesErr)
			}
			v.Close()
			v = openDeletionVault(t, docs)
			unsealDeletionVault(t, v)
			assertDeletionComplete(t, v, docs, id)
		})
	}
}

func assertDeletionRowUnavailable(t *testing.T, v *vault.Vault, id credential.SecretID) {
	t.Helper()
	row := vault.RowFor(id)
	for _, inputs := range [][]vault.CredentialInventory{
		nil,
		{{ID: "surviving-reference", SecretID: string(id)}},
	} {
		if _, ok := v.ResolveRow(row, inputs); ok {
			t.Fatal("retiring ID is reusable through a catalogue or reference row")
		}
		entries, err := v.BuildInventory(context.Background(), inputs)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.ID == row {
				t.Fatal("retiring ID is visible for reuse")
			}
		}
		if err := v.ReplaceSecret(context.Background(), row, credential.NewSecret("reused"), inputs); err == nil {
			t.Fatal("retiring ID accepted a replacement through its row")
		}
	}
}

func TestScheduleDelete_CommittedWriteErrorReconcilesBeforeRowReuse(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, createErr := v.Create(ctx, credential.NewSecret("ordinary-owned-secret"))
	if createErr != nil {
		t.Fatal(createErr)
	}
	// Candidate persistence can also report an error after commit. It stays
	// conditional, with the referenced record and material still intact.
	docs.failName, docs.commitBeforeError = "vault.json", true
	if err := v.PrepareDelete(ctx, id); !errors.Is(err, errDeletionSave) {
		t.Fatalf("committed preparation failure = %v", err)
	}
	ids, candidatesErr := v.PendingDeleteCandidates(ctx)
	if candidatesErr != nil || len(ids) != 1 || ids[0] != id {
		t.Fatalf("committed candidate was not reconciled: %v, %v", ids, candidatesErr)
	}
	if _, ok := v.ResolveRow(vault.RowFor(id), nil); !ok {
		t.Fatal("a confirmed conditional candidate hid its still-referenced record")
	}
	if err := v.ScheduleDelete(ctx, id); !errors.Is(err, errDeletionSave) {
		t.Fatalf("committed promotion failure = %v", err)
	}
	assertDeletionIntent(t, docs, id, false)
	assertDeletionRowUnavailable(t, v, id)
	records, recordsErr := v.SecretRecords(ctx)
	if recordsErr != nil || len(records) != 0 {
		t.Fatalf("committed deletion retained the old live catalogue: %+v, %v", records, recordsErr)
	}
	if v.State() != vault.StateUnsealed {
		t.Fatal("outcome reconciliation changed the seal lifecycle")
	}
	if _, err := v.Get(ctx, id); err != nil {
		t.Fatalf("failed scheduling touched material or lost the root key: %v", err)
	}
	docs.failName = ""
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	assertDeletionComplete(t, v, docs, id)
}

func TestScheduleDelete_UnreadableOutcomeFencesReuseAndProviderExecution(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "not-committed"
		if committed {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			v, docs := setupDeletionVault(t)
			id, createErr := v.Create(ctx, credential.NewSecret("ordinary-owned-secret"))
			if createErr != nil {
				t.Fatal(createErr)
			}
			if err := v.PrepareDelete(ctx, id); err != nil {
				t.Fatal(err)
			}
			docs.failName, docs.commitBeforeError, docs.failRead = "vault.json", committed, true
			blobWrites := docs.blobWrites
			if err := v.ScheduleDelete(ctx, id); !errors.Is(err, errDeletionSave) {
				t.Fatalf("ambiguous promotion failure = %v", err)
			}
			assertDeletionRowUnavailable(t, v, id)
			if _, err := v.SecretRecords(ctx); err == nil {
				t.Fatal("known-material screening accepted an unresolved catalogue")
			}
			if err := v.ScheduleDelete(ctx, id); err == nil {
				t.Fatal("retry claimed an unreadable in-memory intent was durable")
			}
			if _, err := v.PendingDeleteCandidates(ctx); err == nil {
				t.Fatal("candidate enumeration hid its unresolved persistence outcome")
			}
			if docs.blobWrites != blobWrites {
				t.Fatal("unresolved intent reached provider deletion")
			}
			if v.State() != vault.StateUnsealed {
				t.Fatal("uncertainty fencing changed seal state")
			}
			if _, err := v.Get(ctx, id); err != nil {
				t.Fatalf("uncertainty fencing changed the root key or material: %v", err)
			}
			// Only an authoritative readback releases the uncertainty fence.
			docs.failRead, docs.failName = false, ""
			ids, candidatesErr := v.PendingDeleteCandidates(ctx)
			if candidatesErr != nil {
				t.Fatal(candidatesErr)
			}
			if committed {
				if len(ids) != 0 {
					t.Fatalf("committed promotion still lists candidates: %v", ids)
				}
				assertDeletionRowUnavailable(t, v, id)
			} else {
				if len(ids) != 1 || ids[0] != id {
					t.Fatalf("uncommitted promotion lost its candidate: %v", ids)
				}
				if _, ok := v.ResolveRow(vault.RowFor(id), nil); !ok {
					t.Fatal("confirmed uncommitted deletion did not restore the record")
				}
			}
			if _, err := v.SecretRecords(ctx); err != nil {
				t.Fatalf("screening remained fenced after outcome resolution: %v", err)
			}
			if err := v.ScheduleDelete(ctx, id); err != nil {
				t.Fatal(err)
			}
			assertDeletionComplete(t, v, docs, id)
			v.Close()
			v = openDeletionVault(t, docs)
			unsealDeletionVault(t, v)
			assertDeletionComplete(t, v, docs, id)
		})
	}
}

func TestScheduleDelete_CommittedCompletionErrorDoesNotRestoreJournal(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, err := v.Create(ctx, credential.NewSecret("ordinary-owned-secret"))
	if err != nil {
		t.Fatal(err)
	}
	docs.failName, docs.failWrite, docs.commitBeforeError = "vault.json", 2, true
	if err := v.ScheduleDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if docs.failedWrite != 1 {
		t.Fatal("completion commit-then-error was not exercised")
	}
	assertDeletionComplete(t, v, docs, id)
	docs.failName = ""
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	assertDeletionComplete(t, v, docs, id)
}

func TestPrepareDelete_UnreadableCommittedCandidateFencesOnlyUntilReadback(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	id, createErr := v.Create(ctx, credential.NewSecret("still-referenced"))
	if createErr != nil {
		t.Fatal(createErr)
	}
	docs.failName, docs.commitBeforeError, docs.failRead = "vault.json", true, true
	if err := v.PrepareDelete(ctx, id); !errors.Is(err, errDeletionSave) {
		t.Fatalf("ambiguous candidate preparation = %v", err)
	}
	// There is no unconditional deletion marker here. The uncertainty fence
	// itself must prevent selecting the still-present ordinary record.
	assertDeletionRowUnavailable(t, v, id)
	if _, err := v.SecretRecords(ctx); err == nil {
		t.Fatal("screening accepted the unresolved candidate write")
	}
	if err := v.PrepareDelete(ctx, id); err == nil {
		t.Fatal("idempotent preparation trusted an unresolved candidate")
	}
	if _, err := v.Get(ctx, id); err != nil {
		t.Fatalf("candidate uncertainty changed referenced material: %v", err)
	}
	docs.failName, docs.failRead = "", false
	ids, candidatesErr := v.PendingDeleteCandidates(ctx)
	if candidatesErr != nil || len(ids) != 1 || ids[0] != id {
		t.Fatalf("resolved conditional candidate = %v, %v", ids, candidatesErr)
	}
	if _, ok := v.ResolveRow(vault.RowFor(id), nil); !ok {
		t.Fatal("resolved conditional candidate kept its ordinary record fenced")
	}
	records, recordsErr := v.SecretRecords(ctx)
	if recordsErr != nil || len(records) != 1 || records[0].ID != id {
		t.Fatalf("resolved screening catalogue = %+v, %v", records, recordsErr)
	}
	v.Close()
	v = openDeletionVault(t, docs)
	unsealDeletionVault(t, v)
	if _, err := v.Get(ctx, id); err != nil {
		t.Fatalf("restart deleted a referenced conditional candidate: %v", err)
	}
}

func TestPurge_ClearsDeletionUncertaintyBeforeReinitialization(t *testing.T) {
	ctx := context.Background()
	v, docs := setupDeletionVault(t)
	oldID, createErr := v.Create(ctx, credential.NewSecret("old-owned-secret"))
	if createErr != nil {
		t.Fatal(createErr)
	}
	if err := v.PrepareDelete(ctx, oldID); err != nil {
		t.Fatal(err)
	}
	docs.failName, docs.commitBeforeError, docs.failRead = "vault.json", true, true
	if err := v.ScheduleDelete(ctx, oldID); !errors.Is(err, errDeletionSave) {
		t.Fatalf("ambiguous deletion = %v", err)
	}
	if _, err := v.SecretRecords(ctx); err == nil {
		t.Fatal("expected unresolved deletion to fence screening before reset")
	}
	failures, purgeErr := v.Purge(ctx)
	if purgeErr != nil || len(failures) != 0 {
		t.Fatalf("Purge: failures=%+v error=%v", failures, purgeErr)
	}
	if v.State() != vault.StateUninitialized {
		t.Fatal("Purge did not discard the old vault identity")
	}
	docs.failName, docs.failRead = "", false
	if _, err := v.Setup(ctx, vault.SetupRequest{Passphrase: "replacement-pass"}); err != nil {
		t.Fatal(err)
	}
	newID, replacementErr := v.Create(ctx, credential.NewSecret("replacement-screening-material"))
	if replacementErr != nil {
		t.Fatal(replacementErr)
	}
	// No candidate reconciliation or restart intervenes: reset itself owns
	// retiring the old identity's uncertainty fence.
	records, recordsErr := v.SecretRecords(ctx)
	if recordsErr != nil || len(records) != 1 || records[0].ID != newID {
		t.Fatalf("new identity's screening catalogue = %+v, %v", records, recordsErr)
	}
	if id, ok := v.ResolveRow(vault.RowFor(newID), nil); !ok || id != newID {
		t.Fatal("new identity's ordinary secret is not selectable")
	}
	secret, getErr := v.Get(ctx, newID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if err := secret.Use(func(value []byte) error {
		if string(value) != "replacement-screening-material" {
			t.Error("new identity returned incorrect screening material")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, oldID); !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("old identity's material survived reset: %v", err)
	}
}
