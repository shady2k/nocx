/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.access.page.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */
import type { DiagnosticRecord } from './sandbox.access.record'

export interface DiagnosticPage {
  observer: 'unavailable' | 'active' | 'unsupported' | 'failed'
  revision: number
  dropped: number
  discontinuity: boolean
  total: number
  nextCursor: number
  /**
   * @maxItems 200
   */
  records: DiagnosticRecord[]
}
