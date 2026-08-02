/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/vault.deleteSecret.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the vault.deleteSecret JSON-RPC method: the row (addressed by its opaque handle) and its stored material were deleted, metadata first (ADR-0011 §4). The renderer reloads the inventory to see the row gone.
 */
export interface VaultDeleteSecret {}
