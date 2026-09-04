/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/policy.set.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the policy.set JSON-RPC method (ADR-0020 §7 as amended 2026-08-16): what a MATRIX write did to the ONE global agent policy, and what it did to the work already running. It used to answer `{ok: true}`, which was a constant dressed as a fact — an error response already says the policy was not accepted. It says `applied` instead, because a matrix write can now legitimately land nothing and answer with a question: a run's authority is minted when the run starts and is immutable for the run (ADR-0020 decision 5), so a row moved here does not reach a run already deciding under it, and that used to happen in silence (nocx-4yjwk.8). The four run fields are the same four, with the same names and the same meanings, that policy.setRule and policy.forgetRule answer with — one question about the work already running, one vocabulary.
 */
export interface PolicySet {
  /**
   * Whether the matrix was persisted. False ONLY in the default "ask" timing with live runs left behind: nothing changed, and the person has a question to answer first. A write that had already landed could only be reported, and a person told "2 runs are still deciding under the row you just moved" has been handed a fact instead of a choice.
   */
  applied: boolean
  /**
   * How many runs already in flight would go on deciding under the OLD rows. Counted by minting each live run's authority AGAIN from the document this write leaves behind and comparing it, row by row, with the one the run holds — so a run whose session overlay already answers that effect, whose fence had already refused it, or that was minted after somebody else made the same change is correctly counted out, as is a write that states what the rows already say. It is exact, and being exact does not make it small: a row governs every call that classifies as its effect, so a write that really moves one really does reach every live run whose authority still states the old row. It is a count at the moment of the call, never a promise about the next one.
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
