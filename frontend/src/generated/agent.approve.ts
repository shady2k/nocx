/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.approve.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of agent.approve: the state reached after recording the person's decision. The run's eventual outcome arrives through agent.runState.
 */
export interface AgentApprove {
  /**
   * Streaming when a yes or policy decline resumes the run; failed when an egress decline or a refused resume terminalizes it.
   */
  state: 'streaming' | 'failed'
  /**
   * Consumer-visible sentence when the standing answer could not be recorded or its receipt could not be delivered. The decision on this proposal still stood. Omitted when there is no warning.
   */
  warning?: string
}
