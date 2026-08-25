/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.runDelta.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.runDelta server-to-client notification (nocx-x8s2.2, design §7): one chunk of the streamed answer. runId AND entryId are BOTH on every delta because the renderer routes by entryId while two overlapping asks stream concurrently — 'the current answer' is not an identity, and cancel/close from one thread must not abort another's. seq ascends per run from 0 and is the renderer's ordering key; a reconnect reads the run's appended chunks from the ledger and orders by seq. blockId names the run of prose the chunk lands in (ADR-0040), which is a place in the ordered tree rather than an offset into a string.
 */
export interface AgentRunDelta {
  /**
   * The backend-minted run id the delta belongs to.
   */
  runId: number
  /**
   * The TURN the chunk appends to — agent.ask result's entryId (nocx-4em1z). The renderer appends here, never to 'the current answer'.
   */
  entryId: string
  /**
   * The PIECE this chunk appends to (ADR-0040): the `text` child of the turn the backend opened for this run of prose. entryId says which answer — the routing key — and this says where in it. The backend owns the boundary between two runs of prose: a block opens on the first delta after a tool call and is sealed when the next call arrives, so the renderer never works out where one piece ends. That is what retires the anchor it replaces — while the cut was the renderer's, the live path and the restore each computed it and could drift; a block id cannot be re-derived and cannot drift.
   */
  blockId: string
  /**
   * The chunk's order within its run, ascending from 0. The renderer's ordering key; a reconnect orders the ledger's appended chunks by it. Per RUN and not per block: a resumed run continues the numbering (nocx-igu4y), and one block's chunks therefore ascend without being contiguous.
   */
  seq: number
  /**
   * The chunk of answer text to append.
   */
  text: string
}
