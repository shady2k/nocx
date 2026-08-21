/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.collections.create.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.collections.create JSON-RPC method: a collection minted under a NAME in the default location the backend decides (design §6.1), and left OPEN. The shape is api.collections.open's on purpose — a create hands back the same handle-and-collection an open does, so the renderer has one thing to do afterwards rather than two, and there is no moment at which a freshly made collection is not addressable. The method takes a name and never a path: the location is derived inside the backend, so api.collections.open and api.import.postman remain the only api.* methods that accept one (§13.1).
 */
export interface ApiCollectionsCreateResult {
  /**
   * Opaque, backend-minted, 32 lowercase hex characters — the same handle api.collections.open mints, addressing the folder that was just created. Not a bearer token: the folder is re-validated on every call.
   */
  handle: string
  collection: Collection
}
export interface Collection {
  name: string
  /**
   * Every request file in the folder. A collection that has just been created has none, so this is []. Never null.
   */
  requests: RequestRef[]
  /**
   * The files inside the folder that are not requests. A collection that has just been created has none, so this is []. Never null — a renderer walking null is a crash rather than an empty view.
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
