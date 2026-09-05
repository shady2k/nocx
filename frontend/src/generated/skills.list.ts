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
}
