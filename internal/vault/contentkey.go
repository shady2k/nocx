package vault

// System-level key seam (ADR-0018 §3, nocx-rtg0.9).
//
// The ContentDB key is a system-level key, not a user secret: it must be
// readable at startup regardless of the seal state, it must never appear in
// the user's secret inventory, and auto-seal must never make history
// unreadable. It therefore follows the osKeyID precedent — the vault's own
// OS-held root key uses the same shape — rather than the user-secret path
// (Create/Get), whose seal and initialization gates would make a restart of
// an initialized vault unreadable.
//
// Routing and the reference grammar stay in this package: the provider tag
// and the material syntax are persisted protocol, and mintID/parseID are the
// only places that own them (spec §4.2).

import (
	"crypto/sha256"
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
)

// contentKeyNamespace namespaces the derived reference so it cannot collide
// with any other deterministic key the vault mints.
const contentKeyNamespace = "nocx-contentdb-key"

// ContentKeyID derives the deterministic SecretID for the ContentDB key at
// the given provider: sec:v1:<provider>:<32hex>.
// The provider is baked into the reference (references are immutable), so a
// default-provider change later cannot silently move the key.
//
// Package-level since nocx-rtg0.14: the content key's home is decided by
// keystore availability, never by the vault's default-provider policy, and
// the key lifecycle holds no *Vault. The reference grammar stays here — this
// package is the only owner of mintID/parseID/validProviderTag.
func ContentKeyID(p ProviderID) (credential.SecretID, error) {
	if err := validProviderTag(p); err != nil {
		return "", fmt.Errorf("content key: %w", err)
	}
	h := sha256.Sum256([]byte(contentKeyNamespace + ":" + string(p)))
	return credential.SecretID(fmt.Sprintf("sec:v1:%s:%x", p, h[:16])), nil
}

// DefaultProvider returns the vault's chosen default provider — the way the
// provider is chosen for every other secret — or "" when none is set
// (uninitialized vault).
func (v *Vault) DefaultProvider() ProviderID {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.doc.DefaultProvider
}
