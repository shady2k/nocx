package file

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/vaulttest"
)

// ---------------------------------------------------------------------------
// In-memory DocumentStore for tests
// ---------------------------------------------------------------------------

type memDocStore struct {
	mu   sync.Mutex
	docs map[string][]byte
}

func newMemDocStore() *memDocStore {
	return &memDocStore{docs: make(map[string][]byte)}
}

func (s *memDocStore) Read(name string, into any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, ok := s.docs[name]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return false, err
	}
	return true, nil
}

func (s *memDocStore) Write(name string, doc any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	s.docs[name] = data
	return nil
}

func (s *memDocStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, name)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testRootKey returns a 32-byte key for testing.
func testRootKey() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

// unlockedProvider creates a provider, generates a data key, and unlocks it.
func unlockedProvider(t *testing.T) (*Provider, *memDocStore, []byte) {
	t.Helper()
	docs := newMemDocStore()
	rootKey := testRootKey()
	p := New(docs, "vault.json")
	p.SetInstanceID("test-instance")

	if err := p.Unlock(rootKey); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	// Generate a data key so the provider can encrypt.
	if _, err := p.NewDataKey(); err != nil {
		t.Fatalf("NewDataKey: %v", err)
	}

	return p, docs, rootKey
}

// putSecret writes a secret into the provider for testing convenience.
func putSecret(t *testing.T, p *Provider, id credential.SecretID, value string) {
	t.Helper()
	if err := p.Put(context.Background(), id, credential.NewSecret(value)); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// getSecret reads a secret and returns its plaintext.
func getSecret(t *testing.T, p *Provider, id credential.SecretID) string {
	t.Helper()
	s, err := p.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var got []byte
	if err := s.Use(func(b []byte) error { got = bytes.Clone(b); return nil }); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return string(got)
}

// readBlob reads the raw blob from the document store.
func readBlob(t *testing.T, docs *memDocStore, name string) blob {
	t.Helper()
	var b blob
	_, err := docs.Read(name, &b)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	return b
}

// mintRef mints a reference for the file provider in tests.
func mintRef(t *testing.T) credential.SecretID {
	t.Helper()
	id, err := vault.MintReferenceForTest(vault.ProviderFile)
	if err != nil {
		t.Fatalf("MintReferenceForTest: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// Contract suite
// ---------------------------------------------------------------------------

// TestRunProviderContract runs the shared provider contract against an
// unlocked file provider.
func TestRunProviderContract(t *testing.T) {
	vaulttest.RunProviderContract(t, "file", func(t *testing.T) vault.WritableProvider {
		p, _, _ := unlockedProvider(t)
		return p
	})
}

// ---------------------------------------------------------------------------
// Round trip
// ---------------------------------------------------------------------------

func TestRoundTrip(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	id := mintRef(t)
	putSecret(t, p, id, "hunter2")
	if got := getSecret(t, p, id); got != "hunter2" {
		t.Fatalf("round trip = %q, want %q", got, "hunter2")
	}
}

func TestEmptyValueRoundTrip(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	id := mintRef(t)
	putSecret(t, p, id, "")
	if got := getSecret(t, p, id); got != "" {
		t.Fatalf("empty round trip = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Locked tests
// ---------------------------------------------------------------------------

func TestGetWhileLocked(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	p.Lock()
	_, err := p.Get(context.Background(), mintRef(t))
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Get while locked = %v, want ErrVaultSealed", err)
	}
}

func TestPutWhileLocked(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	p.Lock()
	err := p.Put(context.Background(), mintRef(t), credential.NewSecret("x"))
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Put while locked = %v, want ErrVaultSealed", err)
	}
}

func TestDeleteWhileLocked(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	p.Lock()
	err := p.Delete(context.Background(), mintRef(t))
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Delete while locked = %v, want ErrVaultSealed", err)
	}
}

// ---------------------------------------------------------------------------
// Absence is ErrSecretNotFound
// ---------------------------------------------------------------------------

func TestGetAbsent(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	_, err := p.Get(context.Background(), mintRef(t))
	if !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("Get(absent) = %v, want ErrSecretNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Tamper tests — each must produce an error indistinguishable from
// tampering rather than differentiate corrupt data from a wrong key.
// ---------------------------------------------------------------------------

// corruptBlob reads the stored blob, applies the mutation function to the
// blob, and writes it back. Then it attempts to unlock a fresh provider
// with the same root key and asserts the operation returns an error that
// wraps ErrUnsealFailed.
func corruptBlob(t *testing.T, docs *memDocStore, name string, mutate func(b *blob)) {
	t.Helper()

	var b blob
	_, err := docs.Read(name, &b)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	mutate(&b)
	if err := docs.Write(name, &b); err != nil {
		t.Fatalf("write corrupted blob: %v", err)
	}
}

func assertUnsealFails(t *testing.T, p *Provider, rootKey []byte) {
	t.Helper()
	err := p.Unlock(rootKey)
	if err == nil {
		t.Fatal("Unlock succeeded, want error")
	}
	if !errors.Is(err, vault.ErrUnsealFailed) {
		t.Fatalf("Unlock error = %v, want wrapping ErrUnsealFailed", err)
	}
}

func TestTamperFlippedCiphertextByte(t *testing.T) {
	p, docs, rootKey := unlockedProvider(t)
	putSecret(t, p, mintRef(t), "sensitive")
	p.Lock()

	corruptBlob(t, docs, "vault.json", func(b *blob) {
		contents, _ := hex.DecodeString(b.Contents)
		// Flip a byte in the ciphertext portion (after the 12-byte nonce).
		if len(contents) > 13 {
			contents[13] ^= 0x01 // nonce is 12 bytes, so byte 13 is ciphertext
		}
		b.Contents = hex.EncodeToString(contents)
	})

	assertUnsealFails(t, New(docs, "vault.json"), rootKey)
}

func TestTamperFlippedTagByte(t *testing.T) {
	p, docs, rootKey := unlockedProvider(t)
	putSecret(t, p, mintRef(t), "sensitive")
	p.Lock()

	corruptBlob(t, docs, "vault.json", func(b *blob) {
		contents, _ := hex.DecodeString(b.Contents)
		// Flip a byte in the tag (last 16 bytes of GCM).
		if len(contents) > 0 {
			contents[len(contents)-1] ^= 0x01
		}
		b.Contents = hex.EncodeToString(contents)
	})

	assertUnsealFails(t, New(docs, "vault.json"), rootKey)
}

func TestTamperAlteredVersion(t *testing.T) {
	p, docs, rootKey := unlockedProvider(t)
	putSecret(t, p, mintRef(t), "sensitive")
	p.Lock()

	corruptBlob(t, docs, "vault.json", func(b *blob) {
		b.Version = 2
	})

	assertUnsealFails(t, New(docs, "vault.json"), rootKey)
}

func TestTamperUnknownVersion(t *testing.T) {
	p, docs, rootKey := unlockedProvider(t)
	putSecret(t, p, mintRef(t), "sensitive")
	p.Lock()

	corruptBlob(t, docs, "vault.json", func(b *blob) {
		b.Version = 99
	})

	assertUnsealFails(t, New(docs, "vault.json"), rootKey)
}

func TestTamperWrongKey(t *testing.T) {
	p, docs, _ := unlockedProvider(t)
	putSecret(t, p, mintRef(t), "sensitive")
	p.Lock()

	// Unlock with a different root key — must fail with ErrUnsealFailed,
	// indistinguishable from a tampered blob.
	wrongKey := testRootKey()
	assertUnsealFails(t, New(docs, "vault.json"), wrongKey)
}

// ---------------------------------------------------------------------------
// Transplant test — the key test this task exists for
// ---------------------------------------------------------------------------

// TestTransplantInstanceID proves that transplanting the wrappedDataKey and
// contents from one vault instance into another fails AEAD verification.
// Binding only the format version is insufficient: AEAD gives integrity, not
// document identity.
func TestTransplantInstanceID(t *testing.T) {
	// Use the SAME root key for both providers. This isolates the test:
	// transplant failure must come from the vault-instance AAD mismatch,
	// not from a different root key.
	rootKey := testRootKey()

	// Provider A — instance "AAAA".
	docsA := newMemDocStore()
	pA := New(docsA, "vault.json")
	pA.SetInstanceID("AAAA")
	if err := pA.Unlock(rootKey); err != nil {
		t.Fatalf("A Unlock: %v", err)
	}
	if _, err := pA.NewDataKey(); err != nil {
		t.Fatalf("A NewDataKey: %v", err)
	}
	putSecret(t, pA, mintRef(t), "secret-from-A")
	pA.Lock()

	// Provider B — instance "BBBB".
	docsB := newMemDocStore()
	pB := New(docsB, "vault.json")
	pB.SetInstanceID("BBBB")
	if err := pB.Unlock(rootKey); err != nil {
		t.Fatalf("B Unlock: %v", err)
	}
	if _, err := pB.NewDataKey(); err != nil {
		t.Fatalf("B NewDataKey: %v", err)
	}
	putSecret(t, pB, mintRef(t), "secret-from-B")
	pB.Lock()

	// Read blob A, read blob B, overwrite B's wrappedDataKey and contents
	// with A's.
	blobA := readBlob(t, docsA, "vault.json")
	blobB := readBlob(t, docsB, "vault.json")

	blobB.WrappedDataKey = blobA.WrappedDataKey
	blobB.Contents = blobA.Contents

	// Write the tampered blob B back.
	if err := docsB.Write("vault.json", &blobB); err != nil {
		t.Fatalf("write tampered B blob: %v", err)
	}

	// Unlock B with the SAME root key. Should fail because AAD binds the
	// vaultInstance (BBBB) but the ciphertext was authenticated with AAAA.
	pB2 := New(docsB, "vault.json")
	err := pB2.Unlock(rootKey)
	if err == nil {
		t.Fatal("Unlock of transplanted blob succeeded, want error")
	}
	if !errors.Is(err, vault.ErrUnsealFailed) {
		t.Fatalf("Unlock error = %v, want wrapping ErrUnsealFailed", err)
	}
}

// ---------------------------------------------------------------------------
// Lock / Unlock cycle persists secrets
// ---------------------------------------------------------------------------

func TestLockUnlockCycle(t *testing.T) {
	docs := newMemDocStore()
	rootKey := testRootKey()

	p := New(docs, "vault.json")
	p.SetInstanceID("cycle-test")
	if err := p.Unlock(rootKey); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}
	if _, err := p.NewDataKey(); err != nil {
		t.Fatalf("NewDataKey: %v", err)
	}

	id := mintRef(t)
	putSecret(t, p, id, "persist-me")
	p.Lock()

	// Re-open with a fresh provider.
	p2 := New(docs, "vault.json")
	if err := p2.Unlock(rootKey); err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
	if got := getSecret(t, p2, id); got != "persist-me" {
		t.Fatalf("after lock/unlock cycle = %q, want %q", got, "persist-me")
	}
}

func TestUnlockEmptyDocument(t *testing.T) {
	// Unlocking a provider with no document should succeed and start empty.
	docs := newMemDocStore()
	rootKey := testRootKey()
	p := New(docs, "vault.json")
	p.SetInstanceID("empty-test")
	if err := p.Unlock(rootKey); err != nil {
		t.Fatalf("Unlock of empty document: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Secret presence after Put
// ---------------------------------------------------------------------------

func TestOverwrite(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	id := mintRef(t)
	putSecret(t, p, id, "first")
	if err := p.Put(context.Background(), id, credential.NewSecret("second")); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	if got := getSecret(t, p, id); got != "second" {
		t.Fatalf("after overwrite = %q, want %q", got, "second")
	}
}

func TestDeleteAbsent(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	if err := p.Delete(context.Background(), mintRef(t)); err != nil {
		t.Fatalf("Delete absent: %v", err)
	}
}

func TestDeleteThenGetAbsent(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	id := mintRef(t)
	putSecret(t, p, id, "gone-soon")
	if err := p.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := p.Get(context.Background(), id); !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrSecretNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Blob shape verification
// ---------------------------------------------------------------------------

func TestBlobShape(t *testing.T) {
	docs := newMemDocStore()
	rootKey := testRootKey()
	p := New(docs, "vault.json")
	p.SetInstanceID("shape-test")
	if err := p.Unlock(rootKey); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if _, err := p.NewDataKey(); err != nil {
		t.Fatalf("NewDataKey: %v", err)
	}

	id := mintRef(t)
	putSecret(t, p, id, "check-blob")

	var raw map[string]any
	_, err := docs.Read("vault.json", &raw)
	if err != nil {
		t.Fatalf("Read raw blob: %v", err)
	}

	// Check shape.
	if v, ok := raw["version"].(float64); !ok || v != 1 {
		t.Fatalf("version = %v, want 1", raw["version"])
	}
	if v, ok := raw["vaultInstance"].(string); !ok || v == "" {
		t.Fatal("vaultInstance is missing or empty")
	}
	if v, ok := raw["wrappedDataKey"].(string); !ok || v == "" {
		t.Fatal("wrappedDataKey is missing or empty")
	}
	if v, ok := raw["contents"].(string); !ok || v == "" {
		t.Fatal("contents is missing or empty")
	}

	// No extra fields.
	expect := map[string]bool{"version": true, "vaultInstance": true, "wrappedDataKey": true, "contents": true}
	for k := range raw {
		if !expect[k] {
			t.Fatalf("unexpected blob field: %s", k)
		}
	}
}

// ---------------------------------------------------------------------------
// Wrong key is indistinguishable from tampering (no oracle)
// ---------------------------------------------------------------------------

func TestWrongKeyDoesNotLeakOracle(t *testing.T) {
	p, docs, _ := unlockedProvider(t)
	putSecret(t, p, mintRef(t), "dont-leak-me")
	p.Lock()

	wrongKey := testRootKey()
	err := New(docs, "vault.json").Unlock(wrongKey)

	// Must be ErrUnsealFailed, not ErrSecretNotFound or a message that
	// reveals the distinction between "bad key" and "corrupt data".
	if err == nil {
		t.Fatal("Unlock with wrong key succeeded")
	}
	if !errors.Is(err, vault.ErrUnsealFailed) {
		t.Fatalf("Unlock error = %v, want wrapping ErrUnsealFailed", err)
	}
	// Verify the error message does not contain the key or plaintext.
	msg := err.Error()
	if strings.Contains(msg, "dont-leak-me") {
		t.Fatal("error message leaks secret")
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

// brokenDocStore wraps a DocumentStore and fails reads when err is set.
type brokenDocStore struct {
	storage.DocumentStore
	err error
}

func (s *brokenDocStore) Read(name string, into any) (bool, error) {
	return false, s.err
}

// TestStatusStoreReachable asserts that a freshly-created (never unlocked,
// dataKey == nil) provider reports Ready when the store answers.
func TestStatusStoreReachable(t *testing.T) {
	p := New(newMemDocStore(), "vault.json")
	st := p.Status(context.Background())
	if !st.Ready {
		t.Fatalf("Status on new provider = not ready (reason=%q), want Ready", st.Reason)
	}
}

func TestStatusUnlocked(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	st := p.Status(context.Background())
	if !st.Ready {
		t.Fatalf("Status reports not ready on an unlocked provider: reason=%q", st.Reason)
	}
}

// TestStatusSealedStoreReachable asserts that a provider whose data has been
// wiped (Lock) but whose document store is still reachable reports Ready.
// Sealing is the Vault's concern — the store itself answers.
func TestStatusSealedStoreReachable(t *testing.T) {
	p, _, _ := unlockedProvider(t)
	p.Lock()
	st := p.Status(context.Background())
	if !st.Ready {
		t.Fatalf("Status after Lock = not ready (reason=%q), want Ready", st.Reason)
	}
}

// TestStatusStoreUnreachable asserts that a provider whose document store
// returns errors reports not Ready with a store-relevant reason.
func TestStatusStoreUnreachable(t *testing.T) {
	docs := &brokenDocStore{DocumentStore: newMemDocStore(), err: errors.New("disk failure")}
	p := New(docs, "vault.json")
	st := p.Status(context.Background())
	if st.Ready {
		t.Fatal("Status with broken store reports Ready, want not ready")
	}
	if st.Reason == "" {
		t.Fatal("Status with broken store has empty reason, want a store-relevant reason")
	}
}

// A brand-new vault has no blob yet, so the file provider has a root key and no
// data key. Unlock's doc comment promises "the first mutation creates the blob
// lazily"; before this test Put refused instead, and refused with
// ErrVaultSealed — a lie that the renderer turns into an Unlock dialog, so the
// user unlocked, retried and was asked to unlock again forever (nocx-25k9.20).
//
// Asserting "Put returns no error" would be enough to catch it. Reading the
// secret back as well is what proves the minted key is the one the blob was
// written with.
func TestPut_FirstWriteToEmptyStoreMintsDataKey(t *testing.T) {
	docs := newMemDocStore()
	p := New(docs, "vault-blob")
	p.SetInstanceID("inst-1")

	rootKey := testRootKey()
	if err := p.Unlock(rootKey); err != nil {
		t.Fatalf("Unlock on an empty store: %v", err)
	}

	id := credential.SecretID("sec:v1:file:0123456789abcdef0123456789abcdef")
	if err := p.Put(context.Background(), id, credential.NewSecret("hunter2")); err != nil {
		t.Fatalf("first Put after Unlock: %v", err)
	}

	got, err := p.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get after first Put: %v", err)
	}
	var read string
	if err := got.Use(func(b []byte) error { read = string(b); return nil }); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if read != "hunter2" {
		t.Fatalf("read back %q, want %q", read, "hunter2")
	}
}

// The genuinely sealed case must keep saying sealed.
func TestPut_WithoutRootKeyStillReportsSealed(t *testing.T) {
	docs := newMemDocStore()
	p := New(docs, "vault-blob")

	err := p.Put(context.Background(), credential.SecretID("sec:v1:file:0123456789abcdef0123456789abcdef"),
		credential.NewSecret("x"))
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Put with no root key: got %v, want ErrVaultSealed", err)
	}
}

// PurgeAll is what a vault reset calls on this provider. Its material is one
// document, so purging is deleting it — and the in-memory copy has to go with
// it, or a provider that is still unlocked would keep serving secrets that no
// longer exist on disk.
func TestProvider_PurgeAll(t *testing.T) {
	docs := newMemDocStore()
	p := New(docs, "vault-file.json")
	ctx := context.Background()
	root := bytes.Repeat([]byte{7}, 32)

	p.SetInstanceID("inst-1")
	if err := p.Unlock(root); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if _, err := p.NewDataKey(); err != nil {
		t.Fatalf("NewDataKey: %v", err)
	}
	if err := p.Put(ctx, "id-one", credential.NewSecretBytes([]byte("secret"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := p.PurgeAll(ctx); err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}

	if _, ok := docs.docs["vault-file.json"]; ok {
		t.Error("the blob document survived PurgeAll")
	}
	if _, err := p.Get(ctx, "id-one"); err == nil {
		t.Error("a secret is still readable from memory after PurgeAll")
	}
}

// Re-running an interrupted reset must not fail on the half already done.
func TestProvider_PurgeAll_WithNoBlobIsNotAnError(t *testing.T) {
	p := New(newMemDocStore(), "vault-file.json")
	if err := p.PurgeAll(context.Background()); err != nil {
		t.Errorf("PurgeAll on an absent blob: %v, want nil", err)
	}
}
