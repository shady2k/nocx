package vault

import "github.com/shady2k/nocx/internal/credential"

// MintReferenceForTest mints a routable reference for a provider under test.
//
// Production code never calls this. The Vault mints inside Create, because a
// caller able to choose a provider would be making the Vault's routing policy
// decision for it (spec §4.2), and that is the whole reason the reference
// carries the provider tag in the first place.
//
// Test-support code has a genuine need the production path cannot serve: a
// provider's own tests must construct valid references without standing up a
// Vault. The alternatives are worse. Re-implementing the grammar inside each
// provider's test duplicates the one thing id.go exists to own, and a copy
// drifts the moment the grammar gains a version. Keeping the shared contract
// suite inside package vault forces every provider subpackage to hold its own
// copy of the suite, which defeats the purpose of having one.
//
// So: one clearly-named seam, used only by internal/vault/vaulttest.
func MintReferenceForTest(p ProviderID) (credential.SecretID, error) {
	return mintID(p)
}

// ResolveRowForTest resolves a renderer-addressable row handle back to its
// SecretID, for tests that must observe the material behind a wire-driven
// operation.
//
// Production code never calls this. The renderer may not name a secret, and
// no production path maps a row back to its reference — values never come
// back out (ADR-0011 §2), so nothing needs the inverse. Test-support code
// does: a transport test that creates a secret over the socket and must then
// read its value back to prove what was stored has no other route.
func (v *Vault) ResolveRowForTest(row string) (credential.SecretID, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	id, _, ok := v.resolveRowLocked(row, nil)
	return id, ok
}
