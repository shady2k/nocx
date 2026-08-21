/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.request.write.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.request.write JSON-RPC method: the request file on disk now holds what was sent. An empty result is still a contract — the file is the truth (design §6.4), so there is nothing for the write to echo back that a read would not answer more honestly.
 */
export interface ApiRequestWriteResult {}
