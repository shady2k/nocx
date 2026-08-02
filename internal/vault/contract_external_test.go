package vault_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/vaulttest"
)

// TestFakeSatisfiesContract runs the shared provider contract against the
// in-memory Fake. A fake that drifts from the contract fails its own suite —
// which matters more than it sounds, because every downstream test that stands
// in a Fake for a real store is only as trustworthy as the Fake's fidelity.
//
// This file is in package vault_test rather than vault so it may import
// vaulttest, which imports vault. That is the whole reason the runner can live
// in a normal file and be shared by the provider subpackages.
func TestFakeSatisfiesContract(t *testing.T) {
	vaulttest.RunProviderContract(t, "fake", func(t *testing.T) vault.WritableProvider {
		return vaulttest.NewFake()
	})
}
