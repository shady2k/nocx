/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.status.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of sandbox.status. Learn and Enforce are independently available; Learn is unrestricted and best effort.
 */
export interface SandboxStatus {
  learn: ModeStatus
  enforce: ModeStatus
}
export interface ModeStatus {
  available: boolean
  backend: 'landlock' | 'seatbelt' | 'unsupported'
  reason?: string
  detail?: string
  abi?: number
  state: 'available' | 'unavailable' | 'degraded'
  coverage: string[]
}
