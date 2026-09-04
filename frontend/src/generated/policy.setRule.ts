/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/policy.setRule.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the policy.setRule JSON-RPC method: ONE invocation rule was written into the ONE global agent policy — added, or replaced in place — and every other rule and all seven matrix rows are untouched. A gesture that is about one rule writes one object, so a save made against a document read a minute ago cannot delete a standing answer the approval prompt wrote in between (nocx-39bly).
 */
export interface PolicySetRule {
  /**
   * The stored rule's identity. Ids are minted by the backend and never supplied by the renderer for a NEW rule (AD-7), so this is where a caller that sent no id learns the one it was given — and what a later policy.setRule or policy.forgetRule names the rule by.
   */
  id: string
  /**
   * True when the rule was appended, false when it replaced the rule already wearing this id. A replacement keeps its position in the document, its creation time and where it came from.
   */
  added: boolean
}
