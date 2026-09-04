/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.preview.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of skills.preview — what a person reads before deciding whether to install a skill somebody else wrote. Nothing has been written to disk when this is returned; a refusal comes back as a JSON-RPC error naming the step that refused.
 */
export interface SkillsPreview {
  /**
   * The skill's name, taken from the document's frontmatter and matching ^[a-z0-9][a-z0-9-]{0,63}$. The URL's path never names a skill.
   */
  name: string
  /**
   * The frontmatter description, which is what the assistant is offered on every ask.
   */
  description: string
  /**
   * The WHOLE body, frontmatter stripped. A person adopting instructions reads all of them; an excerpt is not something anybody can approve responsibly.
   */
  body: string
  /**
   * The address that was fetched, as the person gave it — never a redirect target.
   */
  url: string
  /**
   * Every path that will land under the skill's directory, slash-separated and relative to it, SKILL.md first and the rest sorted. This is the manifest the approval names (design §5): a skill's body sends the assistant to its own reference files, so installing SKILL.md alone would install a skill whose instructions point at nothing. The list is what the document itself names — one hop, no repository enumeration — and every entry is fetched before this result is returned, so a file that could not be fetched is a refusal rather than a shorter list. Paths only: reading each file is the viewer's job. Never null and never empty; a skill that references nothing is ["SKILL.md"].
   *
   * @minItems 1
   */
  files: [string, ...string[]]
  /**
   * EVERY static-scan match in the body, in pattern order, never only the first: the 8 KiB bound that makes the assistant's write path attach one finding belongs to a tool result, not to a dialog. Never null: no matches is []. A finding is evidence and never a refusal.
   */
  findings: {
    patternId: string
    line: string
    lineNumber: number
  }[]
}
