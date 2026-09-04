/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/policy.forgetRule.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the policy.forgetRule JSON-RPC method: ONE invocation rule was removed from the ONE global agent policy by id, leaving every other rule and all seven matrix rows as they were.
 */
export interface PolicyForgetRule {
  /**
   * True when a rule wearing that id was there and is now gone. False is a SUCCESS, not a failure: an id naming no rule means the rule is already not there, which is what forgetting asked for, and raising would turn a double click — or a page whose read predates somebody else's forget — into an error about a state the person wanted.
   */
  removed: boolean
}
