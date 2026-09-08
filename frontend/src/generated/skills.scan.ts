/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.scan.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of skills.scan — the static scan's own answer for one discovered skill, by file (nocx-4m1n1). It exists so a person's file list can mark which files the live scan matched WITHOUT reading any of them: skills.files stays a bare directory listing, fast and never made to wait on a read, and this is the SECOND call that arrives once the scan has actually run — server-side, over the whole manifest, reusing skills.audit's own bounded read-and-scan loop with no model call and no file text crossing the wire. `read` names every file THIS call actually examined — its own directory walk runs at its own moment, a moment that can differ from whatever walk produced a viewer's file list (a file created in between), so a path absent from `read` was never claimed clean by this result even if it is also absent from `matches` and `omitted`: a viewer treats a path outside `read` (and outside `omitted`) as NOT YET KNOWN, never as clean. `matches` carries a COUNT per file, never a line number: a line number is the file VIEW's own fact, produced by rescanning the file itself when it is opened (skills.file, file.go:115), and a count taken here from a walk at a different moment must not be asserted to still be that file's current line numbers. The count is how many of the scan's PATTERNS matched at least once in the file, not how many lines matched — the underlying scan reports at most one finding per pattern per file. `omitted` names every file the scan could not read, and why, in the same closed vocabulary skills.audit's own `omitted` uses — a file named there carries no entry in `matches` BY CONSTRUCTION, which is not the same as a file the scan looked at and found nothing in: a viewer must check `read` and `omitted` before drawing any file as clear. A name no root holds is a JSON-RPC error: there is nothing to describe, so every field of a result would be an invention.
 */
export interface SkillsScan {
  /**
   * The skill as it was RESOLVED, from the frontmatter and by root precedence — not the string that was asked for.
   */
  name: string
  /**
   * Which root holds the skill. Provenance is the root and never a field in a file, so it cannot be forged by whatever wrote one.
   */
  provenance: 'authored' | 'builtin' | 'managed' | 'installed'
  /**
   * Every file THIS scan actually read and examined, in manifest order. It is what makes "absent from `matches` means clean" true by construction rather than by timing: this call walks the skill's directory at its own moment, independently of whatever walk produced a viewer's file list, so a path outside both `read` and `omitted` is simply not yet known to this result — never clean. Never null: a skill none of whose files could be read has read == [].
   */
  read: string[]
  /**
   * Every file with at least one static-scan match, and how many of the scan's patterns matched in it — see this schema's own description for why that is a count of patterns and not of lines, and never a line number. A path absent from this array is either clean (scanned, nothing matched) or skipped (see `omitted`); this array alone cannot tell those two apart. Never null: no matches is [].
   */
  matches: {
    path: string
    count: number
  }[]
  /**
   * Every file the scan could NOT read, and why — the same closed vocabulary skills.audit's own `omitted` uses (`too-large`, `not-text`, `unreadable`, or `budget-spent` when an earlier file in manifest order already spent the shared scan budget). THIS IS THE FIELD THAT KEEPS A SKIPPED FILE FROM LOOKING CLEAN: a file named here has no entry in `matches` by construction, which is otherwise indistinguishable from a file the scan looked at and matched nothing in. Never null: nothing omitted is [].
   */
  omitted: {
    path: string
    /**
     * 'too-large' — the file alone is over the per-file read budget. 'not-text' — its bytes are not UTF-8. 'budget-spent' — the shared scan budget (`maxBytes`) was already spent by an earlier file in manifest order. 'unreadable' — it was named by the manifest and could not be read now.
     */
    reason: 'too-large' | 'not-text' | 'budget-spent' | 'unreadable'
  }[]
  /**
   * The shared budget the scan across every file in the manifest was measured against — the same number and the same meaning as skills.audit's own `maxBytes`, so a sentence about a `budget-spent` omission can name it.
   */
  maxBytes: number
}
