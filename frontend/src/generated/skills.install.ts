/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.install.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of skills.install — the skill is on disk, and its digest and its source were recorded in the same operation that wrote it. A refusal comes back as a JSON-RPC error naming the step that refused, and leaves nothing behind: an installed skill with no record would be listed as changed and never offered to the assistant.
 */
export interface SkillsInstall {
  /**
   * The skill's name, taken from the document's frontmatter. It is the name the row in the list now carries.
   */
  name: string
  /**
   * Always "installed". Provenance is the root a skill sits in, never a field in its file, and the only root this method writes to is the installed one — so this is a constant rather than a choice, and it travels so the row's kind badge comes from the same answer every other row's does.
   */
  provenance: 'installed'
}
