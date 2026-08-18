/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.access.status.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Availability and loss state of the diagnostic denied-access observer. Enforcement availability is reported separately by sandbox.status.
 */
export interface SandboxAccessStatus {
  available: boolean
  platform: 'linux' | 'darwin' | 'unsupported'
  backend?: string
  reason?: string
  detail?: string
  lost: number
}
