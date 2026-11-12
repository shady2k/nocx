/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/block.outputStart.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the session-scoped block.outputStart notification. `row` is the current backend-owned screen anchor. A running block may receive refreshed anchors when output rewrites rows above its original start. `fromRow` remains the block's original first output row in the ordered rows stream, the coordinate `from` uses in ledger.blockRows; refreshing the screen anchor does not start or complete a block.
 */
export interface BlockOutputStart {
  entryId: string
  row: number
  fromRow: number
  screenRevision: number
  bufferKind: 'normal' | 'alternate'
  bufferInstance: string
  cols: number
  rows: number
}
