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
   * Every folder inside the collection (api.collections.open.schema.json says what the list is for). A collection that has just been minted has none, so this is []. Never null. It is declared here because this result is api.collections.open's shape and the two must not drift.
   */
  folders: string[]
  /**
   * Folders carrying the reserved `.variables.json` file, plus an empty string when the collection root carries one. Presence only; values remain on disk.
   */
  variableFolders: string[]
  /**
   * The files inside the folder that are not requests or not environments. A collection that has just been created has none, so this is []. Never null — a renderer walking null is a crash rather than an empty view.
   */
  malformed: MalformedRef[]
  /**
   * Every environment in `environments/` (design §6.2). Create makes the directory and leaves it EMPTY, so this is [] for a collection that has just been minted — a collection nobody has configured yet, not a broken one. It is declared here because this result is api.collections.open's shape and the two must not drift: a create answering a collection without the field would reach the renderer as a row with a list nobody filled in.
   */
  environments: EnvironmentRef[]
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
/**
 * One environment, as the panel needs it and no further — api.collections.open.schema.json declares the same two fields and says why the values and the route are not among them.
 */
export interface EnvironmentRef {
  /**
   * The environment's path WITHIN the collection — `environments/<name>.json`. It is what api.request.send's `envRelPath` names alongside the handle.
   */
  relPath: string
  /**
   * The name the FILE declares, which is not derivable from relPath. A LABEL for the renderer; it is never sent back.
   */
  name: string
}
