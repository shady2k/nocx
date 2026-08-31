/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.stat.error.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Data payload of a files.stat JSON-RPC error. The classifier must distinguish a confirmed missing path from a path it could not inspect; not-found and permission-denied are invalid-params refusals, while an unknown provider failure is an internal error.
 */
export interface FilesStatError {
  /**
   * Fixed classification refusal vocabulary. not-found proves the path is absent; permission-denied and unavailable mean the path could not be classified and must not be treated as a regular file.
   */
  reason: 'not-found' | 'permission-denied' | 'unavailable'
}
