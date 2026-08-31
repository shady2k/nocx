/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.visible.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the files.visible JSON-RPC method: an empty acknowledgement. The method's only observable effect is on the binding's poll cadence — no listing while hidden, one immediately on becoming visible.
 */
export interface FilesVisibleResult {}
