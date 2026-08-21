/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.readScreenRequest.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.readScreenRequest notification (nocx-ljfwz, design §2.4): the server asks the renderer to capture a session's screen. The renderer owns the grid (AD-6), so the frame is produced here, not in the backend. requestId is the broker-minted correlation id the renderer echoes back in agent.readScreenResolved; sessionId is the session whose screen is read — already narrowed by the run's grant before the request was sent; region, when present, is an absolute buffer row span [start, end); absent means the visible screen.
 */
export interface AgentReadScreenRequest {
  /**
   * The broker-minted request id; echoed back in the resolution.
   */
  requestId: string
  /**
   * The session whose screen is read (inside the run's grant).
   */
  sessionId: string
  /**
   * Absolute buffer row span [start, end). Absent: the visible screen.
   */
  region?: {
    /**
     * First buffer row of the region, inclusive.
     */
    start: number
    /**
     * Last buffer row of the region, exclusive. Must be greater than start.
     */
    end: number
  }
}
