/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.replace.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */
import type { Open } from './open'

export interface SandboxOperationResult {
  operationId: string
  paneId: string
  state: 'preparing' | 'active' | 'ended' | 'failed'
  mode: 'off' | 'enforce'
  reason: string
  open: Open | null
}
