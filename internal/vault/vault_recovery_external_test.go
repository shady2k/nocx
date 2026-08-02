package vault_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

// A recovery code must recover the SECRETS, not merely open the vault.
//
// Unsealing cannot stand in for that property. A recovery envelope wrapping the
// wrong root key decrypts perfectly well on its own, so Unseal returns nil and
// the vault reports itself unsealed while everything inside it has become
// unreadable. That is not hypothetical: Setup built its recovery envelope with
// newRecoveryCode(), which mints a FRESH root key and wraps that one, so the
// code handed to the user at setup opened a vault whose contents it could not
// decrypt. The defect survived the unit suite, an adversarial review of the
// package, and an end-to-end test of the recovery path — because every one of
// them stopped at "the vault unsealed".
//
// It also has to run against the real file provider. The in-memory fake in
// vault_test.go does not survive a reopen, so the same test written there fails
// identically whether the implementation is right or wrong, which makes it
// worse than no test at all.
func TestRecoveryCode_RecoversSecretsAfterSeal(t *testing.T) {
	tmpDir := t.TempDir()
	docStore := storage.NewDocumentStore(tmpDir)
	fp := file.New(docStore, "vault-blob.json")

	reg, err := vault.NewRegistry(fp)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}

	ctx := context.Background()
	result, err := v.Setup(ctx, vault.SetupRequest{Passphrase: "correct horse battery staple"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.RecoveryCode == "" {
		t.Fatal("passphrase setup returned no recovery code")
	}

	const plaintext = "hunter2-the-actual-password"
	id, err := v.Create(ctx, credential.NewSecret(plaintext))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v.Seal()

	// Reopen from disk, exactly as a restart would.
	reg2, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("NewRegistry (reopen): %v", err)
	}
	v2, err := vault.New(docStore, reg2, logger)
	if err != nil {
		t.Fatalf("vault.New (reopen): %v", err)
	}
	if v2.State() != vault.StateSealed {
		t.Fatalf("reopened state = %v, want sealed", v2.State())
	}

	if uerr := v2.Unseal(ctx, vault.UnsealRequest{RecoveryCode: result.RecoveryCode}); uerr != nil {
		t.Fatalf("Unseal with the Setup recovery code: %v", uerr)
	}

	got, err := v2.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after recovery-code unseal: %v — the code opened the vault but "+
			"cannot read it, so it is not a recovery factor", err)
	}
	if err := got.Use(func(b []byte) error {
		if string(b) != plaintext {
			t.Fatalf("secret after recovery unseal = %q, want %q", string(b), plaintext)
		}
		return nil
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
}
