/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/endpoints.delete.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the endpoints.delete JSON-RPC method: the empty object — there is nothing to return, the list is the state (the same shape vault.deleteSecret uses). The endpoint record and, metadata-first, the material behind its own secret are gone; a key that could not be removed is the vault journal's pending delete, retried at the next start (ADR-0030 §4).
 */
export interface EndpointsDeleteResult {}
