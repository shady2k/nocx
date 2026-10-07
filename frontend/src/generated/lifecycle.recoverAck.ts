/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/lifecycle.recoverAck.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The lifecycle.recoverAck result for the composite decision-8 acknowledgement. The request names only session identity and the non-secret recovery episodeId, after both marker sighting and conventional presentation. The backend accepts only the exact live episode after its durable state is sighted, and permits only Lost → Native. The result is a single ok: true.
 */
export interface LifecycleRecoverAck {
  /**
   * true when the acknowledgement was accepted (the lane fell Lost → Native) or was idempotently already accepted. Rejections arrive as JSON-RPC errors: an unknown or closed session, a generation with no pending episode, or a lane that is no longer Lost.
   */
  ok: true
}
