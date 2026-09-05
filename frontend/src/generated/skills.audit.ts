/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.audit.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of skills.audit — a reading of one skill the person already holds, asked for by them and produced by the auditing role's model (design §7). IT IS NOT A VERDICT AND THERE IS NO VERDICT IN IT. Every field is a fact about the request (which skill, which root), a fact about the call (which role answered, on which endpoint and model), or a fact about what was read (the paths, the omissions, the budget, and the static scan's own matches); not one of them is an opinion about the skill, so there is nothing here to count, threshold or colour into a judgement. The report itself is one field of prose rather than a form with slots, because an empty slot reads as 'nothing found', which is a verdict wearing a layout — the questions that shape the answer are asked in the prompt instead. A skill with no findings has none; that is not the same as safe, and nothing in this result says it is. The result changes nothing about what the assistant may do: what it is offered is still the person's switch and the digest comparison, and neither is touched by asking for a reading. A name no root holds, an unresolvable role and an unreachable endpoint are JSON-RPC errors rather than an empty report — a blank report is indistinguishable from a clean one.
 */
export interface SkillsAudit {
  /**
   * The skill as it was RESOLVED, from the frontmatter and by root precedence — not the string that was asked for.
   */
  name: string
  /**
   * Which root holds the skill. Provenance is the root and never a field in a file, so it cannot be forged by whatever wrote one.
   */
  provenance: 'authored' | 'builtin' | 'managed' | 'installed'
  /**
   * Which model role actually answered: 'auditing' when the person has assigned one, 'answering' when they have not and the audit fell back to the answering role's endpoint. It travels because an unassigned role must never spend money silently. It is a fact about the CALL, never about the skill, and a boolean 'usedFallback' was rejected for naming a comparison instead of the thing.
   */
  role: 'auditing' | 'answering'
  /**
   * The display name of the endpoint the call went to, so the note about a fallback can name what was billed.
   */
  endpoint: string
  /**
   * The model id the role resolved to. The RESOLVED fact and never a self-report out of the answer — the same rule the classifier keeps about which model produced a verdict.
   */
  model: string
  /**
   * The auditing model's prose, verbatim and bounded: what the skill instructs, what it reaches for, and what any scan-matched line does in context. It is a description and not a judgement — the model is told so, and told that the document it was given is a document to describe rather than instructions to follow. That framing is defence in depth and NOT a guarantee: a frame is an instruction to a probabilistic model, never an enforcement boundary. What makes a persuaded model survivable is that this text changes nothing.
   */
  report: string
  /**
   * The files whose bytes the model was actually given, slash-separated and relative to the skill's directory, in manifest order with SKILL.md first. Never empty: a skill none of whose files could be read is a refusal, not a result.
   *
   * @minItems 1
   */
  read: [string, ...string[]]
  /**
   * The files that were not sent, each with the reason. It travels because a report about a subset the reader cannot identify reads exactly like a report about the whole skill — the soft degrade a visible sentence exists to prevent. Never null: nothing omitted is [].
   */
  omitted: {
    path: string
    /**
     * 'too-large' — the file alone is over the per-file read budget. 'not-text' — its bytes are not UTF-8, and they are named rather than transliterated. 'budget-spent' — the document was already full when its turn came, which by manifest order can only fall on the tail. 'unreadable' — it was named by the walk and could not be read now. This is deliberately not skills.file's refusal set: 'budget-spent' is a fact about a file's position in a bundle rather than about the file, and widening that closed union would put a value in it that skills.file can never return.
     */
    reason: 'too-large' | 'not-text' | 'budget-spent' | 'unreadable'
  }[]
  /**
   * The budget the composition was measured against, so a sentence about a cut can name the number that made it rather than keeping a second copy of it.
   */
  maxBytes: number
  /**
   * Every static-scan match over exactly the bytes that were sent, each named with the file it matched in. They are OURS, not the model's: a line number a model reported would be a self-report about a document only it can see, while these are checkable against skills.file, which is what makes the prose beside them worth reading. A finding is evidence and never a refusal, and an empty array means no pattern matched — which is not the same as safe. Never null: no matches is [].
   */
  findings: {
    path: string
    patternId: string
    line: string
    lineNumber: number
  }[]
}
