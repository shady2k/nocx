/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.access.record.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */
import type { DiagnosticProposal } from './sandbox.access.proposal'

export interface DiagnosticRecord {
  id: string
  revision: number
  executable: string
  path: string
  operation: string
  access: 'read' | 'write' | 'unknown'
  pathKnown: boolean
  source: 'linux-seccomp' | 'macos-seatbelt'
  precision: 'attempted' | 'reported-denial' | 'unknown'
  prediction: 'denied' | 'allowed' | 'unknown'
  count: number
  state: 'unresolved' | 'pending' | 'dismissed' | 'future-policy' | 'uncertain'
  futureRevision: number
  proposal: DiagnosticProposal | null
}
