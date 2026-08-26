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
 * Params of the agent.approve request (nocx-z9hj4, design §7.2): the person's decision on one exact proposal. Carries the FULL binding — run, attempt, tool, call id and the canonical-argument hash — so a yes names the exact proposal the notification showed and nothing else, plus the decision and how far it reaches. The scope travels with the answer because the BACKEND applies it: a renderer that read the matrix, edited a row and wrote it back would be a second owner of the policy document, racing the settings page.
 */
export interface AgentApprove {
  /**
   * The backend-minted run id — echoed from agent.approvalRequested.
   */
  runId: string
  /**
   * The run's attempt — echoed from agent.approvalRequested.
   */
  attempt: number
  /**
   * The tool name — echoed from agent.approvalRequested.
   */
  tool: string
  /**
   * The model's call id — echoed from agent.approvalRequested.
   */
  callId: string
  /**
   * The canonical-argument hash — echoed from agent.approvalRequested. A changed argument hashes differently and does not resume under this approval.
   */
  argHash: string
  /**
   * The person's decision: yes resumes the run as a new attempt of the same entry; no terminalizes it with agent-declined.
   */
  approved: boolean
  /**
   * How far the answer reaches: this proposal only, every call of the same effect in this terminal session, or the standing policy. 'session' and 'always' are refused for an egress question — 'always send secrets to the provider' is not a standing decision.
   */
  scope: 'once' | 'session' | 'always'
}
