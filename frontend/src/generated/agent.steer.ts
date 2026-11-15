/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.steer.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the agent.steer JSON-RPC method: the person's message was handed to the agent answering the run. The answer waits for the agent's own acceptance of the message, so a message the turn ended under is refused with an error rather than reported delivered. steer is a capability, not a promise: an agent whose protocol has no steering refuses the message and says so.
 */
export interface AgentSteer {
  /**
   * The run that took the message.
   */
  runId: number
  /**
   * True because the agent accepted the message into the running turn.
   */
  steered: true
}
