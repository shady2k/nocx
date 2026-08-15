/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.ask.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the agent.ask JSON-RPC method (nocx-f4s5, design §5, §7). The question text plus references to already-captured frames. The backend records the frame reference, the question and a PENDING run in ONE ledger transaction before the model would be called, and answers with the BACKEND-MINTED run id (what agent.cancel/approve/status will address) and the run's state, ON THE WIRE because the renderer draws it. The run is prepared at the response: recorded, and the backend then drives it to streaming and a terminal state asynchronously (agent.runDelta / agent.runState notifications). The same ask retried with the same askId answers the original run id — never a second run (a retry duplicates neither question nor run).
 */
export interface AgentAsk {
  /**
   * The backend-minted run id — the execution row in the one ledger. Opaque to the renderer; a later agent.cancel/agent.approve/agent.status addresses the run by this id.
   */
  runId: number
  /**
   * The question's entry id in the ledger — the identity the answer is joined to by a caused-by edge, and what the flow renders as the question block.
   */
  questionId: string
  /**
   * The ANSWER entry's id in the ledger — where the streamed deltas land (agent.runDelta's entryId). The renderer needs it BEFORE the first delta so a run that fails before any text still has an answer block to terminalize.
   */
  answerEntryId: string
  /**
   * The run's state, exactly as the renderer draws it (design §7). prepared → streaming → awaiting_approval → one of the terminal states; interrupted is what a run becomes when the backend restarts and finds it non-terminal (design §4.2). A reconnecting renderer reads the state; it never infers liveness from notifications having stopped.
   */
  state:
    | 'prepared'
    | 'streaming'
    | 'awaiting_approval'
    | 'completed'
    | 'cancelled'
    | 'failed'
    | 'interrupted'
  /**
   * The question entry's ingest_seq — commit order in the one ledger (ADR-0019 §2: the stable total order for paging, never causality).
   */
  ingestSeq: number
  /**
   * True when this ask was a replay of an earlier ask with the same askId — the response to the first attempt was lost, and this answers the ORIGINAL run id rather than creating a second run.
   */
  replayed: boolean
}
