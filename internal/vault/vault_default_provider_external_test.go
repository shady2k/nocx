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

// unreadySystem is a registered system provider that is not usable — the state
// of every machine without a running Secret Service, which is most Linux
// desktops and every CI runner.
//
// No existing double could express it: testProvider.Status returns
// Ready: true unconditionally, so the in-package suite cannot represent a
// provider that is present but cannot work. That gap is why the defect below
// survived the unit tests, an adversarial review, and an end-to-end run.
type unreadySystem struct{}

func (unreadySystem) ID() vault.ProviderID { return vault.ProviderSystem }

func (unreadySystem) Status(_ context.Context) vault.Status {
	return vault.Status{Ready: false, Reason: vault.ReasonNoService}
}

func (unreadySystem) Get(_ context.Context, _ credential.SecretID) (credential.Secret, error) {
	return credential.Secret{}, vault.ErrProviderUnavailable
}

func (unreadySystem) Put(_ context.Context, _ credential.SecretID, _ credential.Secret) error {
	return vault.ErrProviderUnavailable
}

func (unreadySystem) Delete(_ context.Context, _ credential.SecretID) error {
	return vault.ErrProviderUnavailable
}

func (unreadySystem) Exists(_ context.Context, _ credential.SecretID) (bool, error) {
	return false, vault.ErrProviderUnavailable
}

// Passphrase setup on a machine with no usable keychain must route new secrets
// to the file provider.
//
// Setup picked the default by asking the registry whether a system provider was
// REGISTERED, and app.go registers it on every platform — so on any machine
// without a Secret Service the default became a provider that cannot store
// anything, and the first secret write failed with
// "provider system unavailable (no-service)".
//
// The assertion is a stored secret that reads back, not the value of
// DefaultProvider. Naming the provider would pin today's choice; reading the
// secret pins the property a user depends on, and leaves the routing free to
// change.
func TestSetup_PassphraseWithUnusableKeychain_StoresSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	docStore := storage.NewDocumentStore(tmpDir)

	reg, err := vault.NewRegistry(unreadySystem{}, file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}

	ctx := context.Background()
	if _, serr := v.Setup(ctx, vault.SetupRequest{Passphrase: "correct horse battery staple"}); serr != nil {
		t.Fatalf("Setup: %v", serr)
	}

	const plaintext = "hunter2-the-actual-password"
	id, err := v.Create(ctx, credential.NewSecret(plaintext))
	if err != nil {
		t.Fatalf("Create after passphrase setup on a keychain-less machine: %v", err)
	}

	got, err := v.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := got.Use(func(b []byte) error {
		if string(b) != plaintext {
			t.Fatalf("secret = %q, want %q", string(b), plaintext)
		}
		return nil
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
}
