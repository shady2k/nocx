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
  /**
   * WHERE a collection made with no place named goes — the DIRECTORY that holds them, `<data dir>/collections`, and never a collection inside it. It rides the listing because it is a fact about this build rather than about any one folder, and the listing is the call every surface already makes.
   *
   * It exists so an ask can PROPOSE a destination instead of demanding one. `api.collections.create` takes a name and puts the folder here; the import ask next door asked for an absolute path with nothing filled in, which is the same concept behind two doors of very different difficulty — and the harder one is what somebody arriving from Postman meets. A proposal rather than "send nothing and let the backend decide": the person has to be able to SEE where their collection is about to land and change it, which an empty field with a sentence under it does not give them.
   *
   * "" when this build has no app directory to derive it from — the state apicoll.ErrNoDefaultLocation names for the creation path. A surface that gets "" proposes nothing and the person types a path, which is what they already do; nothing was promised, so nothing degraded.
   */
  defaultRoot: string
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
