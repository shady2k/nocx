/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.runResolved.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.runResolved RPC (nocx-tjppv, design §4.1): the renderer's answer to an agent.runRequest — a closed outcome. outcome=completed carries the run body: the entry id the command was accepted under (minted at submit by the ordinary path), the exit status of the completed block (null when it froze without one — an entered environment; status names it honestly), the block's output line count and the span of the window actually returned with its text (a long output is clamped, never truncated silently — total says how much more the block holds). outcome=failed carries why, so a renderer that cannot submit (a session it does not know, a lane not prompt-ready) answers honestly instead of hanging the run. requestId is the broker-minted id from the request, echoed back.
 */
export type AgentRunResolved = {
  [k: string]: unknown
} & {
  /**
   * The request id from agent.runRequest.
   */
  requestId: string
  /**
   * completed: the command completed and the run body follows. failed: the submission could not be made or completed and error names why.
   */
  outcome: 'completed' | 'failed'
  /**
   * The failure sentence, present only when outcome is failed.
   */
  error?: string
  /**
   * The entry id the command was accepted under, minted at submit by the ordinary path.
   */
  entryId?: string
  /**
   * The exit status of the completed block; null when it froze without one (an entered environment).
   */
  exitCode?: number | null
  /**
   * The block's own frozen status vocabulary.
   */
  status?: 'success' | 'failure' | 'entered' | 'unknown'
  /**
   * The block's output line count — how much output the command produced in total.
   */
  total?: number
  /**
   * First line of the returned window, inclusive.
   */
  start?: number
  /**
   * Last line of the returned window, exclusive; within [0, total].
   */
  end?: number
  /**
   * The returned window's text.
   */
  text?: string
}
