/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.recoveryStatus.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Metadata-only recovery status for one reclaimed session. It reports the recorded stream end offset and known missing ranges without reading or returning recorded byte chunks.
 */
export interface SessionRecoveryStatus {
  sessionId: string
  /**
   * End offset recorded for this session; combined with replayFrom to determine whether reclaim left an unrecorded interval.
   */
  produced: number
  /**
   * Known gaps in the recorded stream. Always an array; contains metadata only.
   */
  gaps: Gap[]
}
export interface Gap {
  /**
   * First dropped byte offset.
   */
  start: number
  /**
   * Offset just past the last dropped byte.
   */
  end: number
  /**
   * Why the range is missing.
   */
  reason: string
}
