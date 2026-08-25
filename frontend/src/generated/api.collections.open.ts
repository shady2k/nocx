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
 * Result of the api.collections.open JSON-RPC method: the backend-minted handle for the opened folder, whether this call is what opened it, and the collection as it is on disk. This is the ONLY api.* result carrying a handle the caller did not already have, because api.collections.open is the only method that accepts a root — every later call names this handle and a path relative to it, and a root is never accepted again (design §13.1).
 */
export interface ApiCollectionsOpenResult {
  /**
   * Opaque, backend-minted, 32 lowercase hex characters. Not a bearer token — the folder is re-validated on every call — but an enumerable one would let a renderer bug reach a folder it never opened. ONE FOLDER HAS ONE HANDLE for as long as it is open: opening a path that is already open answers with the handle that exists rather than minting a second identity for one directory, and identity is the directory itself — a symlink, a trailing slash or a `.` segment are the same collection.
   */
  handle: string
  /**
   * Whether this call is what opened the folder. false is an open; true means the folder was already open and this answers with the handle it already had — the same handle, the same collection, and no second row anywhere. It is on the wire so a surface can tell the two apart and reveal the row it has instead of adding one; deriving it from the tree would make the renderer a second reader of collection identity, which is the defect this field was added to close (nocx-ghuq3). api.collections.create carries no such field: a folder minted a moment ago cannot have been open, so the question has no answer worth sending there.
   */
  alreadyOpen: boolean
  collection: Collection
}
export interface Collection {
  name: string
  /**
   * Every request file in the folder. Never null — an empty collection is [].
   */
  requests: RequestRef[]
  /**
   * Every directory inside the collection, each as a path relative to the root, parents before their children. It is here because a folder with nothing in it yet is invisible in `requests`, and that is the state a folder spends its first minutes in — a tree drawn from the request paths alone would lose the folder the person just made. It is also the ONE answer to "what folders are there": a renderer deriving them from the request paths as well would agree with this list about every folder that holds a request and disagree about every folder that does not. `environments/` is not among them (§6.2), nor is anything beginning with a dot — the same exclusions the request walk makes, because this list comes off the same walk. Never null — a collection with no folders is [].
   */
  folders: string[]
  /**
   * Folders carrying the reserved `.variables.json` file, each as a path relative to the collection root, plus an empty string when the collection root carries one. Presence only; variable values stay in the collection and never cross the wire.
   */
  variableFolders: string[]
  /**
   * The files inside the folder that are not requests OR not environments: bad JSON, a field the format does not declare, a symlink that was not followed. It is ON the collection rather than in an error beside it, because a caller returning early on an error would let one broken file hide every good one. ONE list for both halves of the folder on purpose — "a file in here that cannot be read" is one concept, and a second list would be a second owner of it. Never null — a clean collection is [].
   */
  malformed: MalformedRef[]
  /**
   * Every environment in `environments/` (design §6.2). It is on the collection because it is part of what the folder IS: a renderer that had to ask a second method which environments exist would have two accounts of one folder, read a round trip apart. Never null — a collection with no environments is [], which is an ordinary collection and not a degraded one.
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
 * One environment, as the panel needs it and no further. TWO FIELDS, and the absence of the rest is the point: an environment file also holds VALUES and a route (§6.5), and neither may ride here. The file is the truth and every surface is a projection of it (§6.4), so a copy of the values in the renderer would be a second truth that drifts the moment somebody edits the file — and a `values` field would additionally put the resolved address of every environment on the wire for a panel that only has to name one.
 */
export interface EnvironmentRef {
  /**
   * The environment's path WITHIN the collection — `environments/<name>.json`. It is what api.request.send's `envRelPath` names alongside the handle, exactly as a request is named (§13.1), and it is the value a picker carries.
   */
  relPath: string
  /**
   * The name the FILE declares, which is not derivable from relPath: the backend keys a secret binding by (collection, environment, variable) using this name, so the two can legitimately differ and only the file knows. It is a LABEL here — the renderer never sends it back, because a second answer to "which environment is this" is what the send path must not be given.
   */
  name: string
}
