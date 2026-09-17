/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agentAccess.forget.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of agentAccess.forget: the answer is no longer remembered, so the next time that agent is started nocx asks again. Nothing reaches into a pane already running: the admit check reads the store on every call, so an agent whose answer is gone has its next tool call refused and goes on running without nocx's tools — the same state a denial produces. There is therefore no run-timing choice here, unlike policy.forgetRule, because there is no work that could be left deciding under an answer that no longer exists.
 */
export interface AgentAccessForget {
  /**
   * True when an answer was there and is now gone. False is a SUCCESS: the three facts named no remembered answer, which is the state the person asked for. Raising would turn a second click — or a page whose read predates somebody else's forget — into an error about what they wanted.
   */
  forgotten: boolean
}
