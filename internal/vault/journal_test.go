package vault_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/vaulttest"
)

// journal entry IDs — each entry in the test uses distinct references so the
// provider state changes from one entry do not affect another.
var (
	orphanPreparedID  = credential.SecretID("sec:v1:file:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	orphanWrittenID   = credential.SecretID("sec:v1:file:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	rotateNewID       = credential.SecretID("sec:v1:file:cccccccccccccccccccccccccccccccc")
	rotateOldID       = credential.SecretID("sec:v1:file:dddddddddddddddddddddddddddddddd")
	unknownProviderID = credential.SecretID("sec:v1:hcp:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
)

// TestJournalEntry_NoPlaintext asserts that marshaling a JournalEntry produces
// only identifiers and routing — never secret bytes (ADR-0011 §4). This is the
// regression test that catches a credential.Secret field added to the struct,
// since the type system helps (Secret refuses to marshal) but the test is what
// keeps it true over time.
func TestJournalEntry_NoPlaintext(t *testing.T) {
	entry := vault.JournalEntry{
		Op:     "create",
		OldID:  credential.SecretID("sec:v1:file:99999999999999999999999999999999"),
		NewID:  credential.SecretID("sec:v1:file:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Target: "profile:myserver",
		Phase:  vault.PhasePrepared,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("json.Marshal(JournalEntry): %v", err)
	}

	text := string(data)
	t.Logf("marshalled %d bytes: %s", len(data), text)

	// Verify the output has exactly the fields we expect and nothing extra.
	// The fields are: op, oldId, newId, target, phase. All string values.
	// No "secret" key should appear.
	if strings.Contains(text, "secret") {
		t.Errorf("marshalled output contains 'secret': %s", text)
	}
	if strings.Contains(text, "plaintext") {
		t.Errorf("marshalled output contains 'plaintext': %s", text)
	}
	if strings.Contains(text, "value") {
		t.Errorf("marshalled output contains 'value': %s", text)
	}

	// Round-trip through Unmarshal to confirm the JSON shape is parseable.
	var decoded vault.JournalEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded.Op != entry.Op {
		t.Errorf("Op = %q, want %q", decoded.Op, entry.Op)
	}
	if decoded.NewID != entry.NewID {
		t.Errorf("NewID = %q, want %q", decoded.NewID, entry.NewID)
	}
	if decoded.Phase != entry.Phase {
		t.Errorf("Phase = %q, want %q", decoded.Phase, entry.Phase)
	}
}

// TestReconcile_PhasePreparedEmptyTarget asserts that an entry in PhasePrepared
// with no Target is rolled back: the orphan secret deleted, entry cleared.
func TestReconcile_PhasePreparedEmptyTarget(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	if err := fake.Put(ctx, orphanPreparedID, credential.NewSecret("orphan")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "create", NewID: orphanPreparedID, Phase: vault.PhasePrepared},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op != "" {
		t.Error("journal entry should be cleared")
	}
	if len(blocked) != 0 {
		t.Fatalf("expected 0 blocked, got %d", len(blocked))
	}
	if _, err := fake.Get(ctx, orphanPreparedID); err == nil {
		t.Error("orphan secret should have been deleted")
	}
}

// TestReconcile_PhaseSecretWrittenEmptyTarget asserts the same rollback for
// PhaseSecretWritten with no Target.
func TestReconcile_PhaseSecretWrittenEmptyTarget(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	if err := fake.Put(ctx, orphanWrittenID, credential.NewSecret("orphan-written")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "create", NewID: orphanWrittenID, Phase: vault.PhaseSecretWritten},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op != "" {
		t.Error("journal entry should be cleared")
	}
	if len(blocked) != 0 {
		t.Fatalf("expected 0 blocked, got %d", len(blocked))
	}
	if _, err := fake.Get(ctx, orphanWrittenID); err == nil {
		t.Error("orphan secret should have been deleted")
	}
}

// TestReconcile_PhaseMetadataRepointed asserts that a completed metadata
// repoint verifies the new secret and deletes the old.
func TestReconcile_PhaseMetadataRepointed(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	if err := fake.Put(ctx, rotateNewID, credential.NewSecret("new-value")); err != nil {
		t.Fatalf("Put new: %v", err)
	}
	if err := fake.Put(ctx, rotateOldID, credential.NewSecret("old-value")); err != nil {
		t.Fatalf("Put old: %v", err)
	}

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "rotate", OldID: rotateOldID, NewID: rotateNewID, Target: "profile:myserver", Phase: vault.PhaseMetadataRepointed},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op != "" {
		t.Error("journal entry should be cleared")
	}
	if len(blocked) != 0 {
		t.Fatalf("expected 0 blocked, got %d", len(blocked))
	}
	if _, err := fake.Get(ctx, rotateNewID); err != nil {
		t.Error("new secret should still exist after reconcile", err)
	}
	if _, err := fake.Get(ctx, rotateOldID); err == nil {
		t.Error("old secret should have been deleted")
	}
}

// TestReconcile_CreateOnlyNoOldID asserts that a PhaseMetadataRepointed entry
// with an empty OldID (a create with no predecessor) clears correctly.
func TestReconcile_CreateOnlyNoOldID(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	createID := credential.SecretID("sec:v1:file:ffffffffffffffffffffffffffffffff")
	if err := fake.Put(ctx, createID, credential.NewSecret("new-only")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "create", NewID: createID, Target: "profile:newsvr", Phase: vault.PhaseMetadataRepointed},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op != "" {
		t.Error("journal entry should be cleared")
	}
	if len(blocked) != 0 {
		t.Fatalf("expected 0 blocked, got %d", len(blocked))
	}
	if _, err := fake.Get(ctx, createID); err != nil {
		t.Error("created secret should still exist")
	}
}

// TestReconcile_UnknownProvider asserts an entry with a provider not in the
// registry is retained and returned as blocked — never cleared.
func TestReconcile_UnknownProvider(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "create", NewID: unknownProviderID, Phase: vault.PhasePrepared},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op == "" {
		t.Error("entry for unknown provider should be retained, not cleared")
	}
	if len(blocked) != 1 {
		t.Fatalf("expected 1 blocked entry, got %d", len(blocked))
	}
	if blocked[0].NewID != unknownProviderID {
		t.Errorf("blocked entry NewID = %q, want %q", blocked[0].NewID, unknownProviderID)
	}
}

// TestReconcile_UnknownProvider_PhaseMetadataRepointed asserts the same
// retention rule applies to a PhaseMetadataRepointed entry with an unknown
// provider — verify-new-secret is not attempted.
func TestReconcile_UnknownProvider_PhaseMetadataRepointed(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "rotate", OldID: rotateOldID, NewID: unknownProviderID, Target: "profile:svr", Phase: vault.PhaseMetadataRepointed},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op == "" {
		t.Error("entry for unknown provider should be retained")
	}
	if len(blocked) != 1 {
		t.Fatalf("expected 1 blocked, got %d", len(blocked))
	}
}

// TestReconcile_AlreadyCleared asserts that empty (cleared) entries are
// skipped without error.
func TestReconcile_AlreadyCleared(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{}, // already cleared
			{}, // already cleared
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)
	if len(blocked) != 0 {
		t.Fatalf("expected 0 blocked, got %d", len(blocked))
	}
}

// TestReconcile_CrossProviderOldID asserts that when OldID belongs to a
// different writable provider than NewID, the old secret is deleted through
// its own provider (cross-provider rotation).
func TestReconcile_CrossProviderOldID(t *testing.T) {
	ctx := context.Background()
	fake1 := vaulttest.NewFake() // ProviderID "file"
	fake2 := vaulttest.NewFakeWithID(vault.ProviderID("other"))
	reg := mustRegister(t, fake1, fake2)

	// OldID routes through fake1 (file), NewID through fake2 (other).
	crossOldID := credential.SecretID("sec:v1:file:22222222222222222222222222222222")
	crossNewID := credential.SecretID("sec:v1:other:11111111111111111111111111111111")

	if err := fake1.Put(ctx, crossOldID, credential.NewSecret("cross-old")); err != nil {
		t.Fatalf("Put old: %v", err)
	}
	if err := fake2.Put(ctx, crossNewID, credential.NewSecret("cross-new")); err != nil {
		t.Fatalf("Put new: %v", err)
	}

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "rotate", OldID: crossOldID, NewID: crossNewID, Target: "profile:x", Phase: vault.PhaseMetadataRepointed},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op != "" {
		t.Error("entry should be cleared")
	}
	if len(blocked) != 0 {
		t.Fatalf("expected 0 blocked, got %d", len(blocked))
	}
	// Old deleted from fake1.
	if _, err := fake1.Get(ctx, crossOldID); err == nil {
		t.Error("old secret should have been deleted from its provider")
	}
	// New still exists in fake2.
	if _, err := fake2.Get(ctx, crossNewID); err != nil {
		t.Error("new secret should still exist in its provider")
	}
}

// TestReconcile_MixedEntries tests that Reconcile handles multiple entries in
// different states correctly and independently.
func TestReconcile_MixedEntries(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	orphan1 := credential.SecretID("sec:v1:file:11111111111111111111111111111111")
	orphan2 := credential.SecretID("sec:v1:file:22222222222222222222222222222222")
	rotateN := credential.SecretID("sec:v1:file:33333333333333333333333333333333")
	rotateO := credential.SecretID("sec:v1:file:44444444444444444444444444444444")

	for _, id := range []credential.SecretID{orphan1, orphan2, rotateN, rotateO} {
		if err := fake.Put(ctx, id, credential.NewSecret("val")); err != nil {
			t.Fatalf("Put %q: %v", id, err)
		}
	}

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "create", NewID: orphan1, Phase: vault.PhasePrepared},
			{Op: "create", NewID: orphan2, Phase: vault.PhaseSecretWritten},
			{Op: "rotate", OldID: rotateO, NewID: rotateN, Target: "profile:svr", Phase: vault.PhaseMetadataRepointed},
			{Op: "create", NewID: unknownProviderID, Phase: vault.PhasePrepared},
			{},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	for i := 0; i < 3; i++ {
		if doc.Journal[i].Op != "" {
			t.Errorf("entry %d should be cleared", i)
		}
	}
	if doc.Journal[3].Op == "" {
		t.Error("entry 3 (unknown provider) should be retained")
	}
	if doc.Journal[4].Op != "" {
		t.Error("entry 4 should remain cleared")
	}
	if len(blocked) != 1 {
		t.Fatalf("expected 1 blocked, got %d", len(blocked))
	}
	if blocked[0].NewID != unknownProviderID {
		t.Errorf("blocked NewID = %q, want %q", blocked[0].NewID, unknownProviderID)
	}
	if _, err := fake.Get(ctx, rotateO); err == nil {
		t.Error("old secret should have been deleted")
	}
	if _, err := fake.Get(ctx, rotateN); err != nil {
		t.Error("new secret should still exist")
	}
	if _, err := fake.Get(ctx, orphan1); err == nil {
		t.Error("orphan prepared secret should have been deleted")
	}
	if _, err := fake.Get(ctx, orphan2); err == nil {
		t.Error("orphan written secret should have been deleted")
	}
}

// TestReconcile_Idempotent asserts that running Reconcile twice is a no-op
// the second time. This is the key crash-recovery property (brief §1).
func TestReconcile_Idempotent(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	if err := fake.Put(ctx, orphanPreparedID, credential.NewSecret("orphan")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := fake.Put(ctx, rotateNewID, credential.NewSecret("new")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := fake.Put(ctx, rotateOldID, credential.NewSecret("old")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "create", NewID: orphanPreparedID, Phase: vault.PhasePrepared},
			{Op: "rotate", OldID: rotateOldID, NewID: rotateNewID, Target: "profile:svr", Phase: vault.PhaseMetadataRepointed},
			{Op: "create", NewID: unknownProviderID, Phase: vault.PhasePrepared},
		},
	}

	blocked1 := vault.Reconcile(ctx, doc, reg)
	docAfterFirst := *doc
	blocked1Copy := make([]vault.JournalEntry, len(blocked1))
	copy(blocked1Copy, blocked1)

	blocked2 := vault.Reconcile(ctx, doc, reg)

	for i := range doc.Journal {
		if doc.Journal[i] != docAfterFirst.Journal[i] {
			t.Errorf("entry %d changed on second reconcile: was %#v, now %#v", i, docAfterFirst.Journal[i], doc.Journal[i])
		}
	}
	if len(blocked2) != len(blocked1Copy) {
		t.Fatalf("second reconcile returned %d blocked entries, first returned %d", len(blocked2), len(blocked1Copy))
	}
	for i := range blocked2 {
		if blocked2[i] != blocked1Copy[i] {
			t.Errorf("blocked entry %d differs: was %#v, now %#v", i, blocked1Copy[i], blocked2[i])
		}
	}
	if _, err := fake.Get(ctx, orphanPreparedID); err == nil {
		t.Error("orphan should still be gone after second reconcile")
	}
	if _, err := fake.Get(ctx, rotateNewID); err != nil {
		t.Error("new secret should still exist after second reconcile")
	}
	if _, err := fake.Get(ctx, rotateOldID); err == nil {
		t.Error("old secret should still be gone after second reconcile")
	}
}

// TestReconcile_NonEmptyTargetPrepared asserts that an entry in PhasePrepared
// with a non-empty Target is retained for investigation and reported as
// blocked (defect 10: previously it was silently retained).
func TestReconcile_NonEmptyTargetPrepared(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	surpriseID := credential.SecretID("sec:v1:file:55555555555555555555555555555555")
	if err := fake.Put(ctx, surpriseID, credential.NewSecret("unexpected")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	doc := &vault.Document{
		Journal: []vault.JournalEntry{
			{Op: "create", NewID: surpriseID, Target: "profile:should-not-happen", Phase: vault.PhasePrepared},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op == "" {
		t.Error("entry with non-empty target but PhasePrepared should be retained")
	}
	if _, err := fake.Get(ctx, surpriseID); err != nil {
		t.Error("secret should not have been deleted when target is set but phase is Prepared")
	}
	if len(blocked) != 1 {
		t.Fatalf("expected 1 blocked (retained for investigation), got %d", len(blocked))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustRegister(t *testing.T, providers ...*vaulttest.Fake) *vault.Registry {
	t.Helper()
	ifaces := make([]vault.Provider, len(providers))
	for i, p := range providers {
		ifaces[i] = p
	}
	reg, err := vault.NewRegistry(ifaces...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg
}

// The record decides what an orphaned journal entry means (ADR-0016).
// An entry with no record is a genuine orphan and is deleted; an entry whose
// record landed is a completed create whose metadata attach never happened —
// the record proves the secret exists, so it is kept, not deleted. Without
// this, every secret created by a caller that never attaches metadata (the
// production save paths) would be deleted at the next startup.
func TestReconcile_PhaseSecretWrittenWithRecordKeepsSecret(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	recID := credential.SecretID("sec:v1:file:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if err := fake.Put(ctx, recID, credential.NewSecret("kept")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	doc := &vault.Document{
		Secrets: []vault.SecretRecord{
			{ID: recID, Name: "prod password", Kind: "password"},
		},
		Journal: []vault.JournalEntry{
			{Op: "create", NewID: recID, Phase: vault.PhaseSecretWritten},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)

	if doc.Journal[0].Op != "" {
		t.Error("journal entry should be cleared")
	}
	if len(blocked) != 0 {
		t.Fatalf("expected 0 blocked, got %d", len(blocked))
	}
	if _, err := fake.Get(ctx, recID); err != nil {
		t.Error("secret with a landed record should NOT be deleted", err)
	}
	if len(doc.Secrets) != 1 {
		t.Error("the record should survive reconciliation")
	}
}

// A completed metadata repoint deletes the old secret AND its record: a
// record naming a value that no longer exists is a dangling row.
func TestReconcile_MetadataRepointedDropsOldRecord(t *testing.T) {
	ctx := context.Background()
	fake := vaulttest.NewFake()
	reg := mustRegister(t, fake)

	newID := credential.SecretID("sec:v1:file:11111111111111111111111111111111")
	oldID := credential.SecretID("sec:v1:file:22222222222222222222222222222222")
	if err := fake.Put(ctx, newID, credential.NewSecret("new")); err != nil {
		t.Fatalf("Put new: %v", err)
	}
	if err := fake.Put(ctx, oldID, credential.NewSecret("old")); err != nil {
		t.Fatalf("Put old: %v", err)
	}

	doc := &vault.Document{
		Secrets: []vault.SecretRecord{
			{ID: newID, Name: "rotated", Kind: "password"},
			{ID: oldID, Name: "old name", Kind: "password"},
		},
		Journal: []vault.JournalEntry{
			{Op: "rotate", OldID: oldID, NewID: newID, Target: "profile:x", Phase: vault.PhaseMetadataRepointed},
		},
	}

	blocked := vault.Reconcile(ctx, doc, reg)
	if len(blocked) != 0 {
		t.Fatalf("expected 0 blocked, got %d", len(blocked))
	}
	if _, err := fake.Get(ctx, oldID); err == nil {
		t.Error("old secret should have been deleted")
	}
	if len(doc.Secrets) != 1 || doc.Secrets[0].ID != newID {
		t.Errorf("old record should be dropped, got %v", doc.Secrets)
	}
}
