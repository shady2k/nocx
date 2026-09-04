/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.files.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of skills.files — every file one discovered skill carries, as they are on disk now. It is what the person's card lists so they can see what a skill is made of, scripts included, before turning it on (design §8). the install question's own file list names the same shape BEFORE an install and cannot serve here: that is what a document said it would fetch, and it says nothing at all about a skill nobody installed from a URL. It answers for any provenance and for a skill that is switched OFF, because a skill that is off is exactly the one this exists for. A name no root holds is a JSON-RPC error: there is nothing to describe, so every field of a result would be an invention.
 */
export interface SkillsFiles {
  /**
   * The skill as it was RESOLVED, from the frontmatter and by root precedence — not the string that was asked for.
   */
  name: string
  /**
   * Which root holds the skill. Provenance is the root and never a field in a file, so it cannot be forged by whatever wrote one.
   */
  provenance: 'authored' | 'builtin' | 'managed' | 'installed'
  /**
   * Every file under the skill's directory, slash-separated and relative to it, SKILL.md first and the rest sorted — the order the install manifest uses for the same list, so the file the person came for is never found among its references. Symlinks are absent: their bytes live somewhere else and the read path refuses them, so naming one would be a row that can only fail to open. Never null and never empty; a skill with no support files is ["SKILL.md"].
   *
   * @minItems 1
   */
  files: [string, ...string[]]
  /**
   * Whether the list stops at maxFiles with more files on disk. It travels because a card that quietly showed the first 256 of 300 would be asserting a manifest it had not read; the files beyond the cut are still on disk, still backed up and still readable by path.
   */
  truncated: boolean
  /**
   * The cap the listing was measured against, so the viewer's sentence can name the number rather than keeping a second copy of it.
   */
  maxFiles: number
}
