/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/backup.create.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface BackupCreateResult {
  fileName: string
  contents: string
  summary: {
    settings: number
    connections: number
    groups: number
    /**
     * The number of snippets in the backup. 0 when the service has no snippet store or the library is empty.
     */
    snippets: number
    credentialBindingsRemoved: number
    groupCredentialBindingsRemoved: number
    groupDefaultKeysOmitted: number
    /**
     * The number of notes in the backup. 0 when the service has no notes store or the library is empty.
     */
    notes: number
  }
}
