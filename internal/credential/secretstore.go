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

// SecretStore is the consumer contract for writing and reading secrets.
// Implementations are responsible for the confidentiality and integrity of
// the stored material (ADR-0011 §2).
//
// ctx bounds how long the caller waits for an answer. It does NOT cancel
// the effect — a storage backend (e.g. go-keyring) may take no context, so
// a Create or Delete that timed out may still land. That is precisely why
// the Vault journals before delegating (spec §4.2).
type SecretStore interface {
	// Create stores value and returns the assigned SecretID. The caller
	// receives the id the store chose — minting is not a consumer concern.
	Create(ctx context.Context, value Secret) (SecretID, error)

	// Get retrieves the secret identified by id. Returns an empty Secret
	// with a nil error when the id is not found.
	Get(ctx context.Context, id SecretID) (Secret, error)

	// Delete removes the secret identified by id. Deleting an absent id
	// is not an error.
	Delete(ctx context.Context, id SecretID) error

	// Exists reports whether a secret with the given id exists.
	Exists(ctx context.Context, id SecretID) (bool, error)
}
