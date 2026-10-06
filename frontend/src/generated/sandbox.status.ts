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
import type { SandboxHeadSummary } from './sandbox.head'
import type { SandboxSource } from './sandbox.source'

export interface SandboxStatusResult {
  paneId: string
  workspaceId: string
  enabled: boolean
  standardRevision: number
  workspaceRevision: number
  profileSource: string
  availability: string
  reason: string
  source: SandboxSource | null
  head: SandboxHeadSummary | null
  preparingOperationId: string
}
