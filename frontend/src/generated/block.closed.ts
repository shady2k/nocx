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
 * Params of the block.closed server-to-client notification (nocx-2v80t.3.7): the command's interval ended, and nothing more will be written to its block. When `kept` is true the last rows are in and the block is sealed in history: it is the companion of block.grew and the moment an attached client can treat the block as whole, and whatever a later read returns is final. It is NOT block.finished — that one raises a banner at the entry's close with the outcome; this one says the BLOCK's rows are complete, and the two are raised by different owners at different moments (the end marker and the completion race in both orders), so a renderer that conflated them would draw a running command's output as finished, or the reverse. It is sent for EVERY interval end the coordinator resolves, whether or not the store kept the block (nocx-2v80t.3.27): the renderer finishes a block the backend knows about on this notification, and it cannot know that none is coming, so a block the keep decision refused, or one no store could open, is said closed too — `kept` says which. The renderer closes a block on its own word in exactly two cases, both where no block.closed can ever reach it: a block never bound to an attempt, which the backend has no entry to name, and every block of a session the pane has let go of, whose sender is gone (frontend/src/scrollback/blocks.ts, abandonUnbound and settleWithoutBackend).
 */
export interface BlockClosed {
  /**
   * The block's entry — the same id ledger.get and ledger.artifact answer for.
   */
  entryId: string
  /**
   * Whether the store holds rows for this block. true means the store holds the block SEALED. false means there is nothing final to read for it — the keep decision refused it (output retention off, a sensitive entry, a critical environment), no store could open it, or the store refused its seal (nocx-2v80t.3.32: whatever rows reached it are not sealed, and are not claimed as the block's final output) — so a renderer has no rows to fetch and must not read the absence as a failure.
   */
  kept: boolean
}
