/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.preview.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */
import type { SandboxPolicy } from './sandbox.policy'

export interface SandboxPreviewResult {
  operationId: string
  confirmationId: string
  expiresAt: string
  workspaceId: string
  mode: 'off' | 'enforce'
  policy: SandboxPolicy | null
  policyDigest: string
  policyVersion: number
}
