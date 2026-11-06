/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/operation.outcome.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The operation.outcome JSON-RPC result: what the backend retained about one mutation identity. It answers the person who stopped waiting, and it never implies a rollback — a timeout is not evidence that the effect did not happen.
 */
export interface OperationOutcome {
  /**
   * The closed set of answers. 'not-started' — no work ran, so a retry cannot double an effect. 'done' — the work finished (it may have answered an error; it is not still running). 'outcome-unknown' — it has not finished, so the person is checking what happened and may ask again while the record is retained. 'unknown' — nothing is retained: the identity was never seen, its retention ran out, or the backend restarted.
   */
  outcome: 'not-started' | 'done' | 'outcome-unknown' | 'unknown'
  /**
   * The catalogue operation the record belongs to, or the empty string where no record exists. It is what a surface names when it says what to look at instead of an answer.
   */
  operation: string
}
