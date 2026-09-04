/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.file.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of skills.file — one file of one discovered skill, as a person reads it. It answers for any provenance, builtin included: reading is not writing, and the person may read what the assistant reads. Two refusals arrive HERE rather than as a JSON-RPC error, because they are true sentences about a file that exists and a viewer needs the file's own name, provenance and the budget to say them: the file is not text, and the file is larger than the read budget. A file that is gone, a path that leaves the skill, and a name no root holds are errors instead — there is no subject to describe, so every field of a result would be an invention.
 */
export interface SkillsFile {
  /**
   * The skill as it was RESOLVED, from the frontmatter and by root precedence — not the string that was asked for.
   */
  name: string
  /**
   * The file as it was resolved, slash-separated and relative to the skill's own directory.
   */
  path: string
  /**
   * Which root supplied the bytes. Provenance is the root and never a field in the file, so it cannot be forged by whatever wrote the file.
   */
  provenance: 'authored' | 'builtin' | 'managed' | 'installed'
  /**
   * The file verbatim, frontmatter included: a person reading SKILL.md is reading what is on disk, not the body the assistant is given. Empty whenever refusal is set, because half a refused file is neither the file nor a refusal.
   */
  text: string
  /**
   * Why the bytes are not shown. Empty means nothing was refused and text is the file. Never null: the closed set is the whole vocabulary a viewer needs.
   */
  refusal: '' | 'not-text' | 'too-large'
  /**
   * The read budget, in bytes, that a too-large refusal was measured against. It travels so the viewer's sentence can name the limit rather than keeping a second copy of the number.
   */
  maxBytes: number
}
