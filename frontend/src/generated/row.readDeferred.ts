/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/row.readDeferred.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Data of the internal -32014 refusal from a paged stored-rows read (ADR-y45sp41c, refining ADR-8eej3e66 §7 for this one retry). The connection had no read slot free, so it neither queued the request without bound nor closed the socket: it answered at once, and the renderer's own priority queue waits this long before sending the most urgent request it holds at that moment. A person never meets this answer; a client that does is expected to retry, not to report a busy state.
 */
export interface RowReadDeferred {
  /**
   * How long to wait before offering another stored-row read.
   */
  retryAfterMs: number
}
