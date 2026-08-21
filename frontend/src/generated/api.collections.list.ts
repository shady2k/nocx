/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.collections.list.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.collections.list JSON-RPC method: every collection folder the user currently has open, in the order they opened them. The app remembers the LIST of opened folders and never their contents (design §6.1), so each entry's collection is re-read from disk on every call — a request file a colleague's git pull added appears without anything being told about it.
 */
export interface ApiCollectionsListResult {
  /**
   * Never null — nothing open is [].
   */
  collections: OpenCollection[]
}
export interface OpenCollection {
  handle: string
  /**
   * The path the user chose, exactly as they gave it. It is what the folder is CALLED, never something to send back: addressing a request means the handle plus a path relative to it (design §13.1).
   */
  path: string
  collection: Collection
  /**
   * Why this one folder could not be re-read — a root replaced or removed since it was opened — and "" when it could. On the entry rather than in an error beside the listing, so one dead folder cannot hide every live one, and present rather than dropped, so a vanished collection is visible in the product and not only in a log.
   */
  error: string
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
