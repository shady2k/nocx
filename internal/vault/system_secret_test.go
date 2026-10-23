package vault

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
)

// Captures each attempted durable transition, including a failed final save.
// The underlying store remains the crash/restart authority.
type systemSecretDocStore struct {
	storage.DocumentStore
	writes []Document
	failAt int
}

func (s *systemSecretDocStore) Write(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	s.writes = append(s.writes, doc)
	if s.failAt == len(s.writes) {
		return errors.New("injected document failure")
	}
	return s.DocumentStore.Write(name, value)
}

func systemSecretRestart(t *testing.T, v *Vault, docs storage.DocumentStore) *Vault {
	t.Helper()
	v.Close()
	restarted, err := New(docs, v.reg, v.logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	if err := restarted.Unseal(context.Background(), UnsealRequest{Passphrase: "test-pass"}); err != nil {
		t.Fatal(err)
	}
	return restarted
}

func assertSystemSecretHidden(t *testing.T, v *Vault, id credential.SecretID) {
	t.Helper()
	ctx := context.Background()
	// Each legacy fallback slot must respect classification, not just the
	// normal (unreferenced) inventory path.
	for _, inputs := range [][]CredentialInventory{
		nil,
		{{ID: "password", SecretID: string(id)}},
		{{ID: "passphrase", PassphraseSecretID: string(id)}},
		{{ID: "key", KeyMaterialSecretID: string(id)}},
	} {
		entries, err := v.BuildInventory(ctx, inputs)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.ID == rowID(id) {
				t.Fatal("system-only secret leaked through inventory")
			}
		}
		if _, ok := v.ResolveRow(rowID(id), inputs); ok {
			t.Fatal("forged system-only row resolved for selection/deletion")
		}
		if err := v.RenameSecret(ctx, rowID(id), "forged rename", inputs); err == nil {
			t.Fatal("forged system-only rename succeeded")
		}
		if err := v.ReplaceSecret(ctx, rowID(id), credential.NewSecret("forged replacement"), inputs); err == nil {
			t.Fatal("forged system-only replacement succeeded")
		}
	}
}

func assertSystemSecretValue(t *testing.T, v *Vault, id credential.SecretID, want string) {
	t.Helper()
	value, err := v.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := value.Use(func(b []byte) error {
		if string(b) != want {
			t.Error("backend material differs from expected value")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertSystemSecretTransitions(t *testing.T, writes []Document, id credential.SecretID) {
	t.Helper()
	for i, doc := range writes {
		for _, entry := range doc.Journal {
			if entry.NewID == id && !entry.SystemOnly {
				t.Fatalf("write %d lost journal classification", i)
			}
		}
		if rec, ok := recordFor(doc.Secrets, id); ok && !rec.SystemOnly {
			t.Fatalf("write %d lost record classification", i)
		}
	}
}

func TestSystemSecretLifecycleAndUserIsolation(t *testing.T) {
	loweredCost(t)
	ctx := context.Background()
	v, docs, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "test-pass")
	capture := &systemSecretDocStore{DocumentStore: docs}
	v.store = capture
	id, err := v.CreateSystemSecret(ctx, credential.NewSecret("oauth-session-one"))
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.writes) != 2 || capture.writes[0].Journal[0].Phase != PhasePrepared || capture.writes[1].Journal[0].Phase != PhaseSecretWritten {
		t.Fatal("create did not use prepared/provider/record sequence")
	}
	assertSystemSecretHidden(t, v, id)
	if attachErr := v.AttachTarget(ctx, id, "mcp:server"); attachErr != nil {
		t.Fatal(attachErr)
	}
	if commitErr := v.CommitMetadata(ctx, id); commitErr != nil {
		t.Fatal(commitErr)
	}
	if replaceErr := v.ReplaceSystemSecret(ctx, id, credential.NewSecret("oauth-session-two")); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	assertSystemSecretTransitions(t, capture.writes, id)
	assertSystemSecretValue(t, v, id, "oauth-session-two")
	v = systemSecretRestart(t, v, docs)
	assertSystemSecretHidden(t, v, id)
	assertSystemSecretValue(t, v, id, "oauth-session-two")

	// Backend screening sees material omitted from every user inventory.
	records, err := v.SecretRecords(ctx)
	if err != nil || len(records) != 1 || records[0].ID != id || !records[0].SystemOnly {
		t.Fatalf("backend catalogue = %+v, error %v", records, err)
	}
	records[0].SystemOnly = false
	assertSystemSecretHidden(t, v, id) // snapshot cannot mutate classification
	if err := v.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, id); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("owned delete left material: %v", err)
	}
}

func TestSystemSecretReplacementRejectsOrdinaryAndUnknownIDs(t *testing.T) {
	loweredCost(t)
	ctx := context.Background()
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "test-pass")
	id, err := v.Create(ctx, credential.NewSecret("ordinary"))
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := mintID(ProviderSystem)
	if err != nil {
		t.Fatal(err)
	}
	for _, rejected := range []credential.SecretID{id, unknown, ""} {
		if err := v.ReplaceSystemSecret(ctx, rejected, credential.NewSecret("replacement")); !errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("replacement accepted non-system ID: %v", err)
		}
	}
	assertSystemSecretValue(t, v, id, "ordinary")
	if resolved, ok := v.ResolveRow(rowID(id), nil); !ok || resolved != id {
		t.Fatal("ordinary user secret stopped resolving")
	}
}

func TestSystemSecretCreateFailuresRecoverWithoutExposingMaterial(t *testing.T) {
	loweredCost(t)
	for _, failure := range []string{"prepared-save", "provider", "record-save", "none"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			provider := newTestProvider(ProviderSystem)
			v, docs, _ := testVault(t, provider)
			mustSetup(t, v, "test-pass")
			capture := &systemSecretDocStore{DocumentStore: docs}
			v.store = capture
			switch failure {
			case "prepared-save":
				capture.failAt = 1
			case "provider":
				provider.fail = errors.New("injected provider failure")
			case "record-save":
				capture.failAt = 2
			}
			_, err := v.CreateSystemSecret(ctx, credential.NewSecret("session"))
			if (err == nil) != (failure == "none") {
				t.Fatalf("create error = %v for %s", err, failure)
			}
			id := capture.writes[0].Journal[0].NewID
			assertSystemSecretTransitions(t, capture.writes, id)
			assertSystemSecretHidden(t, v, id)
			provider.fail = nil
			v = systemSecretRestart(t, v, docs)
			candidates, candidatesErr := v.PendingDeleteCandidates(ctx)
			if candidatesErr != nil {
				t.Fatal(candidatesErr)
			}
			if failure == "prepared-save" {
				if len(candidates) != 0 {
					t.Fatalf("unwritten create became a recovery candidate: %v", candidates)
				}
			} else if len(candidates) != 1 || candidates[0] != id {
				t.Fatalf("recovered system create lost cleanup ownership: %v", candidates)
			}
			if failure != "none" && len(candidates) != 0 {
				// No reference was committed by these failed creates. Their
				// metadata owner, not startup provider availability, decides
				// that the conditional candidate may be promoted.
				if scheduleErr := v.ScheduleDelete(ctx, id); scheduleErr != nil {
					t.Fatal(scheduleErr)
				}
			}
			if failure == "none" {
				assertSystemSecretHidden(t, v, id)
				assertSystemSecretValue(t, v, id, "session")
			} else if _, err := provider.Get(ctx, id); !errors.Is(err, ErrSecretNotFound) {
				t.Fatalf("recovery left failed-create orphan: %v", err)
			}
		})
	}
}

func TestSystemSecretReplaceFailuresPreserveIdentityAndClassification(t *testing.T) {
	loweredCost(t)
	for _, failure := range []string{"prepared-save", "provider", "final-save"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			provider := newTestProvider(ProviderSystem)
			v, docs, _ := testVault(t, provider)
			mustSetup(t, v, "test-pass")
			id, err := v.CreateSystemSecret(ctx, credential.NewSecret("old"))
			if err != nil {
				t.Fatal(err)
			}
			capture := &systemSecretDocStore{DocumentStore: docs}
			v.store = capture
			switch failure {
			case "prepared-save":
				capture.failAt = 1
			case "provider":
				provider.fail = errors.New("injected provider failure")
			case "final-save":
				capture.failAt = 2
			}
			if err := v.ReplaceSystemSecret(ctx, id, credential.NewSecret("new")); err == nil {
				t.Fatal("replacement ignored injected failure")
			}
			assertSystemSecretTransitions(t, capture.writes, id)
			assertSystemSecretHidden(t, v, id)
			provider.fail = nil
			v = systemSecretRestart(t, v, docs)
			assertSystemSecretHidden(t, v, id)
			want := "old"
			if failure == "final-save" {
				want = "new" // provider put landed; the ID and authority did not change
			}
			assertSystemSecretValue(t, v, id, want)
		})
	}
}

func TestSystemSecretBlockedRecoveryKeepsJournalClassification(t *testing.T) {
	loweredCost(t)
	ctx := context.Background()
	provider := newTestProvider(ProviderSystem)
	v, docs, _ := testVault(t, provider)
	mustSetup(t, v, "test-pass")
	capture := &systemSecretDocStore{DocumentStore: docs, failAt: 2}
	v.store = capture
	if _, err := v.CreateSystemSecret(ctx, credential.NewSecret("orphan")); err == nil {
		t.Fatal("create ignored failed record save")
	}
	id := capture.writes[0].Journal[0].NewID
	provider.fail = errors.New("provider offline during recovery")
	v = systemSecretRestart(t, v, docs)
	assertSystemSecretHidden(t, v, id)
	doc, _, err := loadDocument(docs)
	if err != nil || len(doc.Journal) != 1 || !doc.Journal[0].SystemOnly || len(doc.Secrets) != 0 {
		t.Fatalf("blocked orphan lost classification: %+v, %v", doc, err)
	}
	provider.fail = nil
	v = systemSecretRestart(t, v, docs)
	candidates, candidatesErr := v.PendingDeleteCandidates(ctx)
	if candidatesErr != nil || len(candidates) != 1 || candidates[0] != id {
		t.Fatalf("unblocked provider lost conditional recovery: %v, %v", candidates, candidatesErr)
	}
	if scheduleErr := v.ScheduleDelete(ctx, id); scheduleErr != nil {
		t.Fatal(scheduleErr)
	}
	if _, err := provider.Get(ctx, id); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("unblocked recovery left orphan: %v", err)
	}
}

func TestSystemSecretDeleteFailureKeepsJournalClassification(t *testing.T) {
	loweredCost(t)
	ctx := context.Background()
	provider := newTestProvider(ProviderSystem)
	v, docs, _ := testVault(t, provider)
	mustSetup(t, v, "test-pass")
	id, err := v.CreateSystemSecret(ctx, credential.NewSecret("session"))
	if err != nil {
		t.Fatal(err)
	}
	v = systemSecretRestart(t, v, docs) // retain create as a conditional candidate
	provider.fail = errors.New("provider offline")
	if err := v.Delete(ctx, id); err == nil {
		t.Fatal("owned deletion ignored provider failure")
	}
	assertSystemSecretHidden(t, v, id)
	v = systemSecretRestart(t, v, docs)
	assertSystemSecretHidden(t, v, id)
	provider.fail = nil
	systemSecretRestart(t, v, docs)
	if _, err := provider.Get(ctx, id); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("delete recovery left material: %v", err)
	}
}

// Purge uses the provider's bulk boundary, not filtered user inventory.
type systemSecretPurgeProvider struct{ *testProvider }

func (p *systemSecretPurgeProvider) PurgeAll(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.data)
	return nil
}

func TestSystemSecretPurgeAndBackendCatalogueState(t *testing.T) {
	loweredCost(t)
	ctx := context.Background()
	provider := &systemSecretPurgeProvider{newTestProvider(ProviderSystem)}
	v, docs, _ := testVault(t, provider)
	if _, err := v.SecretRecords(ctx); !errors.Is(err, ErrVaultUninitialized) {
		t.Fatalf("uninitialized catalogue error = %v", err)
	}
	mustSetup(t, v, "test-pass")
	id, err := v.CreateSystemSecret(ctx, credential.NewSecret("session"))
	if err != nil {
		t.Fatal(err)
	}
	v.Seal()
	if _, err := v.SecretRecords(ctx); !errors.Is(err, ErrVaultSealed) {
		t.Fatalf("sealed catalogue error = %v", err)
	}
	if failures, err := v.Purge(ctx); err != nil || len(failures) != 0 {
		t.Fatalf("purge = %v, %v", failures, err)
	}
	if _, err := provider.Get(ctx, id); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("purge left system material: %v", err)
	}
	if _, found, err := loadDocument(docs); err != nil || found {
		t.Fatalf("purge left document: found=%v, error=%v", found, err)
	}
}

func TestSystemSecretDocumentRejectsPreClassificationReader(t *testing.T) {
	store := &fakeDocStore{}
	id := credential.SecretID("sec:v1:system:0123456789abcdef0123456789abcdef")
	if err := saveDocument(store, Document{
		Secrets: []SecretRecord{{ID: id, Kind: KindPassword, SystemOnly: true}},
		Journal: []JournalEntry{{Op: "create", NewID: id, Phase: PhasePrepared, SystemOnly: true}},
	}); err != nil {
		t.Fatal(err)
	}
	current := vaultModule
	defer func() { vaultModule = current }()
	// Exercise the actual loader with the last schema that did not know this
	// security field, rather than just pinning the new module constant.
	vaultModule = storage.Module{Name: "vault", Current: 2}
	if _, _, err := loadDocument(store); !errors.Is(err, storage.ErrVersionTooNew) {
		t.Fatalf("old reader accepted system classification: %v", err)
	}
	vaultModule = current
	doc, found, err := loadDocument(store)
	if err != nil || !found || !doc.Secrets[0].SystemOnly || !doc.Journal[0].SystemOnly {
		t.Fatalf("current reader lost classification: %+v, %v", doc, err)
	}
	if writeErr := store.Write(vaultDocName, Document{Version: 2, Secrets: []SecretRecord{{ID: id, Kind: KindPassword}}}); writeErr != nil {
		t.Fatal(writeErr)
	}
	doc, found, err = loadDocument(store)
	if err != nil || !found || doc.Secrets[0].SystemOnly {
		t.Fatalf("legacy records must remain user-owned: %+v, %v", doc, err)
	}
}
