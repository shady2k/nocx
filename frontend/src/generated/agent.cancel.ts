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
 * Result of the agent.cancel JSON-RPC method (nocx-uvac6.2): the backend closes a live agent run after a person's stop request. The cancelled flag is true only when this request closed a non-terminal run; an already-terminal or unknown run is answered with an error. The terminal state is persisted before this result is answered, and the run's already-recorded prose remains in the ledger.
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
}
