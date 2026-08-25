/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.runState.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.runState server-to-client notification (nocx-x8s2.2, design §7): the run's terminal state. A renderer that reconnects mid-answer reads the run's state and its appended chunks from the ledger; it never infers liveness from notifications having stopped. error is present ONLY for failed, and it is a sentence a person reads — never a Go error string. droppedDeltas is present ONLY when the live view is incomplete: the wire refused one or more agent.runDelta frames (outbound's deliberate non-blocking overflow), so the block must not be read as a complete answer. The durable answer is whole either way — every chunk was persisted before the notify — so the marker is a live-view bound, never a terminal-state change (nocx-dw3.1).
 */
export interface AgentRunState {
  /**
   * The backend-minted run id whose state changed.
   */
  runId: number
  /**
   * The run's state, exactly as the renderer draws it (design §7). This notification carries a terminal state: completed | cancelled | failed | interrupted.
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
   * The reason, present only for failed: a sentence a person reads ('the endpoint's credential is unavailable — unlock the vault'), never a Go error string.
   */
  error?: string
  /**
   * How many streamed chunks the wire refused for this run (a full outbound queue took outbound's overflow path). Present only when non-zero: the renderer marks the block's gap instead of reading it as a complete answer. The ledger holds the whole answer regardless.
   */
  droppedDeltas?: number
}
