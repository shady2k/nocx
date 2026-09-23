/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/block.closed.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the block.closed server-to-client notification (nocx-2v80t.3.7): the command's interval ended — its end marker was authenticated, the last rows are in, and the block is sealed in history. It is the companion of block.grew and the moment an attached client can treat the block as whole: whatever a later read returns is final. It is NOT block.finished — that one raises a banner at the entry's close with the outcome; this one says the BLOCK's rows are complete, and the two are raised by different owners at different moments (the end marker and the completion race in both orders), so a renderer that conflated them would draw a running command's output as finished, or the reverse.
 */
export interface BlockClosed {
  /**
   * The block's entry — the same id ledger.get and ledger.artifact answer for.
   */
  entryId: string
}
