/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/operation.rowDone.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The operation.rowDone JSON-RPC notification: the row operation.row started goes away because the work it reported has settled (ADR-8eej3e66 §8). It leaves no finished entry, because only transfers have an outcome worth keeping — a wait row exists only while it means something. Sent once per request that ever showed a row, so an `open` that installed nothing costs the connection no frame at all. It is droppable like operation.row, and a renderer that missed it loses the row only until the request it names settles, which is the same moment by construction.
 */
export interface OperationRowDone {
  /**
   * The JSON-RPC id of the call whose row this removes — the same value operation.row carried, so the two are one row's start and end and a renderer needs no second key.
   */
  requestId: number | string
}
