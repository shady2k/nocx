/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.standingAnswerSaved.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.standingAnswerSaved server-to-client notification (nocx-2019q): a person answered an approval question with a width, and the standing half of that answer was WRITTEN. It is sent only after the write succeeded — a save that failed is reported by agent.approve's own warning and never by this, so a receipt on screen can never claim a rule that is not in the store. It is a fact about the RUN, routed exactly as agent.runDelta and agent.runToolCall are, so the receipt lands in the turn where the question was asked rather than wherever a person happens to be looking. The renderer says what was saved in the WORDS OF THE BUTTON that was clicked, which is why this carries the same three facts the question carried — the direction, the width and the binding — rather than a sentence: the sentence has one owner, and it is the surface that offered the answer.
 */
export interface AgentStandingAnswerSaved {
  /**
   * The backend-minted run id the answer was given on — the same value agent.approvalRequested carried and agent.approve echoed.
   */
  runId: string
  /**
   * The ledger entry of the TURN that asked, so a notification that has outlived its block is dropped rather than drawn on the wrong one — the same guard the deltas use, and the same constraint they carry. NEVER EMPTY, and the chain that produces a receipt has no branch where it could be: a receipt exists only where agent.approve found the run still pending; that set is filled by agent.ask alone, after the ledger transaction that recorded the turn answered with this very id (agent.ask's own entryId, also minLength 1); and agent.ask is refused outright when no content store is wired. The field once permitted an empty string, and that permission is what hid nocx-2019q: a receipt the renderer cannot route is a receipt nobody can ever see, so no schema check could object to the backend sending the wrong entry, or none. The backend now REFUSES to send a receipt without it and reports that on agent.approve's warning instead, where a person can read it.
   */
  entryId: string
  /**
   * The direction of the answer that was saved: true is a permit, false a refusal. Both are standing answers and both get a receipt — a person who said 'never ask me to run this again' configured something just as much as one who said always.
   */
  approved: boolean
  /**
   * How far the saved answer reaches. Only the two widths that WRITE appear here: 'once' saves nothing and produces no receipt, and 'expand' is the widening answer, which edits a row's scopes rather than saving a standing answer.
   */
  scope: 'session' | 'always'
  /**
   * The canonical invocation the answer covers, in the same spelling agent.approvalRequested's standing.rule used — so the receipt reads as the button read. Empty for a non-command proposal, whose answer covers the effect row named below.
   */
  rule: string
  /**
   * The effect class the question was decided under — the row a non-command answer wrote, and the class the renderer names it by. Sent for the same reason agent.approvalRequested sends it: the renderer must never derive an effect from a tool name (ADR-0028 decision 4).
   */
  effect:
    | 'observe'
    | 'mutate-reversible'
    | 'mutate-destructive'
    | 'privilege-change'
    | 'disclose'
    | 'cross-boundary'
    | 'delegate'
  /**
   * The stored rule's id, minted by the store that holds it (AD-7), and the whole of what makes the receipt's Undo exact: it forgets THAT rule through policy.forgetRule and can never discard an answer given between the save and the undo. Empty when the answer wrote something no id addresses — a session overlay, which dies with its session, or a matrix row, which is edited rather than removed — and then the receipt offers no Undo, because there is nothing it could name.
   */
  ruleId: string
}
