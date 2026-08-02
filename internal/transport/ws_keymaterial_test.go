package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// testPrivateKeyPEM generates a new ed25519 key and returns its PEM-encoded
// private key text and its SHA256 fingerprint.
func testPrivateKeyPEM(t *testing.T) (pemOut, fingerprint string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	fingerprint = gossh.FingerprintSHA256(signer.PublicKey())
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), fingerprint
}

// testEncryptedKeyPEM generates an ed25519 key, encrypts it with the given
// passphrase, and returns the PEM text and its SHA256 fingerprint.
func testEncryptedKeyPEM(t *testing.T, passphrase string) (pemOut, fingerprint string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	fingerprint = gossh.FingerprintSHA256(signer.PublicKey())

	// Marshal with passphrase uses OpenSSH format which stores the
	// public key unencrypted, so ParseRawPrivateKey can extract it.
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), fingerprint
}

// --- parsePrivateKeyMaterial tests ---

func TestParsePrivateKeyMaterial_ValidUnencrypted(t *testing.T) {
	pem, expectedFP := testPrivateKeyPEM(t)
	fp, wantsPassphrase, err := parsePrivateKeyMaterial(pem)
	if err != nil {
		t.Fatalf("parsePrivateKeyMaterial(unencrypted): %v", err)
	}
	if fp != expectedFP {
		t.Fatalf("fingerprint mismatch: got %q, want %q", fp, expectedFP)
	}
	if wantsPassphrase {
		t.Fatal("passphraseWanted = true for an unencrypted key")
	}
}

func TestParsePrivateKeyMaterial_ValidEncrypted(t *testing.T) {
	pem, expectedFP := testEncryptedKeyPEM(t, "test-passphrase")
	fp, wantsPassphrase, err := parsePrivateKeyMaterial(pem)
	if err != nil {
		t.Fatalf("parsePrivateKeyMaterial(encrypted): %v", err)
	}
	if fp != expectedFP {
		t.Fatalf("fingerprint mismatch: got %q, want %q", fp, expectedFP)
	}
	if !wantsPassphrase {
		t.Fatal("passphraseWanted = false for an encrypted key")
	}
}

func TestParsePrivateKeyMaterial_InvalidText(t *testing.T) {
	_, _, err := parsePrivateKeyMaterial("this is not a private key")
	if err == nil {
		t.Fatal("parsePrivateKeyMaterial should reject non-key text")
	}
	var invalidKey *errInvalidKeyMaterial
	if !errors.As(err, &invalidKey) {
		t.Fatal("error should be *errInvalidKeyMaterial")
	}
}

// Test that arbitrary binary data is rejected.
func TestParsePrivateKeyMaterial_BinaryData(t *testing.T) {
	_, _, err := parsePrivateKeyMaterial("\x00\x01\x02\x03")
	if err == nil {
		t.Fatal("parsePrivateKeyMaterial should reject binary data")
	}
}

// --- saveKeyMaterial RPC tests ---
