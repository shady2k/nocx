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
 * Params of the agent.runDelta server-to-client notification (nocx-x8s2.2, design §7): one chunk of the streamed answer. runId AND entryId are BOTH on every delta because the renderer routes by entryId while two overlapping asks stream concurrently — 'the current answer' is not an identity, and cancel/close from one thread must not abort another's. seq ascends per run from 0 and is the renderer's ordering key; a reconnect reads the run's appended chunks from the ledger and orders by seq.
 */
export interface AgentRunDelta {
  /**
   * The backend-minted run id the delta belongs to.
   */
  runId: number
  /**
   * The answer entry the chunk appends to — agent.ask result's answerEntryId. The renderer appends here, never to 'the current answer'.
   */
  entryId: string
  /**
   * The chunk's order within its run, ascending from 0. The renderer's ordering key; a reconnect orders the ledger's appended chunks by it.
   */
  seq: number
  /**
   * The chunk of answer text to append.
   */
  text: string
}
