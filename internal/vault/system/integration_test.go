package system_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/system"
	"github.com/shady2k/nocx/internal/vault/vaulttest"
)

// cleanupProvider wraps system.Provider and registers every ID it writes for
// cleanup via t.Cleanup, so the real keychain is left clean after the test.
type cleanupProvider struct {
	vault.WritableProvider
	ids []credential.SecretID
	mu  sync.Mutex
}

func newCleanupProvider(t *testing.T) *cleanupProvider {
	t.Helper()
	p := system.New()
	cp := &cleanupProvider{WritableProvider: p}
	t.Cleanup(func() {
		cp.mu.Lock()
		// Snapshot the list so the cleanup closure doesn't reference the
		// mutable slice across goroutines. The test itself is single-threaded,
		// but the contract runner uses t.Run subtests, so snapshotting is
		// defensive.
		ids := make([]credential.SecretID, len(cp.ids))
		copy(ids, cp.ids)
		cp.mu.Unlock()
		ctx := context.Background()
		for _, id := range ids {
			// Best-effort; we are cleaning up. An already-deleted or
			// never-stored ID's Delete is idempotent.
			_ = cp.WritableProvider.Delete(ctx, id)
		}
	})
	return cp
}

func (cp *cleanupProvider) Put(ctx context.Context, id credential.SecretID, s credential.Secret) error {
	cp.mu.Lock()
	cp.ids = append(cp.ids, id)
	cp.mu.Unlock()
	return cp.WritableProvider.Put(ctx, id, s)
}

// TestRealKeyringRoundTrip runs the shared provider contract against a real
// Secret Service daemon. It skips when no such daemon is on the session bus.
func TestRealKeyringRoundTrip(t *testing.T) {
	if !system.SecretServiceAvailable() {
		t.Skip("no usable Secret Service on the session bus (absent, or its collection is locked) — run under dbus-run-session with an unlocked gnome-keyring")
	}
	vaulttest.RunProviderContract(t, "real-keyring", func(t *testing.T) vault.WritableProvider {
		return newCleanupProvider(t)
	})
}

// TestRealKeyringProbeAndCleanup verifies that a real Secret Service daemon
// (a) reports ready via Probe, (b) survives a write-read-compare-delete cycle
// through a test-minted ID, and (c) confirms the deleted entry is gone — all
// without leaking entries into the real keychain.
func TestRealKeyringProbeAndCleanup(t *testing.T) {
	if !system.SecretServiceAvailable() {
		t.Skip("no usable Secret Service on the session bus (absent, or its collection is locked) — run under dbus-run-session with an unlocked gnome-keyring")
	}

	ctx := context.Background()
	p := system.New()
	cp := newCleanupProvider(t)

	// Probe reports ready.
	status := p.Probe(ctx)
	if !status.Ready {
		t.Fatalf("Probe: Ready=false, Reason=%q", status.Reason)
	}

	// Reproduce the same set/get/delete cycle Probe uses, but with a known
	// test-minted ID so we can assert the entry is gone afterwards.
	id, err := vault.MintReferenceForTest(vault.ProviderSystem)
	if err != nil {
		t.Fatalf("MintReferenceForTest: %v", err)
	}
	secret := credential.NewSecret("probe-check-value")
	if err = cp.Put(ctx, id, secret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := cp.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var gotStr string
	if err = got.Use(func(b []byte) error { gotStr = string(b); return nil }); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if gotStr != "probe-check-value" {
		t.Fatalf("round trip = %q, want probe-check-value", gotStr)
	}
	if err = cp.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err = cp.Get(ctx, id); !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrSecretNotFound", err)
	}
}
