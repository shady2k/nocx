/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/backup.preview.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface BackupRestorePreview {
  previewToken: string
  createdAt: string
  strategy: 'merge' | 'replace'
  settings: SettingsCounts
  connections: ItemCounts
  groups: ItemCounts
  snippets: {
    included: number
  }
  connectionsRequiringCredential: ProfileRef[]
  omissions: {
    credentialBindingsRemoved: number
    groupCredentialBindingsRemoved: number
    groupDefaultKeysOmitted: number
  }
  /**
   * The note count the preview reports — what a person reads before deciding to restore over what they have.
   */
  notes: {
    included: number
  }
}
export interface SettingsCounts {
  included: number
  changed: number
  reset: number
}
export interface ItemCounts {
  included: number
  added: number
  updated: number
  removed: number
}
export interface ProfileRef {
  id: string
  name: string
}
