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
   * Where an installed skill came from, as skills.json recorded it at install time. ABSENT unless a source is recorded: never for authored, builtin or managed skills, and not for a directory somebody moved into the installed root by hand — so its presence answers where the bytes came from and never what provenance the skill has. Inlined rather than named, for the reason every finding here is: a named $def becomes a second generated export nothing consumes.
   */
  source?: {
    url: string
    installedAt: string
  }
}
