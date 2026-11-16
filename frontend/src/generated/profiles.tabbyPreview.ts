/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/profiles.tabbyPreview.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface TabbyPreviewResponse {
  profilesToImport: number
  groupsToImport: number
  secretsToImport: number
  secretProvider: string
  planToken: string
  profileEntries?: {
    name: string
    action: 'new' | 'overwrite' | 'needs-review'
  }[]
  groupNames?: string[]
  secretEntries?: {
    name: string
    type: 'password' | 'passphrase'
  }[]
  skippedSecrets?: {
    secretType: string
    reason: string
  }[]
  collisions?: {
    kind: string
    name: string
    policy: string
  }[]
  warnings?: {
    profile: string
    field: string
    reason: string
  }[]
}
