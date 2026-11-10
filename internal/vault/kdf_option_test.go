package vault

import (
	"log/slog"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// A VAULT BUILT THE PRODUCTION WAY WRAPS AT THE PRODUCTION COST
// (nocx-ouru5.1.16). The cost is a constructor option now; nothing in the
// process can change it for everybody, and the composition root passes none —
// so this is what every shipped vault does.
func TestVaultBuiltWithoutOptionsUsesTheProductionKDF(t *testing.T) {
	v, _, _ := testVault(t)
	mustSetup(t, v, "production-cost")

	env := v.doc.Passphrase
	if env == nil {
		t.Fatal("Setup stored no passphrase envelope")
	}
	want := DefaultKDFParams()
	if env.Memory != want.MemoryKiB || env.Time != want.Time || env.Threads != want.Threads {
		t.Fatalf("envelope KDF = m=%d KiB t=%d p=%d, want the production m=%d t=%d p=%d",
			env.Memory, env.Time, env.Threads, want.MemoryKiB, want.Time, want.Threads)
	}
}

// And the argument is what a caller changes — nowhere else.
func TestNewWithKDFParamsWrapsAtThatCost(t *testing.T) {
	tmpDir := t.TempDir()
	store := storage.NewDocumentStore(tmpDir)
	reg, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	v, err := New(store, reg, logger, KDFParams{MemoryKiB: 1, Time: 1, Threads: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(v.Close)
	mustSetup(t, v, "cheap-cost")

	env := v.doc.Passphrase
	if env == nil {
		t.Fatal("Setup stored no passphrase envelope")
	}
	if env.Memory != 1 || env.Time != 1 || env.Threads != 1 {
		t.Fatalf("envelope KDF = m=%d t=%d p=%d, want the argument's m=1 t=1 p=1",
			env.Memory, env.Time, env.Threads)
	}
}
