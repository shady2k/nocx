package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A signature this tool writes must verify against the public key derived from
// the same seed, over the exact manifest bytes — that is the contract the
// compiled-in keyring (a75.3) will rely on. This test stands in for that
// verifier so a format drift is caught here rather than in the field.
func TestSignRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(seedEnv, base64.StdEncoding.EncodeToString(priv.Seed()))

	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	body := []byte(`{"version":"0.2.0"}`)
	if err := os.WriteFile(manifest, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "manifest.json.sig")
	if err := sign(manifest, sigPath); err != nil {
		t.Fatal(err)
	}

	sig := decodeSig(t, sigPath)
	if !ed25519.Verify(pub, body, sig) {
		t.Fatal("signature did not verify against the derived public key")
	}
	// A one-byte change to the signed bytes must break verification.
	if ed25519.Verify(pub, []byte(`{"version":"0.2.1"}`), sig) {
		t.Fatal("signature verified against tampered bytes")
	}
}

func TestSignRejectsWrongSeedLength(t *testing.T) {
	// 31 bytes, one short of ed25519.SeedSize.
	t.Setenv(seedEnv, base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize-1)))
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sign(manifest, filepath.Join(dir, "out.sig")); err == nil {
		t.Fatal("expected an error for an undersized seed, got nil")
	}
}

func TestSignRequiresSeed(t *testing.T) {
	t.Setenv(seedEnv, "")
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sign(manifest, filepath.Join(dir, "out.sig")); err == nil {
		t.Fatal("expected an error when the seed env is empty, got nil")
	}
}

func decodeSig(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // path is test-controlled
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("signature file is not valid base64: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	return sig
}
