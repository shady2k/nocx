/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.outputGap.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params for the session.outputGap notification. It states a missing half-open range in the session output byte coordinate; it does not carry terminal bytes or reset terminal state.
 */
export interface SessionOutputGap {
  /**
   * The server-minted session id whose live output stream crossed a gap.
   */
  sessionId: string
  /**
   * The first missing byte offset.
   */
  start: number
  /**
   * The first byte offset after the missing range.
   */
  end: number
  /**
   * The reason recorded for the gap. Unknown values must be reported without guessing.
   */
  reason: string
}
