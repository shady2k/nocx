package capability_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
)

func newStoreGuard(maxQueue int) *capability.StoreGuard {
	return capability.NewStoreGuard(catalog.Declarations(), maxQueue, 3*time.Second)
}

// The plan is READ OFF the catalogue, so the audit's conclusion is the one the
// runtime enforces: config joins both barriers (restore rewrites it, reset
// clears its secret references), the vault joins only the reset barrier, and
// content joins the restore barrier.
func TestStoreGuard_PlanIsTheCatalogue(t *testing.T) {
	stores := newStoreGuard(8)

	if got := stores.Barriers(); len(got) != 2 || got[0] != capability.BarrierStoreSet || got[1] != capability.BarrierVaultReset {
		t.Fatalf("barriers = %v, want [%s %s]", got, capability.BarrierStoreSet, capability.BarrierVaultReset)
	}
	if got := stores.BarriersOf(capability.StoreConfig); len(got) != 2 {
		t.Fatalf("config barriers = %v, want both; restore writes it and reset clears its references", got)
	}
	if got := stores.BarriersOf(capability.StoreVault); len(got) != 1 || got[0] != capability.BarrierVaultReset {
		t.Fatalf("vault barriers = %v, want [%s] only: a backup carries no secret material", got, capability.BarrierVaultReset)
	}
	if got := stores.BarriersOf(capability.StoreContent); len(got) != 1 || got[0] != capability.BarrierStoreSet {
		t.Fatalf("content barriers = %v, want [%s]", got, capability.BarrierStoreSet)
	}

	// The restore declaration's stores are exactly the ones the audit found it
	// writes; the vault is not among them.
	if got := stores.StoresOf(catalog.OpBackupRestore.Declaration()); len(got) != 2 ||
		got[0] != capability.StoreConfig || got[1] != capability.StoreContent {
		t.Fatalf("backup.restore stores = %v, want [config content]", got)
	}
	if got := stores.StoresOf(catalog.OpVaultReset.Declaration()); len(got) != 2 ||
		got[0] != capability.StoreConfig || got[1] != capability.StoreVault {
		t.Fatalf("vault.reset stores = %v, want [config vault]", got)
	}
}

// Restore waits for every reader and admits no new one while it waits. The
// refusal of a new reader is how the test observes that the exclusive waiter
// is registered, without depending on a duration.
func TestStoreGuard_RestoreExcludesReaders(t *testing.T) {
	stores := newStoreGuard(8)
	reader, rej := stores.Shared(capability.StoreConfig).TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("reader refused: %+v", rej)
	}
	defer reader.Release()

	granted := make(chan control.Permit, 1)
	go func() {
		p, r := stores.Exclusive(catalog.OpBackupRestore.Declaration()).TryAcquire(context.Background())
		if r == nil {
			granted <- p
		}
	}()

	waittest.WaitFor(t, "the restoring operation to queue", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		p, r := stores.Shared(capability.StoreConfig).TryAcquire(ctx)
		if r == nil {
			p.Release()
			return false
		}
		return true
	})

	reader.Release()
	var permit control.Permit
	waittest.WaitFor(t, "the restoring operation to hold the store set", func() bool {
		select {
		case permit = <-granted:
			return true
		default:
			return false
		}
	})
	defer permit.Release()

	// While it holds the set, a reader is refused by its own bound.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, r := stores.Shared(capability.StoreConfig).TryAcquire(ctx); r == nil {
		t.Fatal("a reader was admitted while the restore held the store set")
	}
}

// Quarantine is unavailable, not busy: the failure names the store and says a
// restart ends it, it is not the saturation error, and the stores it does not
// name keep serving.
func TestStoreGuard_QuarantineIsANamedFailureUntilRestart(t *testing.T) {
	stores := newStoreGuard(8)
	lane := control.NewSemaphore("test-lane", 4)
	stores.Quarantine([]string{capability.StoreConfig}, "the rollback did not finish within 10m0s")

	config := capability.NewSnippetOperation(stores.Shared(capability.StoreConfig), lane, nil)
	ran := false
	err := config.Run(context.Background(), func(context.Context, capability.SnippetService) error {
		ran = true
		return nil
	})
	if ran {
		t.Fatal("an operation over a quarantined store ran its callback")
	}
	var unavailable *capability.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want *capability.UnavailableError", err)
	}
	if capability.IsRefused(err) {
		t.Fatal("a quarantined store answered as a saturation refusal")
	}
	if !strings.Contains(err.Error(), "Restart nocx") || !strings.Contains(err.Error(), "settings and connection") {
		t.Fatalf("the failure does not name the store and the way out: %v", err)
	}

	// And it is not transient: the same admission refuses again.
	if err := config.Run(context.Background(), func(context.Context, capability.SnippetService) error {
		t.Fatal("a second operation over a quarantined store ran its callback")
		return nil
	}); err == nil {
		t.Fatal("the quarantine was lifted by a second attempt")
	}

	// The vault is its own store and stays available.
	vault := capability.NewVaultOperation(stores.Shared(capability.StoreVault), lane, nil)
	if err := vault.Run(context.Background(), func(context.Context, capability.VaultService) error {
		return nil
	}); err != nil {
		t.Fatalf("the vault was refused while a different store was quarantined: %v", err)
	}
}

// Sealed vault work must not wait behind a restore. The vault's plan names the
// reset barrier only, so a restore queued for the store-set barrier has no
// admission the vault path needs — and that is true with capacity exhausted,
// because the vault's barrier and key are a different failure domain.
func TestSealedVaultWorkDoesNotWaitBehindAPendingRestore(t *testing.T) {
	// maxQueue 1: the pending restore fills the store-set queue by itself, so
	// the queue really is exhausted while the vault path is exercised.
	stores := newStoreGuard(1)
	lane := control.NewSemaphore("test-lane", 4)

	reader, rej := stores.Shared(capability.StoreConfig).TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("reader refused: %+v", rej)
	}
	defer reader.Release()

	type result struct {
		permit control.Permit
		rej    *control.Rejection
	}
	restore := make(chan result, 1)
	go func() {
		p, r := stores.Exclusive(catalog.OpBackupRestore.Declaration()).TryAcquire(context.Background())
		restore <- result{permit: p, rej: r}
	}()

	waittest.WaitFor(t, "the restore to fill the store-set queue", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		p, r := stores.Shared(capability.StoreConfig).TryAcquire(ctx)
		if r == nil {
			p.Release()
			return false
		}
		return r.Kind == control.RejectionCapacity || r.Kind == control.RejectionCancelled || r.Kind == control.RejectionWaitExceeded
	})
	select {
	case got := <-restore:
		if got.permit != nil {
			got.permit.Release()
		}
		t.Fatalf("the restore was granted while a reader still held the store set: %+v", got.rej)
	default:
	}

	// The sealed vault work: a vault admission for the read that raised the
	// prompt, and the answer's own admission. Neither shares a resource with
	// the pending restore.
	read, rej := stores.Shared(capability.StoreVault).TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("sealed vault work was refused while a restore was pending: %+v", rej)
	}
	read.Release()

	vault := capability.NewVaultOperation(stores.Shared(capability.StoreVault), lane, nil)
	if err := vault.Run(context.Background(), func(context.Context, capability.VaultService) error {
		return nil
	}); err != nil {
		t.Fatalf("the unlock's answer could not run while a restore was pending: %v", err)
	}

	// And the restore makes progress once the reader lets go.
	reader.Release()
	var got result
	waittest.WaitFor(t, "the restore to be granted after the reader releases", func() bool {
		select {
		case got = <-restore:
			return true
		default:
			return false
		}
	})
	if got.rej != nil {
		t.Fatalf("the restore was refused: %+v", got.rej)
	}
	got.permit.Release()
}

// ── The restore operation's quarantine, end to end ─────────────────────

// The stores the restore writes, with one write that can be made to fail. The
// rollback is the connections store's SECOND write: the first is the apply,
// which succeeds, and the second is the rollback that the quarantine is about.
type failingConnStore struct {
	snap  profile.ConnectionSnapshot
	calls int
}

func (s *failingConnStore) LoadConnectionSnapshot() (profile.ConnectionSnapshot, error) {
	return s.snap, nil
}

func (s *failingConnStore) ReplaceConnectionSnapshot(v profile.ConnectionSnapshot) error {
	s.calls++
	if s.calls == 2 {
		return errors.New("injected rollback failure")
	}
	s.snap = v
	return nil
}

type failingSettingsStore struct {
	overrides map[string]any
	calls     int
}

func (s *failingSettingsStore) NonSecretOverrides() map[string]any {
	out := make(map[string]any, len(s.overrides))
	for k, v := range s.overrides {
		out[k] = v
	}
	return out
}

func (s *failingSettingsStore) ReplaceNonSecretOverrides(v map[string]any) (settings.PendingNotification, error) {
	s.calls++
	if s.calls == 1 {
		return settings.PendingNotification{}, errors.New("injected settings write failure")
	}
	s.overrides = v
	return settings.PendingNotification{}, nil
}

func (s *failingSettingsStore) Publish(settings.PendingNotification) {}

func (s *failingSettingsStore) ValidateSetting(string, any) error { return nil }

type mapDocStore struct{ data map[string][]byte }

func (s *mapDocStore) Read(name string, into any) (bool, error) {
	raw, ok := s.data[name]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, into)
}

func (s *mapDocStore) Write(name string, doc any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[name] = raw
	return nil
}

func (s *mapDocStore) Delete(name string) error {
	delete(s.data, name)
	return nil
}

// A failed rollback is what turns a store set into two generations, and the
// operation — not the caller, not a log line — has to make those stores
// unavailable. The test drives the real backup service through the real
// restore operation and asserts both halves: the error a person would see is
// the quarantine's, and a later read of the store is refused.
func TestRestoreOperation_QuarantinesTheStoresAfterAFailedRollback(t *testing.T) {
	source := backup.NewService(
		&failingConnStore{snap: profile.ConnectionSnapshot{
			Profiles: []profile.SSHProfile{{Base: profile.Base{ID: "new", Type: "ssh", Name: "new"}, Options: profile.StoredSSHProfileOptions{Host: "new.example"}}},
		}},
		&failingSettingsStore{overrides: map[string]any{"tab.placement": "new"}},
		&mapDocStore{}, nil, nil, nil,
	)
	created, err := source.Create()
	if err != nil {
		t.Fatalf("create the backup document: %v", err)
	}

	conn := &failingConnStore{snap: profile.ConnectionSnapshot{
		Profiles: []profile.SSHProfile{{Base: profile.Base{ID: "old", Type: "ssh", Name: "old"}, Options: profile.StoredSSHProfileOptions{Host: "old.example"}}},
	}}
	sett := &failingSettingsStore{overrides: map[string]any{"tab.placement": "old"}}
	svc := backup.NewService(conn, sett, &mapDocStore{}, nil, nil, nil)
	svc.SetRecoveryBudget(time.Minute)

	preview, err := svc.Preview(created.Contents, backup.RestoreReplace)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	guard := newStoreGuard(8)
	lane := control.NewSemaphore("test-lane", 2)
	op := capability.NewRestoreOperation(guard, catalog.OpBackupRestore.Declaration(), lane, svc)

	err = op.Run(context.Background(), func(_ context.Context, s capability.BackupService) error {
		_, rerr := s.Restore(created.Contents, backup.RestoreReplace, preview.PreviewToken)
		return rerr
	})
	var unavailable *capability.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want *capability.UnavailableError", err)
	}
	if capability.IsRefused(err) {
		t.Fatal("a quarantined store answered as a saturation refusal")
	}
	for _, store := range []string{capability.StoreConfig, capability.StoreContent} {
		if _, ok := guard.Quarantined(store); !ok {
			t.Fatalf("%s was not quarantined by the failed rollback", store)
		}
	}

	// A read that arrives afterwards is refused with the quarantine, not let
	// through to a half-applied generation.
	read := capability.NewSnippetOperation(guard.Shared(capability.StoreConfig), lane, nil)
	if err := read.Run(context.Background(), func(context.Context, capability.SnippetService) error {
		t.Fatal("a read ran over a quarantined store")
		return nil
	}); !errors.As(err, &unavailable) {
		t.Fatalf("a later read got %v, want the quarantine", err)
	}
}
