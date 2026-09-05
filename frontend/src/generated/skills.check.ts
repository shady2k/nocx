/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.check.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of skills.check — what content.db holds about one skill, and whether that reading is still about the bytes on disk. It spends nothing: no model role is resolved and none is called, whatever the answer. 'Nobody has checked this' (checked:false) is a RESULT and not an error, for the same reason a file too large to show is one on skills.file: it is a true sentence about a thing that exists, and a caller told apart from a broken store only by reading an error string will get it wrong. checked:false also covers a builtin skill (never auditable, so it can never have a row) and a store that is absent or a stub — three different reasons collapsed into one screen state on purpose, because all three are one fact from the person's side: there is nothing to show here. A STALE CHECK IS STILL THE CHECK — when the recomputed material digest no longer matches the one the check was filed under, checked stays true, check carries the whole stored reading unaltered, and only current moves to false; hiding a stale check would throw away something a person paid a model for, and would make 'no check' and 'an old check' the same state on screen.
 */
export type SkillsCheck = {
  [k: string]: unknown
} & {
  /**
   * The skill as it was RESOLVED, from the frontmatter and by root precedence — not the string that was asked for. The same resolution skills.audit reports under the same field.
   */
  name: string
  /**
   * Whether content.db holds a reading for this skill. false means nobody has checked it, or the store has none to give (no store on this machine, a stub, or the skill is a builtin) — never an error.
   */
  checked: boolean
  /**
   * The stored reading, whole, present exactly when checked is true — including when it is stale. Never trimmed or hidden for staleness: only `current` says that.
   */
  check?: {
    /**
     * Which root held the skill at the time it was checked.
     */
    provenance: 'authored' | 'builtin' | 'managed' | 'installed'
    /**
     * What the auditing model concluded, in the closed vocabulary skills.audit's verdict enforces. It decided nothing then and decides nothing now.
     */
    verdict: 'clear' | 'suspect'
    /**
     * The auditing model's prose, verbatim, exactly as skills.audit returned it when the check was made.
     */
    report: string
    /**
     * Which model role answered when this check was made: 'auditing', or 'answering' when the check fell back to it.
     */
    role: 'auditing' | 'answering'
    /**
     * The display name of the endpoint the check's model call went to.
     */
    endpoint: string
    /**
     * The model id that answered when this check was made.
     */
    model: string
    /**
     * The hex sha256 of the document the model was given when this check was made (skill.AuditMaterial.Digest). It is compared against a fresh recomposition to produce `current` — this field itself never changes once written; only the comparison's answer does.
     */
    digest: string
    /**
     * Unix millis, backend wall clock, when the check was made.
     */
    checkedAt: number
    /**
     * The files whose bytes the model was given, as skills.audit recorded them when the check was made.
     */
    read: string[]
    /**
     * The files that were not sent, each with the reason, as skills.audit recorded them when the check was made. Never null: nothing omitted is [].
     */
    omitted: {
      path: string
      reason: 'too-large' | 'not-text' | 'budget-spent' | 'unreadable'
    }[]
    /**
     * The static scan's matches over exactly the bytes that were sent, as skills.audit recorded them when the check was made. Never null: no matches is [].
     */
    findings: {
      path: string
      patternId: string
      line: string
      lineNumber: number
    }[]
    /**
     * The budget the composition was measured against when this check was made.
     */
    maxBytes: number
  }
  /**
   * Whether the stored check's digest still matches a fresh recomposition of the skill's bytes — present exactly when checked is true. true: the check is about the bytes on disk right now. false: something moved since the check was made — an edit, a reinstall — and the check is still returned, unaltered, because it is still evidence about an earlier version of this skill.
   */
  current?: boolean
}
