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
 * Params of the agent.runState server-to-client notification (nocx-x8s2.2, design §7): the run's state. A renderer that reconnects mid-answer reads the run's state and its appended chunks from the ledger; it never infers liveness from notifications having stopped. error is present for failed runs and for cancelled runs only when cancellation has additional turn-specific detail; it is a sentence a person reads — never a Go error string. Cancelling a turn stops only assistant-run child executions registered to that exact run. A user-started or summoned host command is not registered, is never reported here, and is never changed. droppedDeltas is the authoritative whole-run count and is present only when the live view is incomplete. unarmedBounds is present only when shell integration was unavailable; it names bounds that did not apply while the run still executed.
 */
export interface AgentRunState {
  /**
   * The backend-minted run id whose state changed.
   */
  runId: number
  /**
   * The run's state, exactly as the renderer draws it (design §7). Terminal settlement uses completed | cancelled | failed | interrupted; prepared, streaming and awaiting_approval are non-terminal lifecycle states.
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
   * The reason, present for failed runs and only when a cancelled turn has additional turn-specific detail, such as inability to signal an owned assistant-run child. A user-started host command is outside agent.cancel and never appears here.
   */
  error?: string
  /**
   * The authoritative whole-run count of streamed chunks the wire refused. Present only when non-zero: the renderer marks the block's gap instead of reading it as a complete answer. The ledger holds the whole answer regardless.
   */
  droppedDeltas?: number
  /**
   * Human-readable sentences the RUN states about ITSELF — today, only that it stopped asking the person to widen a scope because it reached the bound on how often one answer may ask (design §5.3). Present only when there is one; a silent stop would be a soft degrade, and a notice on every run is a notice nobody reads.
   *
   * @minItems 1
   */
  notices?: [string, ...string[]]
  /**
   * Human-readable sentences naming lease bounds that could not be armed because shell integration was unavailable. Present only when at least one bound did not apply; never null or empty.
   *
   * @minItems 1
   */
  unarmedBounds?: [string, ...string[]]
}
