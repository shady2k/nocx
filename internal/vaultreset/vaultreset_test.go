package vaultreset_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vaultreset"
)

// --- doubles -----------------------------------------------------------

type fakeVault struct {
	snap     vault.Snapshot
	failures []vault.PurgeFailure
	purgeErr error
	purged   bool
	// order records every call across BOTH doubles, so a test can assert the
	// sequence rather than only the effects.
	order *[]string
}

func (f *fakeVault) Snapshot(_ context.Context) vault.Snapshot { return f.snap }

func (f *fakeVault) Purge(_ context.Context) ([]vault.PurgeFailure, error) {
	f.purged = true
	*f.order = append(*f.order, "purge")
	return f.failures, f.purgeErr
}

type fakeRefs struct {
	impact   profile.SecretReferenceImpact
	countErr error
	clearErr error
	cleared  bool
	order    *[]string
}

func (f *fakeRefs) CountSecretReferences() (profile.SecretReferenceImpact, error) {
	return f.impact, f.countErr
}

func (f *fakeRefs) ClearAllSecretReferences() (profile.SecretReferenceImpact, error) {
	if f.clearErr != nil {
		return profile.SecretReferenceImpact{}, f.clearErr
	}
	f.cleared = true
	*f.order = append(*f.order, "clear-refs")
	return f.impact, nil
}

func newService(t *testing.T, v *fakeVault, r *fakeRefs) *vaultreset.Service {
	t.Helper()
	return vaultreset.New(v, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func sealedSnapshot(keychainReady bool) vault.Snapshot {
	return vault.Snapshot{
		State: vault.StateSealed,
		Providers: []vault.ProviderSnapshot{
			{ID: vault.ProviderSystem, Ready: keychainReady, Reason: reasonFor(keychainReady)},
			{ID: vault.ProviderFile, Ready: true},
		},
	}
}

func reasonFor(ready bool) vault.Reason {
	if ready {
		return ""
	}
	return vault.ReasonNoService
}

// --- Execute -----------------------------------------------------------

// The order IS the design. References go before secrets so that an
// interruption leaves secrets nothing points at — ADR-0011's "brief
// unreachable orphan is safer than metadata pointing at a secret that is
// gone" — rather than connections claiming a password that cannot be produced.
// Reverse these two and the operation still passes every effect-based
// assertion while losing the only property that makes a journal unnecessary.
func TestExecute_ClearsReferencesBeforeDestroyingSecrets(t *testing.T) {
	order := []string{}
	v := &fakeVault{snap: sealedSnapshot(true), order: &order}
	r := &fakeRefs{impact: profile.SecretReferenceImpact{SecretCount: 3}, order: &order}

	if _, err := newService(t, v, r).Execute(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := []string{"clear-refs", "purge"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// Nothing is destroyed if the references cannot be cleared. This is the one
// failure that must abort, because it is the only one that happens while the
// operation is still reversible by doing nothing.
func TestExecute_AbortsWithoutDestroyingAnythingWhenReferencesCannotBeCleared(t *testing.T) {
	order := []string{}
	v := &fakeVault{snap: sealedSnapshot(true), order: &order}
	r := &fakeRefs{clearErr: errors.New("disk full"), order: &order}

	if _, err := newService(t, v, r).Execute(context.Background()); err == nil {
		t.Fatal("Execute returned nil when the references could not be cleared")
	}
	if v.purged {
		t.Error("the vault was purged after the reference clearing failed")
	}
}

// A store that cannot be reached does NOT fail the reset. The vault document
// is gone either way, so the user can set up protection again — reporting a
// failure here would describe a state that has, in the way that matters,
// succeeded. What is left behind travels as residue instead.
func TestExecute_SucceedsWithResidueWhenAStoreCannotBeCleared(t *testing.T) {
	order := []string{}
	v := &fakeVault{
		snap:     sealedSnapshot(false),
		failures: []vault.PurgeFailure{{Provider: vault.ProviderSystem, Err: errors.New("no service")}},
		purgeErr: errors.New("provider system: no service"),
		order:    &order,
	}
	r := &fakeRefs{impact: profile.SecretReferenceImpact{SecretCount: 2}, order: &order}

	got, err := newService(t, v, r).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute returned an error for a reset that did happen: %v", err)
	}
	if len(got.Residue) != 1 {
		t.Fatalf("Residue = %+v, want one entry", got.Residue)
	}
	if got.Residue[0].Store != string(vault.ProviderSystem) {
		t.Errorf("Residue names %q, want %q", got.Residue[0].Store, vault.ProviderSystem)
	}
}

// Empty, never nil. The transport contract declares an array and a null there
// has cost this project a defect once already (nocx-25k9.14): the renderer
// types it as a list and the first iteration over it throws.
func TestExecute_ResidueIsAnEmptyListWhenEverythingWasRemoved(t *testing.T) {
	order := []string{}
	v := &fakeVault{snap: sealedSnapshot(true), order: &order}
	r := &fakeRefs{order: &order}

	got, err := newService(t, v, r).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Residue == nil {
		t.Error("Residue is nil; want an empty slice")
	}
	if len(got.Residue) != 0 {
		t.Errorf("Residue = %+v, want empty", got.Residue)
	}
}

func TestExecute_ReportsWhatWasActuallyCleared(t *testing.T) {
	order := []string{}
	v := &fakeVault{snap: sealedSnapshot(true), order: &order}
	r := &fakeRefs{
		impact: profile.SecretReferenceImpact{SecretCount: 4, ProfileCount: 6},
		order:  &order,
	}

	got, err := newService(t, v, r).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := vaultreset.Impact{SecretCount: 4, ProfileCount: 6}
	if got.Impact != want {
		t.Errorf("Impact = %+v, want %+v", got.Impact, want)
	}
}

// --- Preview -----------------------------------------------------------

// The user is told the keychain is unreachable BEFORE confirming, not
// half-way through. That is what makes proceeding an informed choice rather
// than a surprise, and it is why the operation itself needs no mid-flight
// prompt.
func TestPreview_ReportsAnUnreachableKeychain(t *testing.T) {
	order := []string{}
	v := &fakeVault{snap: sealedSnapshot(false), order: &order}
	r := &fakeRefs{order: &order}

	got, err := newService(t, v, r).Preview(context.Background())
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if got.SystemKeychainReachable {
		t.Error("SystemKeychainReachable = true for a keychain that is not answering")
	}
}

func TestPreview_ReportsAReachableKeychain(t *testing.T) {
	order := []string{}
	v := &fakeVault{snap: sealedSnapshot(true), order: &order}
	r := &fakeRefs{order: &order}

	got, err := newService(t, v, r).Preview(context.Background())
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !got.SystemKeychainReachable {
		t.Error("SystemKeychainReachable = false for a keychain that is answering")
	}
}

// A store that is not registered is not a store that is broken. Reporting it
// as unreachable would put a warning in the dialog about a thing that does not
// exist on this platform.
func TestPreview_AKeychainThatIsNotRegisteredIsNotUnreachable(t *testing.T) {
	order := []string{}
	v := &fakeVault{
		snap: vault.Snapshot{
			State:     vault.StateSealed,
			Providers: []vault.ProviderSnapshot{{ID: vault.ProviderFile, Ready: true}},
		},
		order: &order,
	}
	r := &fakeRefs{order: &order}

	got, err := newService(t, v, r).Preview(context.Background())
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !got.SystemKeychainReachable {
		t.Error("an absent keychain was reported as unreachable")
	}
}

func TestPreview_ChangesNothing(t *testing.T) {
	order := []string{}
	v := &fakeVault{snap: sealedSnapshot(true), order: &order}
	r := &fakeRefs{impact: profile.SecretReferenceImpact{SecretCount: 3}, order: &order}

	if _, err := newService(t, v, r).Preview(context.Background()); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if v.purged || r.cleared {
		t.Error("Preview destroyed something")
	}
	if len(order) != 0 {
		t.Errorf("Preview performed %v", order)
	}
}

func TestPreview_ReportsWhetherThereIsAVaultToReset(t *testing.T) {
	order := []string{}
	v := &fakeVault{
		snap:  vault.Snapshot{State: vault.StateUninitialized},
		order: &order,
	}
	r := &fakeRefs{order: &order}

	got, err := newService(t, v, r).Preview(context.Background())
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if got.VaultInitialized {
		t.Error("VaultInitialized = true for an uninitialized vault")
	}
}
