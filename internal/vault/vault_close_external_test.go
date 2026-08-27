package vault_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
	"github.com/shady2k/nocx/internal/waittest"
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

// autoSealGoroutines counts the vault's own background goroutines, by the
// name of the function they are parked in.
//
// The deleted version of this test compared runtime.NumGoroutine() totals,
// which meant it could not tell one of our goroutines from the test runtime's
// own — and paid for that with five rounds of Gosched and a 10ms sleep to
// keep the totals meaningful at all. Naming the goroutine removes both the
// noise and the sleep.
func autoSealGoroutines() int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "vault.(*Vault).autoSealLoop")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// Close stops the auto-seal goroutine and does not return until it is gone.
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

	// The floor is what earlier tests still hold. Take it as two consecutive
	// equal observations: a goroutine whose Close has returned is still on the
	// runtime's list for a few instructions afterwards, and counting one of
	// those into the floor would put the delta below permanently out of reach.
	before := -1
	waittest.WaitForDetail(t, "the auto-seal goroutine count to settle before the batch",
		func() string { return fmt.Sprintf("last count=%d", before) },
		func() bool {
			n := autoSealGoroutines()
			settled := n == before
			before = n
			return settled
		},
	)

	vaults := make([]*vault.Vault, 0, batch)
	for range batch {
		vaults = append(vaults, newClosableVault(t))
	}

	running := before
	waittest.WaitForDetail(t,
		fmt.Sprintf("at least %d new goroutines while %d vaults are open", batch, batch),
		func() string { return fmt.Sprintf("before=%d running=%d", before, running) },
		func() bool { running = autoSealGoroutines(); return running-before >= batch },
	)

	for _, v := range vaults {
		v.Close()
	}

	after := running
	waittest.WaitForDetail(t, "the auto-seal goroutines to go away after Close",
		func() string {
			return fmt.Sprintf("goroutines did not go away after Close: before=%d running=%d after=%d",
				before, running, after)
		},
		func() bool { after = autoSealGoroutines(); return after-before < batch },
	)
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
