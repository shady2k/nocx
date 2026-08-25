/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.folder.read.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The variables declared by one folder, exactly as its reserved file stores them.
 */
export interface ApiFolderReadResult {
  variables: {
    name: string
    value: string
    enabled: boolean
  }[]
}
