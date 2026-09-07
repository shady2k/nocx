/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.list.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The discovered skills and their person-controlled state.
 */
export interface SkillsList {
  skills: Skill[]
  /**
   * Every skill-shaped directory discovery would not index — a THIRD thing a row can be, and not a kind of skill: nothing here can be switched on and none of it reaches the assistant. It exists because a person who puts a SKILL.md on disk and cannot find it in the product had nothing to read but a log line they would never see (nocx-j0lei), which is the soft degrade AGENTS.md refuses. A directory with no SKILL.md is NOT here: the roots hold ordinary folders, and a row for each would teach people to ignore the list. Required and never absent — an empty array and a missing one would be two ways to say nothing was refused.
   */
  refused: Refusal[]
  documentPath: string
  documentError?: string
}
export interface Skill {
  name: string
  description: string
  provenance: 'authored' | 'builtin' | 'installed' | 'managed'
  path: string
  enabled: boolean
  status: 'approved' | 'changed'
  /**
   * What an installed skill was RESOLVED FROM, as skills.json recorded it at install time — the address, when it was taken, and the digest of what that address served. ABSENT unless a source is recorded: never for authored, builtin or managed skills, and not for a directory somebody moved into the installed root by hand — so its presence answers where the bytes came from and never what provenance the skill has. It is the RESULT and never the ROUTE: the search, the page the model read and the links it followed are deliberately not recorded anywhere, because an agent's route is not reproducible and could only be the model's own assertion (internal/skill/store_doc.go says this at length). Inlined rather than named, for the reason every finding here is: a named $def becomes a second generated export nothing consumes.
   */
  source?: {
    url: string
    installedAt: string
    /**
     * The sha256 over the whole bundle AS SERVED — the value the approval question showed and the value the install's second fetch had to match. It is NOT the digest change detection compares the disk against: that one is the hash of the adopted directory and moves when a person approves their own edits, while this one records what the address gave and never moves. Change detection and never provenance: bytes a stranger served hash to this, and nobody has vouched for them. Optional, because a source row recorded before this field existed has none, and an absent digest means nothing was recorded rather than that nothing matched.
     */
    digest?: string
  }
  /**
   * The row's summary of a stored skills.audit reading — a date, a verdict and a model, exactly what a person can act on regardless of what the bytes are now. ABSENT, never an empty object, for a skill nobody has checked: an empty object would render as a row saying something about a check that does not exist. Deliberately carries no currency flag — whether the bytes still match what was checked is skills.check's answer, computed once when a card opens, because a digest recomputation on this hot path would put a bundle walk behind every toggle, delete and approve that refreshes this list.
   */
  check?: {
    /**
     * RFC3339, when the check was made.
     */
    at: string
    verdict: 'clear' | 'suspect'
    model: string
  }
}
export interface Refusal {
  /**
   * The folder's name in its root — what the person sees in a file manager. It is NOT the frontmatter's name: half these refusals are refusals of that name, and the rest may have none to report.
   */
  directory: string
  provenance: 'authored' | 'builtin' | 'installed' | 'managed'
  /**
   * The SKILL.md that was refused — the file to open and fix.
   */
  path: string
  /**
   * The closed vocabulary a surface keys on. Closed for the reason every other vocabulary on this wire is: an unrecognised value must be a failure rather than a row rendered with a blank explanation.
   */
  reason: 'symlink' | 'unreadable' | 'frontmatter' | 'name' | 'noDescription' | 'descriptionTooLong'
  /**
   * The sentence a person reads, with the number in it where there is one. nocx writes it, so it is the product's words rather than an error string from a library: a refusal is not a stack trace.
   */
  detail: string
}
