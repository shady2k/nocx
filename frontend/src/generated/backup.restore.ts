/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/backup.restore.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface BackupRestoreResult {
  strategy: 'merge' | 'replace'
  settingsChanged: number
  settingsReset: number
  connectionsAdded: number
  connectionsUpdated: number
  connectionsRemoved: number
  groupsAdded: number
  groupsUpdated: number
  groupsRemoved: number
  groupCredentialBindingsRemoved: number
  connectionsRequiringCredential: ProfileRef[]
  omissions: {
    credentialBindingsRemoved: number
    groupCredentialBindingsRemoved: number
    groupDefaultKeysOmitted: number
  }
}
export interface ProfileRef {
  id: string
  name: string
}
