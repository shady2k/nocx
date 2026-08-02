package vaulttest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

// RunProviderContract is the behaviour every WritableProvider must share.
// mk must return a fresh, empty provider per subtest.
//
// It lives in a normal (non-test) file in vaulttest so that every provider
// subpackage — system, file, and whatever comes later — can import and run the
// SAME suite. That is the point of having a contract: this machine and CI have
// no Secret Service, so the two providers are exercised very differently, and
// without one shared suite they drift on precisely the edges nobody here can
// observe. A copy of the runner per subpackage would be a contract in name
// only.
//
// The import cycle this shape appears to invite does not exist: vaulttest
// imports vault, and vault's own tests reach the runner from the external test
// package vault_test, which may import vaulttest freely.
func RunProviderContract(t *testing.T, name string, mk func(t *testing.T) vault.WritableProvider) {
	t.Helper()
	ctx := context.Background()

	mint := func(t *testing.T, p vault.Provider) credential.SecretID {
		t.Helper()
		id, err := vault.MintReferenceForTest(p.ID())
		if err != nil {
			t.Fatalf("mint reference: %v", err)
		}
		return id
	}

	put := func(t *testing.T, p vault.WritableProvider, plaintext string) credential.SecretID {
		t.Helper()
		id := mint(t, p)
		if err := p.Put(ctx, id, credential.NewSecret(plaintext)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		return id
	}

	readBack := func(t *testing.T, p vault.Provider, id credential.SecretID) string {
		t.Helper()
		s, err := p.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		var got []byte
		if err := s.Use(func(b []byte) error { got = bytes.Clone(b); return nil }); err != nil {
			t.Fatalf("Use: %v", err)
		}
		return string(got)
	}

	t.Run(name+"/absent is ErrSecretNotFound", func(t *testing.T) {
		p := mk(t)
		if _, err := p.Get(ctx, mint(t, p)); !errors.Is(err, vault.ErrSecretNotFound) {
			t.Fatalf("Get(absent) = %v, want ErrSecretNotFound", err)
		}
	})

	t.Run(name+"/round trip", func(t *testing.T) {
		p := mk(t)
		id := put(t, p, "hunter2")
		if got := readBack(t, p, id); got != "hunter2" {
			t.Fatalf("round trip = %q, want hunter2", got)
		}
	})

	t.Run(name+"/overwrite", func(t *testing.T) {
		p := mk(t)
		id := put(t, p, "first")
		if err := p.Put(ctx, id, credential.NewSecret("second")); err != nil {
			t.Fatalf("Put overwrite: %v", err)
		}
		if got := readBack(t, p, id); got != "second" {
			t.Fatalf("after overwrite = %q, want second", got)
		}
	})

	t.Run(name+"/delete absent succeeds", func(t *testing.T) {
		p := mk(t)
		if err := p.Delete(ctx, mint(t, p)); err != nil {
			t.Fatalf("Delete(absent) = %v, want nil", err)
		}
	})

	t.Run(name+"/delete then get", func(t *testing.T) {
		p := mk(t)
		id := put(t, p, "gone soon")
		if err := p.Delete(ctx, id); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := p.Get(ctx, id); !errors.Is(err, vault.ErrSecretNotFound) {
			t.Fatalf("Get after Delete = %v, want ErrSecretNotFound", err)
		}
	})

	// An empty secret is a stored value, not an absence. Conflating them would
	// make "the user saved a blank password" look like "no password saved",
	// and the keychain backend makes that conflation easy to write by accident.
	t.Run(name+"/empty value is present", func(t *testing.T) {
		p := mk(t)
		id := put(t, p, "")
		if got := readBack(t, p, id); got != "" {
			t.Fatalf("empty round trip = %q, want empty", got)
		}
	})
}
