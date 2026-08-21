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
 * Result of the api.import.postman JSON-RPC method: the collection folder has arrived at the destination as one atomic write, and this is the list of what the import did NOT carry over. The list is a RESULT rather than a log line because a soft degrade must be visible in the product; an import that silently dropped a pre-request script would be a feature that does not exist surviving a release.
 */
export interface ApiImportPostmanResult {
  /**
   * Never null — an import that carried everything over is [].
   */
  unsupported: Unsupported[]
}
export interface Unsupported {
  /**
   * The feature, named: a curl flag in its long form, a Postman field, an item path. It NEVER carries an argument's VALUE — a refused --oauth2-bearer would otherwise itemise the token it refused.
   */
  what: string
  why: string
}
