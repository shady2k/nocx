/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.import.postman.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.import.postman JSON-RPC method. The export is named by exactly one of `path` (a file on the machine running nocx), `document` (inline JSON bytes), `archiveBytes` (base64-encoded ZIP bytes), or `url` (fetched by the backend over its route). Either way the collection folder has arrived at the destination as one atomic write, and this is the list of what the import did NOT carry over. The list is a RESULT rather than a log line because a soft degrade must be visible in the product; an import that silently dropped a pre-request script would be a feature that does not exist surviving a release. An ARCHIVE import additionally returns one named report per collection or environment it writes, for the same reason the list itself exists: many degrades reported as a total is a total, not a report.
 */
export interface ApiImportPostmanResult {
  /**
   * Never null — an import that carried everything over is [].
   */
  unsupported: Unsupported[]
  /**
   * Present only for an archive import; one report per collection or environment written, in archive-path order.
   */
  documents?: ArchiveDocument[]
}
export interface Unsupported {
  /**
   * The feature, named: a curl flag in its long form, a Postman field, an item path. It NEVER carries an argument's VALUE — a refused --oauth2-bearer would otherwise itemise the token it refused.
   */
  what: string
  why: string
}
export interface ArchiveDocument {
  kind: 'collection' | 'environment'
  name: string
  unsupported: Unsupported[]
}
