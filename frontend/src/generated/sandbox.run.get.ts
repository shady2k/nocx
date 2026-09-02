/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.run.get.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of sandbox.run.get. null means Off; candidatePolicy is enforced only when mode is enforce.
 */
export type SandboxRunGet = null | {
  mode: 'learn' | 'enforce'
  issuedAt: number
  backend: 'landlock' | 'seatbelt'
  shellFolder: string
  candidatePolicy: {
    writableRoots: string[]
    readOnlyRoots: string[]
  }
  provenance: {
    workspaceId: string
    source: 'standard' | 'workspace'
    settingsRevision: number
    profileRevision: number | null
  }
}
