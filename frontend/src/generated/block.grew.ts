/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/block.grew.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the block.grew server-to-client notification (nocx-2v80t.3.7): rows arrived from the helper and are now IN a command's block in history — appended, capped, and durable, not merely painted. It is how an attached client follows a running command's block as it grows without re-asking ledger.get; a client that arrives later reads history and never needs it. Sent to the session's subscriber only, after the store's append commits; a block the keep decision refused sends nothing, because nothing grows.
 */
export interface BlockGrew {
  /**
   * The block's entry — the same id ledger.get and ledger.artifact answer for. The command's authenticated start bound it; for a lifecycle attempt it IS the attempt id.
   */
  entryId: string
  /**
   * The absolute row index the delivery started at — rows ever departed in the session, the same coordinate a stored line's `from` carries (ledger.blockRows.schema.json).
   */
  from: number
  /**
   * How many rows this delivery appended. At least one: a delivery that carried no rows is not an event, and a block that is done growing is said so by block.closed, not by a zero here.
   */
  count: number
}
