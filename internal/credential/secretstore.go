// Package credential provides the Secret type and the SecretStore capability.
//
// It deliberately contains no store implementation. Secrets are held by
// providers under internal/vault, and the Vault is what the composition root
// wires (ADR-0011 as amended by the vault design).
package credential

import "context"

// SecretID is an opaque, stable handle to secret material held by a
// SecretStore. It is the ONLY form in which a secret may appear in a
// persisted domain record or cross a package boundary (ADR-0011 §2).
//
// NewSecretID is deliberately NOT exported after V1.10. The provider is
// encoded in the reference itself (spec §4.1), and minting is the Vault's
// job — any caller outside internal/vault that creates a SecretID would
// be choosing a provider, which is routing policy, not a consumer concern.
type SecretID string

// SecretStore is the consumer mutation/existence capability. It deliberately
// cannot return material: reads require Resolver and an explicit stance.
//
// ctx bounds how long the caller waits for an answer. It does NOT cancel an
// effect already delegated to a backend.
type SecretStore interface {
	// Create stores value and returns the assigned SecretID. The caller
	// receives the id the store chose — minting is not a consumer concern.
	Create(ctx context.Context, value Secret) (SecretID, error)

	// Delete removes the secret identified by id. Deleting an absent id
	// is not an error.
	Delete(ctx context.Context, id SecretID) error

	// Exists reports whether a secret with the given id exists.
	Exists(ctx context.Context, id SecretID) (bool, error)
}

// MaterialStore is the composition-only backend contract used to construct a
// Resolver. Domain consumers must receive Resolver or SecretStore, never this
// interface: Get has no stance.
type MaterialStore interface {
	SecretStore
	Get(ctx context.Context, id SecretID) (Secret, error)
}
