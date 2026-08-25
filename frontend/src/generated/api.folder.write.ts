/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.folder.write.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The canonical variables persisted for one folder.
 */
export interface ApiFolderWriteResult {
  variables: {
    name: string
    value: string
    enabled: boolean
  }[]
}
