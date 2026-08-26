package capability_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// fakeEndpointSecrets is a recording EndpointSecrets double with per-call
// failure injection: every failure path the service documents (mint fails,
// rotate fails, material delete fails) is reachable by setting one field.
type fakeEndpointSecrets struct {
	mu        sync.Mutex
	minted    []credential.SecretID
	mintNames []string
	rotated   []string
	deleted   []credential.SecretID

	mintErr   error
	rotateErr error
	deleteErr error

	// rows resolves renderer row handles to stored references — the fake's
	// RowResolver half, so a test can hand the service a "vault" that holds
	// known rows without standing up a real vault.
	rows map[string]string
}

func (f *fakeEndpointSecrets) ResolveRow(row string, _ []vault.CredentialInventory) (credential.SecretID, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref, ok := f.rows[row]
	if !ok {
		return "", false
	}
	return credential.SecretID(ref), true
}

func (f *fakeEndpointSecrets) CreateNamed(_ context.Context, _ credential.Secret, meta vault.SecretMeta) (credential.SecretID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mintErr != nil {
		return "", f.mintErr
	}
	id := credential.SecretID(fmt.Sprintf("sec:v1:file:%032x", len(f.minted)+1))
	f.minted = append(f.minted, id)
	f.mintNames = append(f.mintNames, meta.Name)
	return id, nil
}

func (f *fakeEndpointSecrets) ReplaceSecret(_ context.Context, row string, _ credential.Secret, _ []vault.CredentialInventory) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rotateErr != nil {
		return f.rotateErr
	}
	f.rotated = append(f.rotated, row)
	return nil
}

func (f *fakeEndpointSecrets) Delete(_ context.Context, id credential.SecretID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeEndpointSecrets) snapshot() (minted []credential.SecretID, rotated []string, deleted []credential.SecretID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]credential.SecretID(nil), f.minted...), append([]string(nil), f.rotated...), append([]credential.SecretID(nil), f.deleted...)
}

// newEndpointEnv wires a ConfigOperation over a REAL profile store (so
// persisted state can be asserted) with the recording secrets double.
// It returns the store's document path as well, so tests can read the raw
// persisted bytes.
func newEndpointEnv(t *testing.T, secrets *fakeEndpointSecrets) (capability.ConfigOperation, *profile.JSONStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := profile.NewJSONStore(filepath.Join(dir, "profiles.json"))
	configGate, vaultGate, _, _, _, _ := testGates()
	op := capability.NewConfigOperation(configGate, vaultGate, testLane(),
		store, store, store, store, newProfileService(t), nil, secrets, secrets)
	return op, store, filepath.Join(dir, "profiles.json")
}

func runConfig(t *testing.T, op capability.ConfigOperation, fn func(context.Context, capability.ConfigService) error) error {
	t.Helper()
	return op.Run(context.Background(), fn)
}

func testEndpoint() profile.Endpoint {
	return profile.Endpoint{
		ID:      "endpoint:custom:openai:1",
		Name:    "OpenAI",
		BaseURL: "https://api.openai.com/v1",
		Schema:  profile.EndpointSchemaOpenAICompatible,
		Models:  []profile.EndpointModel{{Name: "gpt-4o-mini"}},
	}
}

// --- create -------------------------------------------------------------

// The happy path the brief's paired positive demands: on an ordinary
// machine, creating an endpoint with a key SUCCEEDS — the key is minted
// into the vault first, the record holds only the reference, and the key
// VALUE is nowhere in the persisted document.
func TestEndpointCreate_WithKey_MintsFirstAndPersistsOnlyTheReference(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, docPath := newEndpointEnv(t, secrets)
	key := credential.NewSecret("sk-test-0123456789")

	var created profile.Endpoint
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		var err error
		created, err = svc.CreateEndpoint(ctx, testEndpoint(), key)
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	minted, _, _ := secrets.snapshot()
	if len(minted) != 1 {
		t.Fatalf("mints = %d, want exactly 1", len(minted))
	}
	if created.CredentialRef != string(minted[0]) {
		t.Errorf("record ref = %q, want the minted reference %q", created.CredentialRef, minted[0])
	}
	if secrets.mintNames[0] != "OpenAI API key" {
		t.Errorf("mint name = %q, want the ADR-0016 auto-name", secrets.mintNames[0])
	}

	eps, err := store.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(eps) != 1 || eps[0].CredentialRef == "" {
		t.Fatalf("endpoints = %+v, want one with a reference", eps)
	}
	// #nosec G304 — docPath is a t.TempDir path the test itself constructed,
	// never external input; reading it is the assertion.
	docBytes, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	if bytes.Contains(docBytes, []byte("sk-test-0123456789")) {
		t.Fatal("the API key VALUE is persisted in the document")
	}
}

func TestEndpointCreate_WithoutKey_StoresAnEmptyReference(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, _, _ := newEndpointEnv(t, secrets)

	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), credential.Secret{})
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	minted, _, _ := secrets.snapshot()
	if len(minted) != 0 {
		t.Fatalf("mints = %d, want 0 for a keyless create", len(minted))
	}
}

// An invalid record must fail BEFORE the mint: a bad record orphaning a
// freshly-minted key is exactly the state ADR-0011 §4's order exists to
// prevent.
func TestEndpointCreate_InvalidRecord_DoesNotMint(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, _ := newEndpointEnv(t, secrets)
	e := testEndpoint()
	e.Models = nil // invalid

	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, e, credential.NewSecret("sk-test"))
		return err
	}); err == nil {
		t.Fatal("CreateEndpoint must reject the invalid record")
	}
	minted, _, _ := secrets.snapshot()
	if len(minted) != 0 {
		t.Fatalf("mints = %d, want 0 — no orphaned key on a validation failure", len(minted))
	}
	eps, err := store.LoadEndpoints()
	if err != nil || len(eps) != 0 {
		t.Fatalf("endpoints = %+v, want none stored", eps)
	}
}

// The vault refuses to store: nothing is written, the call errors, and the
// user retries. State on disk: no record. State in the vault: nothing (the
// mint never happened).
func TestEndpointCreate_MintFails_NoRecord(t *testing.T) {
	secrets := &fakeEndpointSecrets{mintErr: errors.New("vault sealed")}
	op, store, _ := newEndpointEnv(t, secrets)

	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), credential.NewSecret("sk-test"))
		return err
	}); err == nil {
		t.Fatal("CreateEndpoint must fail when the mint fails")
	}
	eps, err := store.LoadEndpoints()
	if err != nil || len(eps) != 0 {
		t.Fatalf("endpoints = %+v, want none after a failed mint", eps)
	}
}

// The store refuses to write (its document path is a directory — an
// unreadable store, the brief's third failure class): the mint LANDED, so
// the vault holds an ownerless secret — its journaled create, which
// Reconcile deletes at the next start (journal.go:119-137) — and no record
// exists. The restart recovery itself is asserted in the transport
// integration test with a real vault; here the contract is: the call
// errors and the mint happened before the write.
func TestEndpointCreate_StoreWriteFails_LeavesOnlyTheMintedOrphan(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	dir := t.TempDir()
	docPath := filepath.Join(dir, "profiles.json")
	badStore := profile.NewJSONStore(docPath)
	// The store's document path is now an unreadable directory: every load
	// and write fails.
	if err := os.Mkdir(docPath, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	configGate, vaultGate, _, _, _, _ := testGates()
	badOp := capability.NewConfigOperation(configGate, vaultGate, testLane(),
		badStore, badStore, badStore, badStore, newProfileService(t), nil, nil, secrets)

	if err := runConfig(t, badOp, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), credential.NewSecret("sk-test"))
		return err
	}); err == nil {
		t.Fatal("CreateEndpoint must fail when the store cannot be written")
	}
	minted, _, _ := secrets.snapshot()
	if len(minted) != 1 {
		t.Fatalf("mints = %d, want 1 — the mint happened before the write", len(minted))
	}
}

// --- update -------------------------------------------------------------

func TestEndpointUpdate_WithoutKey_KeepsTheCredential(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, _, _ := newEndpointEnv(t, secrets)
	key := credential.NewSecret("sk-original")
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), key)
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	updated := testEndpoint()
	updated.Name = "OpenAI EU"
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		got, err := svc.UpdateEndpoint(ctx, updated, nil)
		if got.CredentialRef != string(secrets.minted[0]) {
			t.Errorf("ref = %q, want the unchanged %q", got.CredentialRef, secrets.minted[0])
		}
		return err
	}); err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	_, rotated, deleted := secrets.snapshot()
	if len(rotated) != 0 || len(deleted) != 0 {
		t.Fatalf("rotated = %v, deleted = %v, want neither — no key given", rotated, deleted)
	}
}

// Update with a new key ROTATES the material behind the endpoint's own
// secret: same reference, ReplaceSecret called with its row, and the record
// keeps the same id — nothing is orphaned, nothing dangles (ADR-0030 §2).
func TestEndpointUpdate_NewKey_RotatesInPlace(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, _, _ := newEndpointEnv(t, secrets)
	key := credential.NewSecret("sk-original")
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), key)
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	originalRef := secrets.minted[0]

	newKey := credential.NewSecret("sk-rotated")
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		got, err := svc.UpdateEndpoint(ctx, testEndpoint(), &newKey)
		if got.CredentialRef != string(originalRef) {
			t.Errorf("ref = %q, want the unchanged %q — rotation keeps the id", got.CredentialRef, originalRef)
		}
		return err
	}); err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	_, rotated, _ := secrets.snapshot()
	if len(rotated) != 1 {
		t.Fatalf("rotations = %d, want 1", len(rotated))
	}
	if rotated[0] != vault.RowFor(originalRef) {
		t.Errorf("rotated row = %q, want %q", rotated[0], vault.RowFor(originalRef))
	}
	if len(secrets.minted) != 1 {
		t.Fatalf("mints = %d, want 1 — rotation mints nothing", len(secrets.minted))
	}
}

// Update with a key on a KEYLESS endpoint mints instead of rotating: there
// is no material to rotate.
func TestEndpointUpdate_NewKey_OnKeylessEndpoint_Mints(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, _, _ := newEndpointEnv(t, secrets)
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), credential.Secret{})
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	key := credential.NewSecret("sk-first")
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		got, err := svc.UpdateEndpoint(ctx, testEndpoint(), &key)
		if got.CredentialRef == "" {
			t.Error("ref must be set after adding a key")
		}
		return err
	}); err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	_, rotated, _ := secrets.snapshot()
	if len(rotated) != 0 {
		t.Fatalf("rotations = %d, want 0 — nothing to rotate", len(rotated))
	}
	if len(secrets.minted) != 1 {
		t.Fatalf("mints = %d, want 1", len(secrets.minted))
	}
}

// Updating an absent endpoint must fail BEFORE any vault call: no mint, no
// rotation for a record that does not exist.
func TestEndpointUpdate_Missing_NoVaultCall(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, _, _ := newEndpointEnv(t, secrets)
	key := credential.NewSecret("sk-test")
	e := testEndpoint()
	e.ID = "endpoint:custom:ghost:1"

	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.UpdateEndpoint(ctx, e, &key)
		return err
	}); err == nil {
		t.Fatal("UpdateEndpoint must fail for an absent endpoint")
	}
	minted, rotated, deleted := secrets.snapshot()
	if len(minted) != 0 || len(rotated) != 0 || len(deleted) != 0 {
		t.Fatalf("mint/rotate/delete = %d/%d/%d, want none for a missing endpoint", len(minted), len(rotated), len(deleted))
	}
}

// The rotation fails: the record is untouched, the call errors, the user
// retries. The rotation is same-id, so a retry converges (ADR-0030 §4).
func TestEndpointUpdate_RotateFails_RecordUnchanged(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, _ := newEndpointEnv(t, secrets)
	key := credential.NewSecret("sk-original")
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), key)
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	secrets.rotateErr = errors.New("provider down")
	newKey := credential.NewSecret("sk-rotated")
	updated := testEndpoint()
	updated.Name = "OpenAI EU"
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.UpdateEndpoint(ctx, updated, &newKey)
		return err
	}); err == nil {
		t.Fatal("UpdateEndpoint must fail when the rotation fails")
	}

	eps, err := store.LoadEndpoints()
	if err != nil || len(eps) != 1 {
		t.Fatalf("endpoints = %+v, want the original record", eps)
	}
	if eps[0].Name != "OpenAI" || eps[0].CredentialRef != string(secrets.minted[0]) {
		t.Errorf("record = %+v, want name OpenAI with the original ref", eps[0])
	}
}

// --- delete -------------------------------------------------------------

// The brief's "deleting an endpoint does not orphan its secret": the record
// goes and the material follows, metadata-first (ADR-0030 §4).
func TestEndpointDelete_RemovesRecordThenMaterial(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, _ := newEndpointEnv(t, secrets)
	key := credential.NewSecret("sk-test")
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), key)
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	ref := secrets.minted[0]

	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.DeleteEndpoint(ctx, testEndpoint().ID)
	}); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}
	_, _, deleted := secrets.snapshot()
	if len(deleted) != 1 || deleted[0] != ref {
		t.Fatalf("deleted = %v, want the endpoint's own reference %q", deleted, ref)
	}
	eps, err := store.LoadEndpoints()
	if err != nil || len(eps) != 0 {
		t.Fatalf("endpoints = %+v, want none", eps)
	}
}

// A keyless endpoint deletes without touching the vault at all.
func TestEndpointDelete_NoRef_NoVaultCall(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, _, _ := newEndpointEnv(t, secrets)
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), credential.Secret{})
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.DeleteEndpoint(ctx, testEndpoint().ID)
	}); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}
	_, _, deleted := secrets.snapshot()
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v, want none for a keyless endpoint", deleted)
	}
}

// The material delete fails: the record is STILL gone (the user's intent),
// and the vault owns the cleanup — the journaled delete is retried by
// Reconcile at the next start (ADR-0030 §4). The service reports success
// exactly as vault.deleteSecret does.
func TestEndpointDelete_MaterialDeleteFails_RecordStillGone(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, _ := newEndpointEnv(t, secrets)
	key := credential.NewSecret("sk-test")
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), key)
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	secrets.deleteErr = errors.New("provider delete failed")
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.DeleteEndpoint(ctx, testEndpoint().ID)
	}); err != nil {
		t.Fatalf("DeleteEndpoint must report success — the record is gone and the journal owns the cleanup: %v", err)
	}
	eps, err := store.LoadEndpoints()
	if err != nil || len(eps) != 0 {
		t.Fatalf("endpoints = %+v, want none", eps)
	}
}

// GetEndpoint names one record by id — the lookup the Test button's
// credential resolution needs (nocx-reu5): the probe names the endpoint and
// the backend resolves the credential it owns, exactly as connections.test
// resolves a profile by its id. A missing id is a NAMED sentinel so the
// caller can tell "no such endpoint" from a store failure.
func TestEndpointGet_ReturnsStoredRecord(t *testing.T) {
	op, _, _ := newEndpointEnv(t, &fakeEndpointSecrets{})
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.CreateEndpoint(ctx, testEndpoint(), credential.Secret{})
		return err
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	want := testEndpoint()
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		got, err := svc.GetEndpoint(want.ID)
		if err != nil {
			return err
		}
		if got.ID != want.ID || got.BaseURL != want.BaseURL || got.Schema != want.Schema {
			t.Errorf("GetEndpoint = %+v, want the stored record %+v", got, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("GetEndpoint: %v", err)
	}
}

func TestEndpointGet_MissingIDIsANamedSentinel(t *testing.T) {
	op, _, _ := newEndpointEnv(t, &fakeEndpointSecrets{})
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, err := svc.GetEndpoint("endpoint:custom:nope:1")
		return err
	})
	if !errors.Is(err, profile.ErrEndpointNotFound) {
		t.Fatalf("GetEndpoint for a missing id = %v, want profile.ErrEndpointNotFound", err)
	}
}

func TestEndpointUpdate_NoKeyDeclarationClearsExistingCredential(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, _ := newEndpointEnv(t, secrets)
	existing := testEndpoint()
	existing.CredentialRef = "sec:v1:existing"
	if err := store.CreateEndpoint(existing); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	updated := existing
	updated.Name = "Local"
	updated.NoKey = true
	updated.CredentialRef = ""

	var got profile.Endpoint
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		var err error
		got, err = svc.UpdateEndpoint(ctx, updated, nil)
		return err
	}); err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	if got.CredentialRef != "" {
		t.Fatalf("updated credential reference = %q, want empty", got.CredentialRef)
	}
	stored, err := store.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(stored) != 1 || stored[0].CredentialRef != "" || !stored[0].NoKey {
		t.Fatalf("stored endpoint = %+v, want noKey with no credential", stored)
	}
}
