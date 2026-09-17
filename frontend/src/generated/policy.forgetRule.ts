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
   * True when a rule wearing that id was there and is now gone. False is a SUCCESS, not a failure: an id naming no rule means the rule is already not there, which is what forgetting asked for, and raising would turn a double click — or a page whose read predates somebody else's forget — into an error about a state the person wanted. It is also false when `applied` is false, and the two are different states: one is "already as you wanted", the other is "you have a question to answer first".
   */
  removed: boolean
  /**
   * Whether the write landed in the store. False ONLY in the default "ask" timing with live runs left behind: nothing changed, and the person has a question to answer first. A write that had already landed could only be reported, and a person told "3 runs are still using the answer you just deleted" has been handed a fact instead of a choice.
   */
  applied: boolean
  /**
   * How many runs already in flight would go on deciding under the OLD answer. A run's grant is minted when the run starts and is immutable for the run (ADR-0020 decision 5), so a policy write never reaches one. This counts only the runs whose own grant would DECIDE DIFFERENTLY without the answer — not every live run, which would report six affected by an answer governing none of them and train a person to dismiss the question. It is a count at the moment of the call, never a promise about the next one.
   */
  affectedRuns: number
  /**
   * How many of those runs this call actually terminalized, through the path a person's own stop takes. Zero in every timing but "stop".
   */
  stoppedRuns: number
  /**
   * How many had already reached a terminal state by the time the stop got there. Not a failure and not counted as stopped: crediting this gesture with an ending it did not cause would make the sentence a person reads untrue.
   */
  finishedBeforeStop: number
}
