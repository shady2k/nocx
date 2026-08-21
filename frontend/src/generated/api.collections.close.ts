/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.collections.close.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.collections.close JSON-RPC method: the folder has left the opened-folder list and its handle stops resolving. An empty result is still a contract — additionalProperties: false on an empty shape is what makes "returns nothing" enforceable, and a renderer that wants a field here cannot be written. Closing a handle nobody minted answers an error instead.
 */
export interface ApiCollectionsCloseResult {}
