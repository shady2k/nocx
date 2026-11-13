//go:build nocx_local_ssh

package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/zalando/go-keyring"
	gossh "golang.org/x/crypto/ssh"
)

func TestResolvePrivateKeyPassphraseByHash(t *testing.T) {
	keyring.MockInit()
	rc := newTestRealClient(t)
	ctx := context.Background()
	store := newTestStore()

	// Verify the lookup/save contract: store a secret, retrieve it,
	// confirm it is non-empty without revealing plaintext.
	hash, err := store.Create(ctx, credential.NewSecret("my-passphrase"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := rc.lookupKeyPassphrase(ctx, store, hash)
	if err != nil {
		t.Fatalf("lookupKeyPassphrase: %v", err)
	}
	if got.IsEmpty() {
		t.Error("lookupKeyPassphrase returned empty Secret for a stored passphrase")
	}
}

// TestLoadKeyWithStoredPassphrase verifies the full path: an encrypted
// private key whose passphrase is stored in the SecretStore is successfully
// parsed when loadKey is called with the matching ConnectConfig.
func TestLoadKeyWithStoredPassphrase(t *testing.T) {
	keyring.MockInit()
	rc := newTestRealClient(t)
	ctx := context.Background()
	store := newTestStore()

	dir := t.TempDir()
	passphrase := "encrypted-key-passphrase"

	// Generate and marshal an encrypted ed25519 key.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	path := filepath.Join(dir, "encrypted_test_key")
	if err = os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	// Store the passphrase in the SecretStore.
	id, err := store.Create(ctx, credential.NewSecret(passphrase))
	if err != nil {
		t.Fatalf("store passphrase: %v", err)
	}

	// Without a config, loadKey returns ErrEncryptedKey (no passphrase available).
	_, err = rc.loadKey(ctx, path, nil)
	if err == nil {
		t.Fatal("expected ErrEncryptedKey with nil config, got nil")
	}
	var encErr *ErrEncryptedKey
	if !errors.As(err, &encErr) {
		t.Fatalf("expected *ErrEncryptedKey, got %T: %v", err, err)
	}

	// With cfg but empty PassphraseSecretID, still ErrEncryptedKey.
	cfgNoPass := &ConnectConfig{Secrets: store}
	_, err = rc.loadKey(ctx, path, cfgNoPass)
	if err == nil {
		t.Fatal("expected ErrEncryptedKey with empty PassphraseSecretID, got nil")
	}
	if !errors.As(err, &encErr) {
		t.Fatalf("expected *ErrEncryptedKey, got %T: %v", err, err)
	}

	// With cfg + valid PassphraseSecretID, the key parses successfully.
	cfg := &ConnectConfig{
		Secrets:            store,
		PassphraseSecretID: id,
	}
	signer, err := rc.loadKey(ctx, path, cfg)
	if err != nil {
		t.Fatalf("loadKey with stored passphrase: %v", err)
	}
	if signer == nil {
		t.Fatal("expected non-nil signer from encrypted key")
	}
}

func generateTestKey(t *testing.T) ([]byte, gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(block), signer
}
