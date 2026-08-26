/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.runRequest.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.runRequest notification (nocx-tjppv, design §4.1): the server asks the renderer to run a command through the same submit path a person uses. The backend never writes to the PTY (design §2.1) — the renderer submits through its ordinary orchestration (block, ledger entry, attempt, output artifact), waits for the completion, and answers agent.runResolved. requestId is the broker-minted correlation id the renderer echoes back in agent.runResolved; sessionId is the lane the command runs in — already narrowed by the run's grant before the request was sent; command is exactly what a person would type.
 */
export interface AgentRunRequest {
  /**
   * The broker-minted request id; echoed back in the resolution.
   */
  requestId: string
  /**
   * The session (the lane) the command runs in, inside the run's grant.
   */
  sessionId: string
  /**
   * The command to submit, exactly as a person would type it.
   */
  command: string
}
