/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.cancel.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the agent.cancel JSON-RPC method (nocx-uvac6.2): the backend closes a live agent run after a person's stop request. Before terminalization it synchronously stops only assistant-run child executions registered to that exact run; a user-started or summoned host command has no registration and is never changed. The cancelled flag is true only when this request closed a non-terminal run; an already-terminal or unknown run is answered with an error. The terminal state is persisted before this result is answered, and the run's already-recorded prose remains in the ledger. The optional presentation fields mirror agent.runState so this reserved response can close the renderer even when that notification is dropped.
 */
export interface AgentCancel {
  /**
   * The backend-minted run id that was stopped.
   */
  runId: number
  /**
   * The run state after the stop. A successful cancellation is the cancelled terminal state.
   */
  state: 'cancelled'
  /**
   * True because this request stopped the live run.
   */
  cancelled: true
  /**
   * Additional terminal presentation detail, present only when cancellation could not stop an owned assistant-run child (for example, the host has no local process group to signal). A user-started host command is outside agent.cancel and never produces this field.
   */
  error?: string
  /**
   * The authoritative whole-run count of streamed chunks refused by the wire. Present only when non-zero so the reserved response preserves the same visible gap marker as agent.runState.
   */
  droppedDeltas?: number
}
