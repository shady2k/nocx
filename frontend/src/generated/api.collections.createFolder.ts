/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.collections.createFolder.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.collections.createFolder JSON-RPC method: ONE folder made inside a collection the user has open, and the collection as it is afterwards. A collection is a folder and it may contain folders (design §6.2) — the Postman importer already writes them, so an imported collection has structure and one built inside nocx had none. The method takes a NAME and the EXISTING folder to put it in, both addressed through the backend-held handle, so it does not join the two api.* methods that accept a root (§13.1). Nesting is repeated calls rather than a path, because a create that made its intermediate folders would silently mint a misspelling and could never answer "that folder is not there".
 */
export interface ApiCollectionsCreateFolderResult {
  /**
   * The new folder's path WITHIN the collection — the parent joined to the name, or just the name when it was made at the root. It is what the caller passes back as `parentRelPath` to make a folder inside this one, and it is carried rather than left to be reassembled: a renderer joining a parent and a name itself would be a second answer to what this folder is called from the root.
   */
  relPath: string
  collection: Collection
}
/**
 * The collection as it is now, with the new folder in it. It rides on the result because the caller's next move is to draw the tree, and a listing fetched in a second round trip would be a second account of one folder taken at a second moment. It is api.collections.open's shape, through the same assembler, so the three results cannot disagree about what a collection is.
 */
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
