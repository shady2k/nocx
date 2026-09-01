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
  provenance: 'authored' | 'builtin' | 'managed'
  path: string
  enabled: boolean
  status: 'approved' | 'changed'
}
