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
 * Result of the agent.steer JSON-RPC method: the run the message was about, and what became of it. A message typed while a turn runs either goes INTO that turn — the agent takes it before its next model call — or becomes the conversation's next question, because the connector says its agent cannot steer, because the turn ended first, was cancelled, or the agent did not accept it in time. The block draws the two differently, so the outcome is stated rather than derived. The declines a message applies to a pending approval happen either way, before this answer.
 */
export interface AgentSteer {
  /**
   * The run the message was about.
   */
  runId: number
  /**
   * steered: the agent took the message inside the running turn. next_question: the turn could not take it, so the message is kept and asked once the current turn ends, in a block of its own.
   */
  outcome: 'steered' | 'next_question'
}
