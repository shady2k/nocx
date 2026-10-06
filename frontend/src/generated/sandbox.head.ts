/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.head.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface SandboxHeadSummary {
  launchId: string
  mode: 'off' | 'enforce'
  state: 'preparing' | 'active' | 'ended' | 'failed'
  grantId: number | null
  policyDigest: string
  policyVersion: number
  enforcement: string
  observer: string
}
