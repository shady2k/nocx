package vault_test

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"testing"
	"time"

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

// Close stops the auto-seal goroutine.
//
// Counting goroutines is a blunt instrument, so this creates a batch and
// measures the delta rather than an absolute: a leak of one per Vault is
// invisible in an absolute count next to the test runtime's own goroutines,
// and obvious as a delta.
//
// Without Close there is no exit path at all — the loop selects forever — so
// every Vault ever constructed keeps a goroutine and, through it, itself.
func TestClose_StopsTheAutoSealGoroutine(t *testing.T) {
	const batch = 20

	settle := func() int {
		// Two rounds: goroutines returning from a select need a scheduling
		// point before they are gone from the count.
		for range 5 {
			runtime.Gosched()
			time.Sleep(10 * time.Millisecond)
		}
		return runtime.NumGoroutine()
	}

	before := settle()

	vaults := make([]*vault.Vault, 0, batch)
	for range batch {
		vaults = append(vaults, newClosableVault(t))
	}
	running := settle()
	if running-before < batch {
		t.Fatalf("expected at least %d new goroutines while %d vaults are open, saw %d",
			batch, batch, running-before)
	}

	for _, v := range vaults {
		v.Close()
	}
	after := settle()

	if after-before >= batch {
		t.Fatalf("goroutines did not go away after Close: before=%d running=%d after=%d",
			before, running, after)
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
