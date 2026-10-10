/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.intent.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Outcome of a structured interactive intent; refusal is explicit and reports no bytes written.
 */
export interface SessionIntentResult {
  state:
    'executed' | 'refused' | 'failed_partial' | 'delivery_unknown' | 'cancelled' | 'in_progress'
  bytesWritten: number
  fenceAfter: number
  /**
   * The epoch this pane's session was in when the answer was decided (nocx-zg3k3.3.1). A controller presents it with its next intent, and a refusal naming a DIFFERENT value than the one it presented is the signal to re-present the same intent — nothing was written, so the retry is the same write. Absent only when no session decided the answer (a refusal reached before the session was consulted: busy, closing).
   */
  accessEpoch?: number
  retryAfterMs?: number
  refusal?: {
    cause:
      | 'stale_target'
      | 'incomparable'
      | 'expired'
      | 'forged'
      | 'token_spent'
      | 'snapshot_gone'
      | 'completeness_unknown'
      | 'cannot_encode'
      | 'would_submit'
      | 'access_revoked'
      | 'no_read_barrier'
      | 'commit_deadline'
      | 'capacity'
      | 'busy'
      | 'closing'
      | 'refused'
    regionNow?: string
    regionTruncated?: boolean
    regionOmitted?: boolean
  }
}
