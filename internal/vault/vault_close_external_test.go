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

func newClosableVault(t *testing.T) *vault.Vault {
	t.Helper()
	tmpDir := t.TempDir()
	docStore := storage.NewDocumentStore(tmpDir)
	reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	v, err := vault.New(docStore, reg,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

// Close stops the auto-seal goroutine and waits for its completion. Creating
// a batch makes the lifecycle check cover multiple independent goroutines.
// Without Close there is no exit path at all — the loop selects forever — so
// every Vault ever constructed keeps a goroutine and, through it, itself.
func TestClose_StopsTheAutoSealGoroutine(t *testing.T) {
	const batch = 20

	vaults := make([]*vault.Vault, 0, batch)
	for range batch {
		vaults = append(vaults, newClosableVault(t))
	}
	for _, v := range vaults {
		v.Close()
	}
}

// Close is idempotent and safe on a vault that was never set up. Shutdown paths
// call it without knowing what state the vault reached, and a second call must
// not panic on an already-closed channel.
func TestClose_IsIdempotent(t *testing.T) {
	v := newClosableVault(t)
	v.Close()
	v.Close()
}

// Close seals. A shutdown that stopped the timer but left the root key in
// memory would keep exactly the material the seal lifecycle exists to remove.
func TestClose_Seals(t *testing.T) {
	v := newClosableVault(t)
	ctx := context.Background()
	if _, err := v.Setup(ctx, vault.SetupRequest{Passphrase: "correct horse battery staple"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if v.State() != vault.StateUnsealed {
		t.Fatalf("state after Setup = %v, want unsealed", v.State())
	}

	v.Close()

	if v.State() != vault.StateSealed {
		t.Fatalf("state after Close = %v, want sealed", v.State())
	}
	if _, err := v.Create(ctx, credential.NewSecret("nope")); err == nil {
		t.Fatal("Create after Close should fail — the vault is sealed")
	}
}
