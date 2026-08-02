package vault_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

// TestCreate_ConcurrentFileProvider runs 50 concurrent Create calls against a
// real file.Provider backed by a DocumentStore. Only the file provider is
// registered, so all secrets route to it. Passphrase setup ensures no system
// provider is needed.
func TestCreate_ConcurrentFileProvider(t *testing.T) {
	tmpDir := t.TempDir()
	docStore := storage.NewDocumentStore(tmpDir)

	// The file provider stores its encrypted blob in the same directory.
	fp := file.New(docStore, "vault-blob.json")

	reg, err := vault.NewRegistry(fp)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Passphrase-based setup — this routes to ProviderFile.
	// Argon2 runs at production cost once; subsequent Creates use no KDF.
	result, err := v.Setup(context.Background(), vault.SetupRequest{Passphrase: "test-passphrase"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.RecoveryCode == "" {
		t.Fatal("passphrase setup should return a recovery code")
	}

	ctx := context.Background()
	const n = 50
	type res struct {
		id  credential.SecretID
		err error
		val string
	}
	ch := make(chan res, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val := fmt.Sprintf("concurrent-value-%d", idx)
			creatID, creatErr := v.Create(ctx, credential.NewSecret(val))
			ch <- res{creatID, creatErr, val}
		}(i)
	}
	wg.Wait()
	close(ch)

	results := make([]res, 0, n)
	for r := range ch {
		if r.err != nil {
			t.Fatalf("concurrent Create: %v", r.err)
		}
		results = append(results, r)
	}

	// Commit journal entries so Reconcile on reconstruction (defect 1) does
	// not delete the orphan PhaseSecretWritten entries.
	for _, r := range results {
		atErr := v.AttachTarget(ctx, r.id, "external-test")
		if atErr != nil {
			t.Fatalf("AttachTarget(%s): %v", r.id, atErr)
		}
		cmErr := v.CommitMetadata(ctx, r.id)
		if cmErr != nil {
			t.Fatalf("CommitMetadata(%s): %v", r.id, cmErr)
		}
	}

	// Discard the Vault and provider. Construct fresh instances over the same
	// directory to verify persistence (defect 11: the old test read through the
	// same live provider, so a saveBlob that persisted only the last write
	// would still pass).
	fp2 := file.New(docStore, "vault-blob.json")
	reg2, err := vault.NewRegistry(fp2)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	v2, err := vault.New(docStore, reg2, logger)
	if err != nil {
		t.Fatalf("New after discard: %v", err)
	}
	if err := v2.Unseal(ctx, vault.UnsealRequest{Passphrase: "test-passphrase"}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	for _, r := range results {
		sec, err := v2.Get(ctx, r.id)
		if err != nil {
			t.Fatalf("Get(%s) after reconstruction: %v", r.id, err)
		}
		var s string
		if useErr := sec.Use(func(b []byte) error { s = string(b); return nil }); useErr != nil {
			t.Fatalf("Use(%s): %v", r.id, useErr)
		}
		if s != r.val {
			t.Fatalf("value for %s = %q, want %q", r.id, s, r.val)
		}
	}
}

// TestFileProviderSetupTime measures how long passphrase setup takes with
// production argon2 parameters (nominally 100–200 ms per spec §11).
func TestFileProviderSetupTime(t *testing.T) {
	t0 := time.Now()
	tmpDir := t.TempDir()
	docStore := storage.NewDocumentStore(tmpDir)
	fp := file.New(docStore, "vault-blob.json")
	reg, _ := vault.NewRegistry(fp)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	v, _ := vault.New(docStore, reg, logger)
	_, err := v.Setup(context.Background(), vault.SetupRequest{Passphrase: "timed"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Logf("passphrase setup with file provider: %v", time.Since(t0))
}
