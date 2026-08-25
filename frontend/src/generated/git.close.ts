/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/git.close.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the git.close JSON-RPC method: the binding is closed — removed from the registry and drained — so the repository it guarded is released. closed is always true in a result; a close of an unknown binding answers the unknownBinding error instead (the store re-resolves on the one reason "unknown-binding", nocx-bpqil).
 */
export interface GitCloseResult {
  closed: boolean
}
