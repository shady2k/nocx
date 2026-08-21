/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.collections.open.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.collections.open JSON-RPC method: the backend-minted handle for the opened folder, and the collection as it is on disk. This is the ONLY api.* result carrying a handle the caller did not already have, because api.collections.open is the only method that accepts a root — every later call names this handle and a path relative to it, and a root is never accepted again (design §13.1).
 */
export interface ApiCollectionsOpenResult {
  /**
   * Opaque, backend-minted, 32 lowercase hex characters. Not a bearer token — the folder is re-validated on every call — but an enumerable one would let a renderer bug reach a folder it never opened.
   */
  handle: string
  collection: Collection
}
export interface Collection {
  name: string
  /**
   * Every request file in the folder. Never null — an empty collection is [].
   */
  requests: RequestRef[]
  /**
   * The files inside the folder that are not requests: bad JSON, a field the format does not declare, a symlink that was not followed. It is ON the collection rather than in an error beside it, because a caller returning early on an error would let one broken file hide every good one. Never null — a clean collection is [].
   */
  malformed: MalformedRef[]
}
export interface RequestRef {
  /**
   * The request's path WITHIN the collection. It is what every later call names alongside the handle; it is never a path the renderer chose.
   */
  relPath: string
  name: string
  method: string
}
export interface MalformedRef {
  relPath: string
  /**
   * For a person: which file, and what was wrong with it.
   */
  reason: string
}
